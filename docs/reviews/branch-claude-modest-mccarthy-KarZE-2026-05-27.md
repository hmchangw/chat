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

---

## Test-automation

### TDD compliance

**medium** — `pkg/model/cassandra/message_test.go`: All new exported types (`ReactionKey`, `ReactorInfo`, `Reactions`) and both custom JSON methods land in the **same commit** (`7e9c5fb`) as their tests. The Red phase is unverifiable from git history — tests and implementation arrived atomically. Per CLAUDE.md §4 the Red-Green-Refactor cycle is mandatory; this is a recurring pattern on the branch but worth flagging.

### Coverage

**low** — `pkg/model/cassandra/message.go:109-111`: `MarshalJSON`'s `nil` branch is unreached at runtime — the only nil-map test marshals through the enclosing `Message` struct with `omitempty`, which causes the encoder to skip the field entirely without ever calling `MarshalJSON`. A direct `json.Marshal(Reactions(nil))` would close the gap and confirm the branch is reachable.

### `structScan` rewrite coverage

**medium** — `history-service/internal/cassrepo/utils_test.go`: The new "column in result has no matching cql tag → return false" early-exit (utils.go:141-143) has zero unit tests. The existing tests only cover non-pointer and pointer-to-non-struct inputs. A table-driven unit test with a mocked `gocql.Iter` providing a surplus column would cover this.

**medium** — `history-service/internal/cassrepo/*_integration_test.go`: Every cassrepo integration test removed the `reactions` INSERT arguments and assertions (correctly reverting v2). The `reactions` column is declared in the schema but is never written with a non-nil value in any cassrepo test. The gocql smoke test (`pkg/model/cassandra/gocql_map_udt_smoke_test.go`) round-trips the UDT map but **not through `structScan`** — it uses raw `iter.Scan(&got)` against a dedicated smoke table. If `structScan` regresses or a future code change breaks the reflective path on `MAP<UDT,UDT>`, no integration test would fail.

### Mock hygiene

`mockgen` is not installed in this sandbox so a live `make generate` staleness check could not run. Visual inspection: `mock_repository.go` was hand-updated in `ca34108` to match the `MessageReader` interface shrink; all 14 method signatures use `models.Message` consistently with `service.go`. CI's `make generate` gate should confirm.

### Build-tag & TestMain discipline

All new integration test files carry `//go:build integration`. `pkg/model/cassandra/main_test.go` correctly wires `testutil.RunTests(m)` behind the integration tag. Compliant.

### Shared testutil container usage

`pkg/model/cassandra/gocql_map_udt_smoke_test.go` uses `testutil.CassandraKeyspace(t, ...)` for the shared container, then opens a second keyspace-pinned session manually because it needs `cluster.Keyspace = keyspace` for unqualified UDT name resolution. The inline `cluster.CreateSession()` is justified and follows CLAUDE.md's carve-out for tests that need specific session configuration.

### Table-driven structure

**nitpick** — `pkg/model/cassandra/message_test.go:213-329`: The `TestReactions_MarshalJSON_*` and `TestReactions_UnmarshalJSON_*` tests are individual top-level functions rather than table-driven subtests. CLAUDE.md §4 prefers tables for multi-input/output variations of the same logic. Acceptable for 8 cases but inconsistent with project preference.

### gocql smoke test design

`TestGocqlMapUDTRoundTrip` writes two reactions and reads them back. Correctly gates the panic path. Does NOT exercise the NULL column path (write a row with no reactions, read back) — which is the common case in cassrepo today since no integration test populates the column. Acceptable given the smoke test's stated purpose.

**Overall verdict:** Unit-test surface for the JSON codec is thorough on error paths; the regression-detection gap is in **integration coverage** — no test writes a non-nil `Reactions` through `structScan`, so a revert of the `MapScan → positional Scan` change or a future structScan regression would not be caught by CI.

---

## Bug & security

### SAST results

| Tool         | Status | Notes |
|--------------|--------|-------|
| gosec        | PASS   | No new findings introduced by this branch. |
| govulncheck  | PASS   | No reachable vulnerabilities. |
| semgrep      | FAIL   | Pre-existing Python/`pyo3` environment crash in this sandbox; **not attributable to this PR**. Re-run in CI to confirm. |

### Findings

