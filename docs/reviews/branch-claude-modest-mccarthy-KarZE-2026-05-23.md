# Branch Review — `claude/modest-mccarthy-KarZE`

**Date:** 2026-05-23
**Base:** `origin/main` (`3cd3ab8`)
**Branch HEAD:** `79ee428`
**Diff size:** 29 commits, ~36 files (rebased onto origin/main)

## Executive summary

The branch implements the **message reactions side-table** feature per the spec at `docs/specs/message-reactions-table.md`: removes the embedded `reactions MAP<…>` column from 4 Cassandra message tables, creates a dedicated `chat.message_reactions((message_id), emoji)` side table with LCS compaction, adds two new readers (`GetReactionsByMessageID`, `GetReactionsByMessageIDs`) on the cassrepo, wires a `hydrateReactions` service helper into all 6 history-service handlers that return messages, and updates the schema/client-API docs.

Six expert lenses ran in parallel against the rebased branch.

### Findings by severity

| Severity | history-service | Go | Tests | Bug/Sec | Perf | Obs | **Total** |
|---|---|---|---|---|---|---|---|
| critical | 0 | 0 | 0 | 0 | 0 | 0 | **0** |
| high | 1 | 2 | 0 | 0 | 0 | 0 | **3** |
| medium | 4 | 4 | 0 | 0 | 2 | 2 | **12** |
| low | 4 | 4 | 3 | 1 | 3 | 1 | **16** |
| nitpick | 1 | 6 | 0 | 3 | 2 | 1 | **13** |

### Top-line risk assessment

**Ready to merge with follow-up tracked.** No critical findings. The two `high` items are both worth addressing pre-merge (double-error-wrap noise and the `messageIDs[:0:0]` aliasing footgun). Most `medium` findings are observability/operability follow-ups (metrics + sub-spans around the new fan-out) or interface-design refinements (Reader bloat, redundant singular method, positional-int constructor risk) that don't block the feature but should be tracked.

SAST: `gosec` PASS, `govulncheck` PASS, `semgrep` blocked locally by a Python env issue (broken cryptography binding) — CI's pinned semgrep environment is the gate of record.

Integration tests and the new benchmark cannot run locally (Docker unavailable). All integration-tagged code compiles, vets clean, and `make lint` reports 0 issues. CI runs the real integration suite.

---

## Service: history-service

### (a) Diff correctness vs existing conventions

**medium —** `GetReactionsByMessageID` (`history-service/internal/cassrepo/message_reactions.go:19-36`) deviates from the established iterator pattern:
- `messages_by_id.go:13` and `messages_by_room.go` consume rows via `structScan` / `scanMsgsFromIter`; the new method uses a raw `iter.Scan(&emoji, &users)` loop. Acceptable (no struct tag exists for the `(emoji, users)` pair) but worth a note.
- Error wrap `"loading reactions for message %s"` (line 33) describes the data, not the action — peers wrap as `"querying message by id %s: %w"` (`messages_by_id.go:22`). Style drift, not a bug.

**high — double-error wrap loses signal.** In `GetReactionsByMessageIDs`, the goroutine wraps `GetReactionsByMessageID` errors with `"loading reactions for message %s: %w"` (`message_reactions.go:79`); the inner call already wraps with the *same* prefix at line 33; then `g.Wait()` wraps a third time at line 91. Final error reads: `loading reactions for messages: loading reactions for message m1: loading reactions for message m1: <gocql err>`. Drop the per-goroutine wrap (just `return err`) — the leaf wrap names the message; the outer wrap names the batch.

### (b) Scope drift / file bloat

**low —** `messages.go` is now ~502 lines (was ~478). `hydrateReactions` lives next to handlers but is a cross-cutting helper used by `threads.go` too. Belongs in a sibling `reactions.go` (file already exists at `service/reactions_test.go`). Cohesion nit, not a blocker.

### (c) Abstraction changes

