# Branch Review — `claude/modest-mccarthy-KarZE`

**Date:** 2026-05-27
**Base:** `origin/main`
**PR:** #221
**Diff size:** 28 files, +2295 / -133 (≈ 1691 lines of those are spec/plan/doc text — most code-signal lives in ~600 net lines of Go + ~50 lines of CQL)
**Services touched:** 1 (`history-service`)
**Shared packages touched:** `pkg/model/cassandra`, `pkg/testutil`
**Commits in scope:** 2
- `ca34108` — `refactor(history-service): revert v2 side-table reactions (phase A of v3 pivot)`
- `7e9c5fb` — `feat(reactions): introduce v3 embedded reactions (Commit B of A+B split)`

## Executive summary

The branch migrates message reactions from a side-table design (the v2 iteration on this PR, reverted in Commit A) to embedded `MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>` columns on the four message tables (introduced in Commit B). The pivot eliminates N+1 hydration on read at the cost of larger row scans, requires two new UDTs, a custom JSON marshaller on a named map type, and a rewrite of `structScan` to bypass a gocql `MapScan` panic on `MAP<frozen<UDT>, frozen<UDT>>` columns.

### Findings by severity

| Severity | Count |
|----------|-------|
| critical | 0 |
| high     | 2 |
| medium   | 10 |
| low      | 7 |
| nitpick  | 8 |

### Top-line risk assessment

Two **high**-severity issues land on the same code surface:

1. **Silent truncation in `structScan` on unmapped columns** (`cassrepo/utils.go:141-143`) — flagged independently by 5 of 6 lenses. When a Cassandra column has no matching `cql` tag on the destination struct, the function returns `false` with no `iter.SetErr`, no `slog`, no metric. Callers treat this identically to "iterator exhausted" — a future DDL column addition causes message-history queries to silently return empty pages with no error. Easy fix; high blast radius if left.
2. **No byte-volume cap on read pages now that reactions ride inline** (`cassrepo/utils.go`, `service/messages.go`) — the page-size cap (100 messages) bounds message count but not response size. A single page of viral messages with thousands of reactions each could exceed NATS's 1 MB default payload limit and amplify Cassandra read amplification under TWCS compaction. Architectural concern, not a code defect — flagging for write-path / per-message reaction count enforcement in the future `addReaction` PR.

Everything else is **medium** or below: a misleading "idempotent" claim on the v3 migration script that risks dropping reaction data on re-runs against populated dev keyspaces; an `UnmarshalJSON` without input size bounds; one error not wrapped with `%w`; a few hot-path allocator opportunities; and untested edge paths in the rewritten `structScan` and `MarshalJSON` nil branch.

The design itself is sound — the A+B split preserves revertability, the new types are clean, the JSON wire shape matches the docs, the gocql smoke test gates the core compatibility risk, and the existing N+1 fan-out is gone. **Verdict: fix-first on the two high items, ship the rest.**