**high** — `cassrepo/utils.go:139-143`: structScan unmapped-column returns `false` silently; no error propagated to caller. When Cassandra returns a column with no matching `cql` tag on the destination struct, `structScan` returns `false` without `iter.SetErr`. Calling loops (`scanMsgsFromIter`, `scanMessagesUpTo`) treat `false` identically to iterator exhaustion → `break` → `iter.Close()` returns nil. Result: paginated reads return empty pages with `HasNext: false` and no error; `GetMessageByID` returns `(nil, nil)` ("not found"). A future DDL column addition would cause a complete silent availability failure with no metrics, no log line, no error to the client. (Reflagged here from the Go-expert lens — same line, same root cause, multiple-angle confirmation.)

**medium** — `pkg/model/cassandra/message.go:136-161`: `UnmarshalJSON(data []byte)` calls `json.Unmarshal(data, &entries)` without any guard on `len(data)`. The comparable cursor decoder in `utils.go:20-23` correctly bounds `len(encoded)` against `maxCursorBytes`; the same discipline should apply here. A malicious or corrupted Cassandra row could trigger unbounded allocation. Risk is low in normal operation but worth a defensive cap.

**medium** — `docker-local/cassandra/init/90-migrate-reactions-to-v3.cql:19-28`: The migration script unconditionally `DROP IF EXISTS reactions` then `ADD IF NOT EXISTS reactions ...`. The header comment frames this as "idempotent" — it is **only** idempotent on an empty v3 keyspace. On a populated v3 keyspace (the normal state after any reaction is added), the DROP tombstones all reaction data. Most Cassandra container configurations execute the `init/` directory on each fresh start, so a `docker-compose down && up` cycle with persistent volumes would silently destroy reaction history. The header warning is present but downplays the severity of re-runs. Recommend either rephrasing the comment ("safe to re-run only against a keyspace with no reaction data") or guarding the DROP behind a sentinel check.

**low** — `cassrepo/utils_test.go`: No unit test covers the unmapped-column path of `structScan`. Combined with the silent-false-return issue above, this path is both unguarded and untested.

**low** — `pkg/model/cassandra/message.go:136-161`: If `data` is a well-formed JSON object `{}`, `json.Unmarshal` returns an opaque error (`cannot unmarshal object into Go value of type []cassandra.reactionEntry`), which gets wrapped and returned. The `TestReactions_UnmarshalJSON_MalformedJSON` case covers `{not valid json` but not the well-formed-but-wrong-type boundary. Low impact; a typed schema-mismatch error message would be friendlier.

**nitpick** — `pkg/model/cassandra/message.go:76`: The doc comment claims emoji values are NFC-normalised, but the Go code does not enforce normalisation — the byte sequence read from Cassandra is stored verbatim. NFC enforcement is delegated to the (future) `addReaction` PR at the gatekeeper layer. Documented behaviour; flagging for visibility.

**nitpick** — `pkg/model/cassandra/message.go:112-131`: `MarshalJSON` ranges the map (non-deterministic iteration order) then `sort.Slice` imposes canonical order. Concurrency-safe by Go contract — `r Reactions` is a value receiver (map header copy) and the local slice is private. No race risk.

**Overall verdict:** The v3 embedded-reactions model is structurally sound and the marshal/unmarshal logic is correct. The `structScan` silent-availability gap is the only `high` introduced; the migration-script `DROP THEN ADD` framing is a `medium` that's easy to fix with a comment rewrite.

---

## Performance

### `Reactions.MarshalJSON` hot-path cost

**medium** — `pkg/model/cassandra/message.go:108-131`: For a non-nil zero-length `Reactions{}`, `MarshalJSON` runs to completion: `make([]reactionEntry, 0, 0)`, `sort.Slice` on empty slice, `json.Marshal([])`. The `omitempty` tag on `Message.Reactions` does not eliminate this because Go consults the custom `MarshalJSON` before testing emptiness. An early `if len(r) == 0 { return []byte("[]"), nil }` guard between the nil check and the `make` call saves two allocations and a marshal round-trip per empty map. Per-page impact is small but the path is hot.

For non-empty maps: `make([]reactionEntry, 0, len(r))` is correctly sized, append never reallocates, and the `O(k log k)` sort is unavoidable for stable output. Acceptable.

### `structScan` allocator footprint vs prior `MapScan`

**low** — Net improvement. Prior `MapScan` allocated a `map[string]interface{}` per row plus internal `RowData` allocations, and was non-functional for `MAP<frozen<UDT>, frozen<UDT>>` (panic). New path allocates one `map[string]reflect.Value` (sized exactly) and one `[]interface{}` per row, with `iter.Columns()` returning a cached slice (zero-alloc). Net comparable alloc count, no boxing for UDT columns, panic eliminated.

**medium** — `cassrepo/utils.go:127-145`: `fieldByTag` is rebuilt on every `structScan` call even though the struct type and column set are constant across all rows of a given query. Hoisting the tag index (e.g. `prepareStructScan(rt, cols) → []interface{} pointers`) outside the per-row loop would remove 2 allocs/row on 100-message pages — 200 maps eliminated per page. Medium effort, meaningful at sustained load.

