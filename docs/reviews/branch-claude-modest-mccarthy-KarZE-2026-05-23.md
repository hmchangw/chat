# Branch Review — `claude/modest-mccarthy-KarZE`

**Date:** 2026-05-23
**Base:** `origin/main` (`3cd3ab8`)
**Branch HEAD:** `79ee428`
**Diff size:** 29 commits, ~36 files (rebased onto origin/main)

## Executive summary

The branch implements the **message reactions side-table** feature per the spec at `docs/specs/message-reactions-table.md`: removes the embedded `reactions MAP<…>` column from 4 Cassandra message tables, creates a dedicated `chat.message_reactions((message_id), emoji)` side table with LCS compaction, adds two new readers (`GetReactionsByMessageID`, `GetReactionsByMessageIDs`) on the cassrepo, wires a `hydrateReactions` service helper into all 6 history-service handlers that return messages, and updates the schema/client-API docs.

Six expert lenses ran in parallel against the rebased branch.

### Findings by severity *(pending test-automation lens)*

| Severity | history-service | Go | Tests | Bug/Sec | Perf | Obs | **Total** |
|---|---|---|---|---|---|---|---|
| critical | 0 | 0 | _pending_ | 0 | 0 | 0 | **0** |
| high | 1 | 2 | _pending_ | 0 | 0 | 0 | **3+** |
| medium | 4 | 4 | _pending_ | 0 | 2 | 2 | **12+** |
| low | 4 | 4 | _pending_ | 1 | 3 | 1 | **13+** |
| nitpick | 1 | 6 | _pending_ | 3 | 2 | 1 | **13+** |

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