- **`ReactionMap` type alias** (`message_reactions.go:14`) — **nitpick**. As an alias (`= map[...]...`), it adds zero type safety, only naming. Inconsistent: the interface declarations in `service.go:26-27` still mention `cassrepo.ReactionMap` AND `map[string]cassrepo.ReactionMap`. Either alias both or neither.
- **`hydrateReactions` helper** — **earned**. Justified across 5 callers.
- **4-arg `NewRepository`** (`cassrepo/repository.go:21`) — **medium**. Positional `int, int` is fragile (`365, 50` shows up at 30+ test sites; nothing prevents swapping them). Either functional-options pattern or a config struct. The adjacent `int, int` will silently mis-wire on any future reorder.
- **`MessageReader` interface extension** — **low**. `GetReactionsByMessageID` is exactly `GetReactionsByMessageIDs([id])[id]` plus an absence check. The singular method is only used once (`messages.go:303`). Worth folding to one method on the interface (the helper can still expose both at the repo level for the bench / integration tests).

### (d) Design coherence — **OK**

Hydration is post-auth in all 6 sites (`messages.go:114,170,273,303`; `threads.go:104,208`) — every call sits AFTER `GetHistorySharedSince` / access-window check and AFTER `redactUnavailableQuotes`. Correct ordering.

### (e) Project-pattern adherence

- No new NATS subjects; existing handlers untouched.
- `idgen` not relevant.
- Concurrency cap is env-driven and clamped (`cassrepo/repository.go:24-26`); default 50 (`config/config.go`).
- **medium — `REACTIONS_FETCH_CONCURRENCY` is per-request, not global.** With N concurrent NATS requests, true ceiling is `N × 50` Cassandra in-flight reads. No global limiter, no doc note, no metric. At 50/req × even modest concurrency this will saturate the gocql connection pool under load. Track for follow-up metric work.
- Generated mocks (`mock_repository.go`): regenerated, not hand-edited. OK.
- `cmd/main.go` change is one-line and minimal.

### (f) Client-API doc — **low**

`docs/client-api.md:949` adds one clause to the existing `reactions` row. Sufficient for behaviour parity (no schema change visible to clients). Worth a one-sentence "Reactions are stored server-side in a side table" note for client implementers.

### Other findings

- **medium — `AnyTimes()` shadowing**: `messages_test.go:75-82` adds a permissive `AnyTimes()` default for both reaction methods on `newServiceWithRoomMock`. The comment at lines 69-74 correctly flags FIFO-match risk and points to `newServiceNoReactionDefault`. The 5 new hydration tests use the strict scaffold — good. **Risk**: every other test using `newServiceWithRoomMock` (~25 sites) now silently passes reaction calls. If a future bug stops calling `GetReactionsByMessageIDs`, no existing test catches it; only the 5 new tests would.
- **medium — `repository_test.go` coverage**: 19 lines only test the clamp (`repository_test.go:11-18`). No test verifies the field is wired through to `GetReactionsByMessageIDs`.
- **low — large mock regeneration**: 30+ method signatures changed in `mock_repository.go` because of a pre-existing `cassandra.Message` → `models.Message` type rename surfaced by `make generate`. Confirm no behaviour change beyond type swap.
- **nitpick — bench concurrency hardcoded**: `message_reactions_bench_test.go:18` uses `50`. Worth `b.Run` sub-benches at 1/10/50/200 to show the scaling curve since "default 50" is now a load-bearing config decision.

---

## Go Expert Lens

### high

**H1 — Double-wrap of "loading reactions for message X" loses signal** — `message_reactions.go:79` and `:91`. Inner call already returns `fmt.Errorf("loading reactions for message %s: %w", messageID, err)` (line 33); the per-goroutine site at :79 re-wraps with the *same* string ("loading reactions for message %s"), and `g.Wait()` is then wrapped again at :91 with "loading reactions for messages". Final error reads: `loading reactions for messages: loading reactions for message m1: loading reactions for message m1: <gocql err>`. Drop the :79 wrap (just `return err`) — the inner wrap already names the message and the outer wrap already names the batch.

