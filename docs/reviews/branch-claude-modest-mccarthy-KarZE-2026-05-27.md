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

---

## Service: history-service

### (a) Diff correctness

**medium** — `cassrepo/utils.go:117-118`: The doc comment on `structScan` states it "records an iterator error and returns false" when a column has no matching `cql` tag on `dest`, but the code at lines 141-142 simply `return false` with no `iter.SetErr(...)`. The `iter.Close()` calls in `GetMessageByID` / `GetMessagesByIDs` therefore report no error on schema mismatch — callers receive a silently truncated result set, not an error. The comment is actively misleading.

**low** — `cassrepo/messages_by_id_integration_test.go:70` and `cassrepo/thread_messages_integration_test.go:208`: The "full row" round-trip tests remove the old reactions data from their INSERTs (correctly, since v2 is reverted) but do not replace it with a v3 `map[ReactionKey]ReactorInfo` write + assertion. The `reactions` column is now in the schema and `structScan` is responsible for reading it, but no cassrepo integration test exercises a non-nil `Reactions` value through the production read path. The `gocql_map_udt_smoke_test.go` covers raw `iter.Scan(&got)`, not the reflective `structScan` path.

### (b) Scope drift / refactor-readiness

**nitpick** — The change is tightly scoped: two UDT definitions, a schema column-type change in four test `CREATE TABLE` blocks, restoration of `reactions` to `baseColumns`, and the `structScan` rewrite. No new endpoints, no new responsibilities, no new data stores. Scope is appropriate.

### (c) Abstraction changes

**low** — `cassrepo/utils.go:119-147`: The `structScan` rewrite is justified — the `MapScan` panic on `MAP<frozen<UDT>, frozen<UDT>>` is a real, reproducible gocql failure mode (documented in the comment block). Positional `iter.Scan(values...)` is the correct fix. The new implementation is **stricter**, though: every column returned by the query must have a `cql` tag on the destination struct. The prior `MapScan` could tolerate extra columns silently. This stricter contract is documented in the comment but not enforced at the call sites — see (a).

### (d) Design coherence

The PR fits the service's read-side job cleanly. Inline reactions on the message row eliminate the N-parallel-fetch fan-out that the v2 design required. The `Reactions` named-map type with a custom JSON marshaller producing a flat sorted array is idiomatic and aligns with the wire shape in `docs/client-api.md`, which was updated in the same PR. `pkg/model/cassandra` is the right home for the UDT types; the alias chain `models.Message → cassandra.Message` is untouched.

### (e) Project-pattern adherence

No new NATS subjects, streams, or outbox events are introduced — no `pkg/subject` / `pkg/stream` changes needed. No new IDs generated. No JetStream consumer added. Config changes in `config.go` are minor normalisation only. All patterns followed correctly.

### (f) Client-API doc rule

History-service handlers under `chat.user.{account}.…` are registered at `service.go:100-111`. None of their request/response shapes changed at the wire level — the `reactions` field already existed on the `Message` type returned by all handlers; only its on-disk representation and JSON shape changed. `docs/client-api.md` was updated in the same PR (lines 966-1035) to document the v3 flat-array wire shape and explicitly flag the breaking change from the prior `Map<emoji → Participant[]>` shape. The CLAUDE.md §5 hard rule is satisfied.

**Overall verdict: fix-first.** The misleading `structScan` comment masks a real silent-failure mode; full-row integration tests should exercise at least one non-nil v3 `Reactions` map through `structScan` before shipping.