### Per-row scan cost with inline reactions map

**high** — `docker-local/cassandra/init/10-table-messages_by_room.cql` (architectural, not a code bug): The bucketed tables use TWCS (1-day window). With reactions inline, a viral message can accumulate thousands of `(emoji, userAccount)` map entries. Cassandra deserialises the entire `MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>` column on every read regardless of whether the caller needs reactions. At ~60 bytes per entry, a 10 000-reaction message adds ~600 KB to the row footprint; a 100-message page crossing such a partition could transfer tens of MB. No per-row reaction-count cap exists in the schema or the future write path; the existing page-size cap (100 messages) bounds count, not byte volume. **Action belongs in the upcoming `addReaction` PR** — enforce a max-reactions-per-message at the write path.

### Page-size byte volume

**high** — `cassrepo/utils.go:55`, `service/messages.go:22`: With reactions inline, a single page response can produce a multi-MB NATS reply payload. NATS's default `max_payload` is 1 MB; deployments using the default will hit `nats: maximum payload exceeded` on heavy-reaction pages. Mitigations: cap per-message reactions (write path); reduce page size when reactions are dense; bound page responses by estimated byte size. Co-related to the architectural high above.

### N+1 elimination

**low** — confirmed clean. `service/messages.go` and `service/threads.go` have no reaction-hydration call sites. The single `"hydrating thread parent messages"` log line in `threads.go:174` is unrelated (parent-message enrichment, pre-existing). Fan-out gone.

### Concurrency primitives

**low** — no issues. No new goroutines, channels, mutexes, `sync.Once`, or `WaitGroup` in the changed files. Existing errgroup patterns in `messages.go` unchanged.

### Caching

**nitpick** — `pkg/model/cassandra/message.go:108`: No missed memoisation. `Reactions` values are read-path-only and not hot enough to warrant a cache.

**Overall verdict:** The N+1 elimination and the `structScan` correctness fix are clear wins. The architectural risk — no per-message reaction-count cap — is the highest-impact issue from a perf lens; it manifests only under viral-message load but cannot be fixed in this PR (the write path lives elsewhere). Flag for the upcoming `addReaction` PR.

---

## Observability

### `structScan` silent miss — diagnostic value degraded

**medium** — `cassrepo/utils.go:141-143`: When a Cassandra column has no matching `cql` tag, the function returns `false` without calling `iter.Scan`. gocql never sets an error on the iterator; `iter.Close()` returns nil. In `messages_by_id.go:24` the `!found` path silently returns `(nil, nil)` — indistinguishable from "row not found". In paged readers, the unmapped-column case causes rows to be silently dropped with no log line, no error return, no observable signal.

A one-line `slog.Warn("structScan: unmapped column", "column", col.Name)` directly above `return false` would restore diagnostic visibility for schema-drift or query/struct mismatches without changing the caller contract. (Same line as the bug-lens `high` and the Go-lens `high` — this lens scores it as `medium` because the action is purely an observability improvement; the underlying correctness fix is captured under the other lenses.)

### `request_id` propagation

**low** — `service/messages.go:335, 407, 449, 454`: The PR correctly back-fills `request_id` on all read-path slog lines touched by this diff. Four pre-existing error log sites NOT touched by this PR still lack `request_id`:
- `messages.go:335` — `"edit: update content"` (Cassandra write failure on EditMessage)
- `messages.go:407` — `"delete: soft-delete"` (Cassandra write failure on DeleteMessage)
- `messages.go:449, 454` — `"canonical marshal/publish failed"` (best-effort publish failure)
- `threads.go:149` — `"unhandled thread filter"`
- `threads.go:194` — `"thread parent message belongs to unexpected room"`

These are **pre-existing gaps**, not regressions introduced here. Out of scope for this PR but worth a follow-up.

### Structured logging discipline

**nitpick** — No `fmt.Println`, `log.Println`, or string-interpolation slog calls introduced anywhere in the diff. All new or modified log lines use correct key-value pairs. Clean.

### Secret / payload leakage

**nitpick** — `Reactions` contains `engName`, `chnName`, `account` (PII-adjacent but not credentials). None of these appear in any log line. Message bodies are not logged at any site touched by this diff. Clean.

### OTel spans

**low** — The service already instruments via `oteljetstream` at the NATS transport layer (`main.go:69`). No new handler methods or Cassandra call paths were introduced; the `structScan` refactor is purely internal to existing query paths. No net new span coverage is expected or missing.