**H2 — `messageIDs[:0:0]` mutation footgun + missing WHY comment** — `message_reactions.go:48`. `ids := messageIDs[:0:0]` is non-obvious enough to warrant the comment CLAUDE.md §3 calls for. The three-arg slice with zero cap *forces* a fresh backing array on first `append`, so callers' slices are safe today — but a reader can't tell that without re-deriving it. Either add `// fresh backing array; capacity 0 forces append to allocate, never aliasing caller's slice` or just `ids := make([]string, 0, len(messageIDs))` — equally cheap, no aliasing trap.

### medium

**M1 — Per-goroutine `fmt.Errorf` *inside* the lambda happens even on `gctx.Done()`** — `message_reactions.go:73-80`. When the parent context is canceled, every queued goroutine returns `fmt.Errorf("loading reactions for message %s: %w", id, ctx.Canceled)`, and `errgroup.Wait` returns the *first* such error. The handler at `messages.go:114` then logs `error=loading reactions for message <some-random-id>: context canceled` — misleading; the cancellation didn't originate from any specific message. Use `if err != nil { return err }` (see H1) and the leaf wrap at :33 stays meaningful when it's a real Cassandra error.

**M2 — `MessageReader` interface bloat** — `service/service.go:18-29`. Adding two reaction methods to `MessageReader` couples every consumer of message reads to reactions. A `ReactionReader` interface (consumer-defined, two methods) composed into `MessageRepository` would respect ISP and the project's "interfaces in the consumer" rule (CLAUDE.md §3). Worth splitting before more side-table reads land (pins, bookmarks, etc.).

**M3 — `GetReactionsByMessageID` is redundant** — `message_reactions.go:18-35`. Trivially derivable from `GetReactionsByMessageIDs([]string{id})[id]`. Two methods on the interface and two mock setups in every test for negligible benefit. Either drop it and have `GetMessageByID` use the plural form, or document why the singular path exists (avoids errgroup/semaphore overhead for the hot single-message path — plausible, but say so).

**M4 — Wrap-phrasing inconsistency: gerund vs imperative** — `messages.go:494` (`"hydrating reactions: %w"`) and `message_reactions.go:33,79,91` (`"loading reactions for …"`). Project pattern elsewhere is imperative-noun (`"get room times"`, `"persist message"`). Minor, but inconsistent.

### low

**L1 — `ReactionMap` alias invites confusion** — `message_reactions.go:13`. `type ReactionMap = map[string][]cassandra.Participant` (alias, no methods, no identity). Fine choice — readers see `ReactionMap` and the underlying type in error messages — but consider whether `service/service.go` should re-export it as `service.ReactionMap` since the interface signature uses `cassrepo.ReactionMap`, leaking the repo package into every consumer's type vocabulary.

**L2 — `omitempty` on `Message.Reactions` is correct but subtle** — `pkg/model/cassandra/message.go:93`. Nil map serializes as absent; empty map `{}` would serialize. Hydration leaves nil for "no reactions" (`messages.go:498`), so wire payload matches old `cql:"reactions"` behavior. Worth a one-liner doc on the field.

**L3 — `MessageReactionRow` `Row` suffix breaks `pkg/model/cassandra/` convention** — `pkg/model/cassandra/reaction.go:6`. Sibling carriers (`Message`, `Participant`, `File`, `Card`, `QuotedParentMessage`) have no suffix. Either drop the suffix (`MessageReaction`) or accept it as deliberate disambiguation from a future API-level type — note the choice in the comment.

**L4 — `var _ MessageRepository = (*cassrepo.Repository)(nil)` lives in `service.go:117`** — should live next to the implementation (`cassrepo/repository.go`) so a breaking change to `Repository` fails the build in the package owning it, not in `service`. The current placement also forces `service` to import `cassrepo` purely for the assertion.

