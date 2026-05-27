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

---

## Go expert

### Named-map type: `Reactions` JSON codec (`pkg/model/cassandra/message.go`)

**medium** — `message.go:137`: `string(data) == "null"` is idiomatic but allocates a string per call. `bytes.Equal(data, []byte("null"))` avoids the allocation. Hot-path candidate.

**medium** — `message.go:149`: The duplicate-key error is built as `fmt.Errorf("reactions: duplicate key (%s, %s)", ...)` without `%w`. Per CLAUDE.md §3, errors must be wrapped. Fix: define a sentinel `ErrDuplicateReactionKey` and wrap it, or at minimum use `%w` on an underlying cause.

**medium** — `message.go:143`: `fmt.Errorf("reactions: unmarshal: %w", err)` describes the underlying call ("unmarshal") rather than what the function was doing. CLAUDE.md §3 prefers "what the current function was doing, not what failed underneath" — e.g. `"unmarshal reactions array: %w"`.

**low** — `message.go:112-130`: `MarshalJSON` rebuilds the entries slice and re-runs `sort.Slice` on every encode. Necessary for correctness on mutable maps; flagging only as a profiling target.

**nitpick** — `message.go:92` / `Message.Reactions` field: nil maps are elided via `omitempty` + the marshaller returning `"null"`, but the custom `MarshalJSON` is invoked before `omitempty` for non-nil empty maps. Empty `Reactions{}` serialises to `[]` rather than being omitted. Documented and tested — confirming as intentional.

### `structScan` rewrite (`history-service/internal/cassrepo/utils.go`)

**high** — `utils.go:141-143`: When a result column has no matching `cql` tag, `structScan` returns `false` silently. The function's own comment claims it "records an iterator error" — it does not. `scanMsgsFromIter` and siblings treat `false` identically to iterator exhaustion (break + iter.Close() → nil error), so a schema drift silently truncates the result set with no error, no log, no metric. Fix: call `iter.SetErr(...)` (or surface via a returned error) so downstream `iter.Close()` returns a non-nil error, plus a `slog.Warn` at the missing-column site.

**medium** — `utils.go:127-146`: `fieldByTag` and the `values` slice are allocated per row in a tight loop. The struct type and column set are constant across a query; hoisting the tag-index lookup outside the scan loop (e.g. `prepareStructScan(rt, cols)`) would remove ≥2 allocations per row. The prior `MapScan` also allocated per row, so this is not a regression — but it is a known optimisation point.

**nitpick** — `utils.go:119`: `dest interface{}` should be `dest any` (Go 1.18+); the codebase is on Go 1.25.

### Mocks / generation

**clean** — `mock_repository.go` is correctly regenerated (`models.Message` substituted for `cassandra.Message` across all method signatures). No manual edits.

### Concurrency / sync

No new goroutines or sync primitives. No `time.Sleep`. Clean.

### Test coverage of new code

**low** — `message_test.go`: covers nil map, empty map, sorted output, duplicate-key rejection, round-trip. Missing: a `Message` JSON round-trip with explicit `Reactions: nil` vs `Reactions: Reactions{}` to defend the `omitempty` vs `[]` distinction at the enclosing-struct level.

**Overall verdict:** The design is sound and the `MapScan → positional Scan` pivot is well-motivated, but the silent-truncation in `structScan` (utils.go:141-143) is a `high` that must be fixed before merge.