### Prometheus metrics

**nitpick** — `pkg/metrics` does not exist; the project uses OTel tracing rather than Prometheus counters on the read path. No reaction-specific counter or histogram was expected here based on the existing instrumentation pattern. No gap.

### `UnmarshalJSON` error sites

**nitpick** — `pkg/model/cassandra/message.go` `UnmarshalJSON` returns wrapped errors. These surface through the standard JSON decode path; any caller that logs the decode error receives the full chain. No additional instrumentation needed at the model layer.

**Overall verdict:** Observability is a net improvement on the touched paths — `request_id` back-filled on seven log lines. The `structScan` silent-drop on unmapped columns (`utils.go:142`) is a diagnostic gap that should be addressed with a `slog.Warn` alongside the correctness fix.

---

## Prioritized action list

Ordered by severity, then impact ÷ effort. The first two items belong in this PR; items 3–8 are cleanup that can ship in a follow-up or in the upcoming `addReaction` PR.

| # | Severity | Action | Where | Why |
|---|----------|--------|-------|-----|
| 1 | **high** | Make `structScan` fail loudly on unmapped columns. Call `iter.SetErr(fmt.Errorf("structScan: unmapped column %q", col.Name))` (or surface via a returned error) before `return false`, plus `slog.Warn` with `column` and the destination type name. Update the function's doc comment to match the actual behaviour. | `history-service/internal/cassrepo/utils.go:141-143` | A future DDL column addition would otherwise silently truncate every paged read. Currently the comment lies about recording an error; 5 of 6 lenses flagged this. |
| 2 | **medium** | Rewrite the migration-script header to drop the word "idempotent" and explicitly warn that re-runs against a populated v3 keyspace will destroy reaction data. Optionally guard the DROP behind a sentinel column-presence-and-empty check. | `docker-local/cassandra/init/90-migrate-reactions-to-v3.cql:1-18` | Dev-environment data loss waiting to happen on the next `docker-compose down && up` cycle with persistent volumes. |
| 3 | **medium** | Add at least one cassrepo integration test that writes a non-nil `Reactions{}` map via raw CQL and round-trips it through `GetMessageByID` (or one of the page readers). | `history-service/internal/cassrepo/messages_by_id_integration_test.go` | The gocql smoke test covers raw `iter.Scan` but not the `structScan` path; a structScan regression would not be caught by CI today. |
| 4 | **medium** | Wrap the duplicate-key error in `Reactions.UnmarshalJSON` with `%w` (sentinel or otherwise). Also tighten the error wrap on line 143 from `"reactions: unmarshal: %w"` to `"unmarshal reactions array: %w"` per CLAUDE.md §3. | `pkg/model/cassandra/message.go:143, 149` | Error chain breaks `errors.Is` / `errors.As` for downstream callers. |
| 5 | **medium** | Add an early `if len(r) == 0 { return []byte("[]"), nil }` guard in `Reactions.MarshalJSON` to avoid the slice+sort+marshal round-trip on empty maps. | `pkg/model/cassandra/message.go:108-131` | Hot path; tiny diff for measurable per-page allocator savings on messages with no reactions. |
| 6 | **medium** | Hoist the `fieldByTag` index out of `structScan` so it isn't rebuilt per row. Either expose `prepareStructScan(rt, cols)` returning a reusable `[]interface{}` plan, or compute the field map once per query in `scanMsgsFromIter`. | `history-service/internal/cassrepo/utils.go:127-145` | Removes 2 allocs/row on every paged read. |
| 7 | **medium** | Bound `len(data)` in `Reactions.UnmarshalJSON` (defensive cap, e.g. 256 KB). Add a table-driven test covering `{}` and other well-formed-but-wrong-type inputs alongside the existing malformed-JSON case. | `pkg/model/cassandra/message.go:136-161` | Defence-in-depth against corrupted rows; closes a small test-coverage gap. |
| 8 | **low / nitpick** | Convert `dest interface{}` to `dest any` (Go 1.25 convention); add a `Message`-level round-trip test for `Reactions: nil` vs `Reactions: Reactions{}` to lock the `omitempty` vs `[]` distinction; convert the `TestReactions_MarshalJSON_*` / `_UnmarshalJSON_*` suite to table-driven subtests. | `history-service/internal/cassrepo/utils.go:119`, `pkg/model/cassandra/message_test.go` | House-style cleanups; cheap. |

The architectural perf concerns (no per-message reaction-count cap; page responses not bounded by byte volume) cannot be fixed in this PR — the write path lives in the upcoming `addReaction` change. Capture them as known-issues against that PR's spec.