### nitpick

**N1 — `newServiceNoReactionDefault`** — `messages_test.go:107`. Name reads as "no reaction default" (no default reaction?). Suggest `newServiceStrictReactions` or `newServiceWithoutReactionStubs`.

**N2 — `users = nil // gocql reuses the slice header otherwise`** — `message_reactions.go:30`. Good comment; exactly the WHY style CLAUDE.md §3 prescribes (slight correction: gocql reuses the backing *array*, not the header).

**N3 — `for i := range msgs` loop** — `messages.go:486,500`. No comment on the `rangeValCopy` motivation. One-line note `// index loop avoids copying 440-byte Message struct (gocritic rangeValCopy)` would help future readers resist "fixing" it.

**N4 — `slog.Error` vs `slog.ErrorContext`** — all 5 hydration sites (`messages.go:114,170,273`; `threads.go:104,208`) plus `GetMessageByID:303` use `slog.Error(...)` not `slog.ErrorContext(c, ...)`. If the project's slog handler reads ctx-attached attrs (e.g. requestID), `slog.Error` skips them. Worth confirming with the router middleware pattern.

**N5 — `testing.TB` widening of `CassandraKeyspace`** — `pkg/testutil/cassandra.go:103`. Safe; body uses only TB methods. Backward-compatible.

**N6 — Bench `b.ReportAllocs()` missing** — `message_reactions_bench_test.go`. Worth adding to surface GC-pressure changes alongside ns/op.

---

## Test-Automation Lens

### Verdict
All new exported and internal functions ship with tests. Mocks are current. `-race` is wired (`Makefile:59,61`). No `high`-severity findings.

### TDD coverage (CLAUDE.md §4)

| Symbol | Test |
|---|---|
| `cassrepo.GetReactionsByMessageID` | `message_reactions_integration_test.go:24-54` (happy + empty partition) |
| `cassrepo.GetReactionsByMessageIDs` | `message_reactions_integration_test.go:56-138` (happy / nil-empty / dedup / 100-fan-out / ctx-cancel) |
| `cassrepo.ReactionMap` (alias) | exercised transitively |
| `cassandra.MessageReactionRow` | `pkg/model/cassandra/reaction_test.go:10-25` (JSON round-trip) |
| `cassrepo.NewRepository` (4-arg) | `repository_test.go:11-19` (clamps `-1`, `0`; passthrough `50`) |
| `service.hydrateReactions` | `service/reactions_test.go:18-66` (empty / populates-matching / error-wraps) |

`TestMain` is present at `cassrepo/main_test.go:1-11`. `testutil.RunTests` wires `TerminateAll` per CLAUDE.md §4.

### Mock staleness
`make generate SERVICE=history-service` regenerates only a one-line header comment on `mocks/mock_repository.go` (path string change due to `go generate` cwd), not the interface body. The new `MessageReader` methods are already wired through the mock (used at `reactions_test.go:21`, `threads_test.go:627`, `messages_test.go:1032`). **Not stale.** Reverted with `git checkout`.

### Coverage (function-level, unit pass)

```
service/messages.go:484           hydrateReactions          100.0%
cassrepo/repository.go:22         NewRepository             100.0%
cassrepo/message_reactions.go:19  GetReactionsByMessageID     0.0%  (integration-only)
cassrepo/message_reactions.go:46  GetReactionsByMessageIDs    0.0%  (integration-only)
```
Service package = **89.7%** (above 80% floor). Cassrepo aggregate 15.3% is expected — repo methods are intentionally covered by `//go:build integration` testcontainers, per CLAUDE.md §4.

### Findings

**low — no test exercises errgroup partial-failure sibling cancellation.** `message_reactions_integration_test.go:121-137` covers pre-cancelled context, but nothing forces one of N goroutines to return a gocql error and asserts siblings observe `gctx.Done()`. Hard to provoke deterministically against a real cluster; acceptable gap.

**low — no test asserts the concurrency cap is honored under load.** `LargeFanOut` (line 102-119) sends 100 IDs with `reactionsConcurrency=50` but only asserts the result map size, not that in-flight count never exceeded 50. Would need an instrumented session wrapper; out of scope for repo tests.

**low — `iter.Close()` error propagation untested.** `GetReactionsByMessageID` wraps `iter.Close()` errors at `message_reactions.go:33`. No test injects a Close error (would require a fault-injecting session). Acceptable.

**info — permissive `AnyTimes()` reaction defaults in `newServiceWithRoomMock`** (`messages_test.go:81-89`) could mask a regression where a handler silently stops hydrating. Mitigated by `newServiceNoReactionDefault` and routing all "HydratesReactions" assertions through it (`messages_test.go:303,331,359,1023`; `threads_test.go:611,642`). Reasonable trade-off.

**info — `reactionsConcurrency=50` passthrough test** is a single assertion, but the function body has no other branches after the clamp; equivalence-class coverage complete.

### Other checks
- `setupCassandra` widened to `testing.TB` — bench file uses it cleanly with `*testing.B`.
- All new integration tests use `testutil.CassandraKeyspace` via the existing `setupCassandra` helper — no inline `testcontainers.GenericContainer`. Compliant with CLAUDE.md §4.
- Table-driven structure used where appropriate.
- `MessageReactionRow` has JSON round-trip test only (no BSON tag in struct, so BSON test unnecessary — `reaction.go:5` comment is explicit).

---

## Bug & Security Lens

### SAST results
- **gosec**: PASS (no medium+ findings introduced by this branch).
- **govulncheck**: PASS (0 vulnerabilities; 13 unreached transitive findings unchanged from main).
- **semgrep**: FAIL — `pyo3_runtime.PanicException` / `ModuleNotFoundError: _cffi_backend`. **Environment issue** (broken Python ↔ Rust crypto binding in the harness), NOT branch-introduced. CI's normal pinned semgrep environment is the gate of record.

### Findings

**low — `messageIDs[:0:0]` aliasing footgun** — `message_reactions.go:54`. Cap=0 forces `append` to reallocate today, so the caller's slice is safe — but a future refactor to `messageIDs[:0]` (or `messageIDs[:0:len(messageIDs)]`) would silently overwrite the caller's input. The comment ("Dedupe.") doesn't flag the reliance on cap=0. Prefer `ids := make([]string, 0, len(messageIDs))`. (Cross-references Go-lens H2.)

**nitpick — double-contextualized error wrap** — `message_reactions.go:79`. Cross-references Go-lens H1.

**nitpick — gocql comment slightly misleading** — `message_reactions.go:30` says "gocql reuses the slice header otherwise". It reuses the *backing array*, not the header. Keep the `users = nil` reset (it IS necessary), just tweak the wording.

**nitpick — silent clamp of `reactionsConcurrency`** — `repository.go:23`. CLAUDE.md §3 discourages silent handling; consider a `slog.Warn` at construction so an operator who set `REACTIONS_FETCH_CONCURRENCY=0` learns it was clamped.

### Things explicitly verified clean

- **No race on `out`**: every write to `out[id]` is under `mu.Lock()`; `reactionsConcurrency` is immutable post-construction (`message_reactions.go:64-87`).
- **`users = nil` reset between Scan iterations** (`message_reactions.go:30`) prevents gocql backing-array reuse from cross-polluting emoji entries.
- **No goroutine / iterator leak**: `g.Wait()` blocks on all spawned goroutines; `iter.Close()` always called; semaphore acquire is selected against `gctx.Done()` so cancellation never blocks (`message_reactions.go:70-75`).
- **No CQL injection**: all queries use `?` placeholders; only test fixtures use `fmt.Sprintf` and only for keyspace/table DDL with fixed format strings.
- **No secret logging**: handlers log `messageID` / `roomID` only; no message bodies, tokens, or env values.
- **`setupCassandra` widening to `testing.TB`** (`pkg/testutil/cassandra.go:103`): no panic-prone `t.(*testing.T)` cast.
- **DOS bounded**: callers feed at most `maxPageSize=100` IDs through `hydrateReactions`; fan-out parallelism capped at 50 (default). No amplification.
- **Input dedupe correctness**: `[:0:0]` cap=0 → `append` reallocates, so the caller's slice is not mutated (today; see L1).

---

## Performance Lens

### medium

**Read-path latency added unconditionally** — `messages.go:114,170,273,302`; `threads.go:104,208`. Every paged read now waits on a Cassandra fan-out of up to `page_size` single-partition reads before responding. For a 100-message page that is ~100 extra round-trips serialized behind the handler's reply. Hydration is parallel internally, but tail latency = slowest of N reads (N ≤ 100). Spec explicitly accepts this trade-off (`docs/specs/message-reactions-table.md` §14). Recommend p50/p99 metrics around `hydrateReactions` and `GetReactionsByMessageID` before rollout; also a `cassandra.reactions.fanout` histogram for `len(ids)` so the operator can confirm the cardinality assumption.

**Single-message path adds a sequential round-trip** — `messages.go:302-307`. `GetMessageByID` now does message-read → access-check → reactions-read. The reactions read cannot be parallelized with the message read (auth needs the message row). Acceptable; the cost is one extra Cassandra RTT per call. Worth noting in SLOs.

### low

**Page-cap × default concurrency mismatch** — `config.go:42`, `messages.go:24`. `maxPageSize=100` but default `REACTIONS_FETCH_CONCURRENCY=50`. A 100-message page serializes into two waves of 50, so worst-case latency is `2 × p99(single-partition read)`. Default is reasonable, but doc the relationship (or default concurrency to `maxPageSize`) so operators don't have to reverse-engineer it.

**Re-allocation in scan loop** — `message_reactions.go:30`. `users = nil` after every `iter.Scan` forces gocql to allocate a fresh `[]Participant` per emoji. Required for correctness (avoids aliasing) but worth a comment noting why it can't be `users = users[:0]`.

**Repeated map/mutex allocation per request** — `message_reactions.go:47,53,63-65`. Each hydration call allocates `out`, `seen`, `sem`, `mu`, and N goroutine closures. For sustained read QPS this is GC pressure that a `sync.Pool` for the result map could mitigate — but only after benchmarking shows it matters. Bench exists but cannot run locally (no Docker).

**Errgroup cancellation propagates partial work** — `message_reactions.go:69-87`. A goroutine that completes its query just before cancel still grabs `mu.Lock()` and writes to `out`, which is then discarded. Harmless but adds wasted work on error paths.

### nitpick

**Dedupe slice trick** — `message_reactions.go:54`. `[:0:0]` is clever but unobvious. Two-line comment saves the next reader 30 seconds.

**Loop-var shadow** — `message_reactions.go:67-68`. Explicit `id := id` is unnecessary on Go 1.22+ (per-iteration scope since 1.22). Codebase convention may keep it; just noting it's now dead defensiveness.

### Cannot measure here

No Docker available; `BenchmarkGetReactionsByMessageIDs` (50 messages × 5 reactions) was not run. The bench needs to run in CI with results captured before this lands under production load.

### Verified non-issues

- Goroutine lifecycle: `g.Wait()` is always called; `defer <-sem` releases the slot correctly; no leak.
- Empty-page short-circuit (`messages.go:485-487`) avoids any Cassandra contact.
- Cross-partition `IN` correctly rejected in favor of token-aware parallel reads (spec §14).
- Caching deliberately omitted — correct call for v1 given reaction churn.
- Pages with zero reactions still issue N empty round-trips; YAGNI for v1.

---

## Observability Lens

### medium — no metrics on the reaction fan-out (post-rollout blind spot)

`cassrepo/message_reactions.go:55-93` (`GetReactionsByMessageIDs`) is a new I/O-amplifying primitive. After rollout you cannot answer:
- p50/p99 latency of `GetReactionsByMessageIDs` per handler.
- Fan-out cardinality (`len(messageIDs)`) distribution — concurrency clamp at `r.reactionsConcurrency` is only effective if you can see when it's saturated.
- Error rate by reason (`hydration_errors_total{reason=…}`) — particularly `context.Canceled` from errgroup sibling-cancel vs real Cassandra failures.

Existing handler-boundary observability (the `Logging()` middleware's `duration`) captures total handler time, so a latency regression is visible *in aggregate*, but isolating the reactions hop from the page read requires either a sub-span or a histogram. Recommend at minimum a Prometheus histogram `history_reactions_fanout_duration_seconds{handler}` and a counter `history_reactions_fanout_errors_total{reason}`.

### medium — no OTel sub-span around `hydrateReactions`

`messages.go:483-502`. The parent span on `ctx` propagates to gocql via `.WithContext(gctx)`, so Cassandra calls appear in the trace as N peer spans (one per `GetReactionsByMessageID`) without a parent grouping span. When debugging a slow page in Tempo/Jaeger you can't collapse the fan-out. A single `tracer.Start(ctx, "history.hydrateReactions")` wrapper would make the fan-out a coherent subtree.

This matches the existing convention in `history-service` (no service-layer code creates spans; instrumentation is only at the NATS conn boundary via `otelnats` and at the publisher), so it's a *pre-existing* observability gap that this PR widens rather than introduces.

### low — no `requestID` field on the new error logs

The 6 new `slog.Error` calls (`messages.go:115,171,274,303`; `threads.go:104,209`) do not include `"requestID"`. This matches the established convention in `history-service/internal/service/messages.go` — none of the pre-existing handler-level error logs add a `requestID` field either. The router's `Logging()` middleware (`pkg/natsrouter/middleware.go:62-70`) emits a per-request `"nats request"` line that includes `requestID + subject + duration`, and `Recovery()` adds it on panic. Correlation in Kibana/Loki therefore requires joining on `subject` + timestamp rather than `requestID` for handler-level error logs. Branch is consistent with existing pattern; not a regression. Cross-reference Go-lens N4 about `slog.ErrorContext`.

### nitpick — log message capitalization inconsistent with neighbours

`messages.go:115,171,274` and `threads.go:104,209` use prefixed phrasing (`"load history: hydrate reactions"`). Pre-existing logs in the same file use participle form (`"loading history"`, `"edit: update content"`). The new ones mix both styles. Still greppable and `slog.Error` with key-value pairs (CLAUDE.md §3 compliant).

### Negative findings (all good)

- **No `fmt.Println` / `log.Println` / text-format loggers** in the diff.
- **No secrets / message bodies / PII logged.** All 6 sites log only `error`, `roomID`, `messageID`, `threadRoomID` — entity IDs per CLAUDE.md §3.
- **Request-ID propagation via `context.Context` is intact.** Middleware at `pkg/natsrouter/middleware.go:19-31` stores the ID on `natsutil.WithRequestID(ctx, reqID)`. `hydrateReactions` accepts the `*natsrouter.Context` (which implements `context.Context`) and forwards it. Errgroup-derived `gctx` carries the value into goroutines → `r.session.Query(...).WithContext(gctx).Iter()`.
- **slog field naming** uses `roomID`, `messageID`, `threadRoomID` — camelCase matches neighbours.
- **Empty-result path** (`messages.go:487-489`) short-circuits without a log line — correct, no noise.
- **Helper does not log on error**, only wraps with `fmt.Errorf("hydrating reactions: %w", err)` (`messages.go:498`) and lets the caller log once at the handler boundary — avoids double-logging.

### Bottom line

Diff is CLAUDE.md §3 compliant. The two `medium` items (no metrics, no sub-span on fan-out) are pre-existing project gaps that this PR widens by adding an I/O-amplifying primitive without dedicated instrumentation; both are acceptable to land in this PR but should be tracked as follow-up work before relying on the hydration path under production load.

---

## Prioritized Action List

Top items across all lenses, ordered by severity (critical → high → medium) then impact ÷ effort.

| # | Sev | Action | Where | Why |
|---|---|---|---|---|
| 1 | high | Drop the per-goroutine error wrap inside the fan-out lambda; let the leaf wrap at `:33` carry the message ID, and the outer wrap at `:91` carry the batch context. | `message_reactions.go:79` | Three-level error nesting today reads: `loading reactions for messages: loading reactions for message m1: loading reactions for message m1: <gocql err>`. Loses signal in logs. (Go-lens H1, history-lens, security-lens, perf-lens M1.) |
| 2 | high | Replace `ids := messageIDs[:0:0]` with `ids := make([]string, 0, len(messageIDs))`. Same allocation cost, no aliasing trap, intent obvious. | `message_reactions.go:48` | Cap=0 trick works today but a future refactor to `[:0]` silently mutates the caller's slice. (Go-lens H2, security-lens.) |
| 3 | medium | Add a Prometheus histogram `history_reactions_fanout_duration_seconds{handler}` and an errors counter `history_reactions_fanout_errors_total{reason}`. | new code path | Without metrics, you can't tell whether the new fan-out is the cause of any future read-latency regression. Pre-rollout investment. (Obs-lens medium, perf-lens medium.) |
| 4 | medium | Open an OTel sub-span in `hydrateReactions` (`tracer.Start(ctx, "history.hydrateReactions")`) so the fan-out collapses into one subtree in Tempo/Jaeger. | `messages.go:483-502` | The parent span propagates to gocql but the N reactions reads appear as flat peer spans. Hard to debug slow pages. (Obs-lens medium.) |
| 5 | medium | Decide: drop the singular `GetReactionsByMessageID`, OR document why it exists alongside the plural. Two methods on the interface and two mock setups per test for negligible benefit. | `message_reactions.go:18-35`, `service/service.go:26-27` | Reduces interface surface and mock duplication. If kept, the rationale (single-message hot path avoids errgroup overhead) should be in a doc comment. (Go-lens M3, history-lens.) |
| 6 | medium | Replace positional `int, int` constructor args (`maxBuckets, reactionsConcurrency`) with a config struct or functional options. | `cassrepo/repository.go:21` | 30+ test sites pass `365, 50` — silent swap risk on future refactor. (history-lens (c).) |
| 7 | medium | Split `MessageReader` into `MessageReader` + `ReactionReader` (composed in `MessageRepository`). Future side-table reads (pins, bookmarks) shouldn't widen `MessageReader` further. | `service/service.go:18-29` | Respects ISP and CLAUDE.md §3 "interfaces in the consumer". (Go-lens M2.) |
| 8 | medium | `REACTIONS_FETCH_CONCURRENCY` is per-request, not global. Document the per-request scope in the env description, OR add a global limiter / pool sentinel. | `config.go:42` | True ceiling under N concurrent NATS requests is `N × 50` Cassandra in-flight reads. Could saturate gocql pool. (history-lens (e).) |
| 9 | low | Replace `slog.Error(...)` with `slog.ErrorContext(c, ...)` at all 6 hydration error sites. | `messages.go:115,171,274,303`; `threads.go:104,209` | Allows ctx-attached attrs (requestID, traceID) to reach the log handler if the project enables that pattern. (Go-lens N4, obs-lens low.) |
| 10 | low | Add a one-sentence "Reactions are stored server-side in a side table; clients never write them" note to `docs/client-api.md` near `:949`. | `docs/client-api.md` | Explains the read-vs-write semantics for client implementers. (history-lens (f).) |

