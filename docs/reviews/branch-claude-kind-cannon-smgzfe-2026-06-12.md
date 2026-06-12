# Branch Review: claude/kind-cannon-smgzfe

- **Date:** 2026-06-12
- **Base:** main
- **Services touched:** 1 — history-service (plus `docs/client-api.md`; no `pkg/` changes)
- **Diff:** 5 files, +106/−74 — `internal/service/threads.go`, `internal/models/message.go`, both test files, one doc row
- **Reviewers:** 1 per-service generalist + 5 global lenses (Go, test-automation, bug & security, performance, observability)

## Executive summary

**Finding counts (unique, deduplicated):** critical **0** · high **0** · medium **1** · low **5** · nitpick **6**

**Top-line risk: LOW.** The branch fixes a real, deterministic data-visibility bug (thread replies newer than `rooms.lastMsgAt` were excluded from `GetThreadMessages`) and removes the now-purposeless room-times dependency. All six reviewers independently verified the core fix is correct against the actual CQL and schema; `make sast` passes (gosec/govulncheck/semgrep, 0 findings); full service tests green under `-race`; package coverage 92.3% with `GetThreadMessages` at 95.0%.

The single **medium** is a documentation gap: the shared error table at `docs/client-api.md:1635` still advertises `not_found "room not found"` for all four paginated read RPCs, but this branch removes that error from Get Thread Messages. The lows are comment staleness, one untested defensive branch, and test-harness duplication. The intentional wire-behavior change (deleted room + stale subscription no longer 404s) was independently assessed by three reviewers as acceptable and is already disclosed in the PR description.

## Service: history-service

Unit tests pass (`make test SERVICE=history-service`, race detector on).

### (a) Diff correctness
- **low** — Behavior change beyond the stated fix: the old path surfaced `errcode.NotFound("room not found")` when the room doc was missing (room_times.go:27-28 via the removed `rtErr`). GetThreadMessages can no longer return room-NotFound; a stale subscription on a deleted room now proceeds to Cassandra and returns replies. The subscription gate (utils.go:14-25) covers normal cases, so this is likely acceptable, but it is an unstated wire-behavior change.
- **low** — The new inverted-range guard branch (threads.go:98-101, fires only when `accessSince > now+clockSkewTolerance`) has no test; CLAUDE.md requires boundary conditions covered. The two added tests (threads_test.go:316-341, 347-377) are well-targeted otherwise — the first directly asserts the regression (`gotCeiling.After(recentReplyAt)`), the second uses a strict room mock to lock out the dependency.
- Floor change is sound: dropping `createdAt` from the floor is correct (no reply predates its room) and only loosens a clustering slice on a single partition; the comment at threads.go:85-91 justifies it accurately.

### (b) Scope drift / refactor-readiness
No findings — the diff touches only the thread request model, the one handler, its tests, and one doc row. The goroutine fan-out removal is exactly the scope implied by removing the Mongo read.

### (c) Abstraction changes
- `resolveRoomTimesOrError` is **not** orphaned — still used by LoadHistory, LoadNextMessages, LoadSurroundingMessages (messages.go:40, 124, 184). `RoomMeta`, `walkBounds`, and `clockSkewTolerance` all retain other consumers. No dead code left behind; the `sync` import was correctly dropped.
- **nitpick** — `clockSkewTolerance`'s doc comment (room_times.go:33-36) still describes it purely as a meta-hint sanitizer ("triggers a Mongo fallback"); threads.go:93 now reuses it as the future-row ceiling guard, so the comment is slightly stale.

### (d) Design coherence
No findings. The removal matches the service's data model: `thread_messages_by_thread` is one partition per thread with no bucket walk (CLAUDE.md Cassandra section), so room watermarks had no legitimate role; the lastMsgAt ceiling was actively wrong since broadcast-worker never bumps it for thread fan-out. The simplification makes the handler match its siblings' shape (access gate → find → bounds → read).

### (e) Project-pattern adherence
- Tier-1 errcode usage intact (`BadRequest`/`Forbidden` with reasons; raw `fmt.Errorf("loading thread messages: %w", err)` at threads.go:105 for infra). No log-and-return double-logging introduced. Subject registration unchanged via `pkg/subject` (service.go:154).
- **nitpick** — Tests live in external package `service_test` (threads_test.go:1), contra CLAUDE.md's same-package rule; pre-existing convention in this service, not introduced by the diff, and the new tests rightly follow the file's existing style.

### (f) Client-API doc rule
`docs/client-api.md` **is** modified in the same diff (line 1614) and is accurate: the per-RPC request table (lines 2578-2582) never listed `meta`, so the common-fields row was the only place needing the carve-out, and "ignored if sent" matches verified behavior (`TestGetThreadMessagesRequest_IgnoresLegacyMetaField`, message_test.go).
- **nitpick** — The intro sentence at line 1609 still says Get Thread Messages accepts "these shared optional fields" while the `meta` row immediately excludes it; the row disambiguates, but tightening the intro would read cleaner.

No critical or high findings.

## Go expert

**Verdict: clean, well-executed change.** The diff removes the room-times (`meta`) dependency from `GetThreadMessages`, replacing the `lastMsgAt`-derived ceiling with a server-clock ceiling — fixing a real bug (thread replies never bump `rooms.lastMsgAt`, so the old ceiling hid fresh replies). Findings below are minor.

### Findings

**[low] Stale comment referencing removed behavior** — `history-service/internal/service/messages_test.go:60-61`: the `newServiceWithRoomMock` doc comment says "every handler invokes the bucket-walk resolver, and almost no test cares about its return." After this change, `GetThreadMessages` no longer does — the comment over-claims and the permissive `GetRoomTimes` stub is now dead weight for the ~20 thread tests using `newService`. Worth a one-word fix ("every paginated-read handler except Get Thread Messages…").

**[low] Duplicated service wiring in the new strict-mock test** — `history-service/internal/service/threads_test.go:352-373`: `TestHistoryService_GetThreadMessages_NoRoomTimesDependency` hand-rolls all 7 mocks plus the `config.Config` literal (`90/500/10/true`) because `newServiceWithRoomMock` pre-stubs `GetRoomTimes` with `MinTimes(0)` (which wouldn't fail if called — the duplication is *necessary* for strictness, so this is justified). But the config defaults now live in two places and can drift; a `newServiceStrictRooms(t)` helper or extracting the config literal would prevent that.

**[nitpick] Docs intro contradicts the carve-out** — `docs/client-api.md:1609` still says "The paginated read RPCs (Load History, Load Next, Load Surrounding, Get Thread Messages) accept these shared optional fields", while the new `meta` row at line 1614 excludes Get Thread Messages. The row wins on a careful read, but the intro sentence now over-promises. The per-RPC request table (line 2578-2582) correctly omits `meta`.

**[nitpick] Lost `NotFound("room not found")` path** — removing `resolveRoomTimesOrError` from this handler drops the `mongo.ErrNoDocuments → 404` translation. In practice no regression: `getAccessSince` (`internal/service/utils.go:14-25`) fires first and returns `Forbidden(MessageNotSubscribed)` for nonexistent rooms, and the parent lookup 404s on a missing message. Noting only so it's a conscious choice.

### Verified clean (checked, no issue)

- **No dangling code**: `sync` import removed with the goroutine fan-out; `resolveRoomTimesOrError` still has 3 live callers (`messages.go:40,124,184`); `walkBounds` and `clockSkewTolerance` (`room_times.go`) still used. No orphaned helpers.
- **No stale `Meta` references**: `tools/loadgen/history_generator.go:115-119` mirrors the request and never had `meta`; no `req.Meta` reads remain for this type; cassrepo comments don't reference the old thread ceiling. Remaining hits are historical `docs/superpowers/` plans (intentionally frozen).
- **Comment accuracy**: the load-bearing claim in `threads.go:85-91` ("broadcast-worker skips lastMsgAt for thread fan-out") is true — `broadcast-worker/handler.go:236` says exactly that.
- **Bounds logic**: dropping the `floor.IsZero()`/`historyFloor` clamp is correct — `floor` now starts at `now-historyFloor` (never zero) and only moves *forward* via `accessSince`; the inverted-range guard comment (`threads.go:98`) accurately describes the only remaining trigger.
- **Section 3 compliance**: error wrapping describes the current operation, `errcode` constructors at the boundary, `time.Now().UTC()`, camelCase `json` tags, no log-and-return.
- **Wire compatibility**: server-side struct-field removal is non-breaking (unknown JSON fields ignored), pinned by `TestGetThreadMessagesRequest_IgnoresLegacyMetaField`. `docs/client-api.md` updated in the same PR per Section 5.
- **Tests pass**: `make test SERVICE=history-service` green with `-race`. The regression test asserts the actual queried bounds via `DoAndReturn` capture — exactly the right shape for this bug.

## Test-automation

### Verification results

- **Tests**: `go test -race ./history-service/...` — all PASS.
- **Coverage**: `history-service/internal/service` total **92.3%** (above the 80% floor, meets the 90% target for handlers). `GetThreadMessages` 95.0%, `GetThreadParentMessages` 93.6%, `emptyThreadResponse`/`validateThreadFilter` 100%.
- **Mock staleness**: the diff touches no `store.go` or service-layer repository interfaces — `make generate` not needed; tree stayed clean.
- **Test deletions**: `threads_test.go` is purely additive (0 removed lines). The only removed test is `TestGetThreadMessagesRequest_WithMeta_Roundtrip` (models/message_test.go:148), replaced by two tests that together cover more (round-trip + legacy-payload decode).

### Findings

**TDD heuristic — all three behavior changes have matching tests in the same diff (pass)**
1. Ceiling no longer lastMsgAt-derived → `TestHistoryService_GetThreadMessages_CeilingIncludesFreshReplies` (threads_test.go:316) asserts via `DoAndReturn` capture that the ceiling passed to the repo is after a reply created 1 minute ago — this would fail under the old watermark-derived ceiling, so it's a genuine regression test, not a tautology.
2. No room-times dependency → `TestHistoryService_GetThreadMessages_NoRoomTimesDependency` (threads_test.go:352) wires strict mocks bypassing `newService`'s permissive `GetRoomTimes(...)` default (messages_test.go:52), so any regression to room-times reads fails the mock controller. Correctly constructed.
3. `Meta` field removed → `TestGetThreadMessagesRequest_IgnoresLegacyMetaField` (message_test.go:159) verifies legacy payloads carrying `meta` still decode, matching the docs "ignored if sent" claim. Good wire-compat coverage.

**Existing error/edge coverage intact (pass)** — accessSince clipping (threads_test.go:145, 297, floor assertions at 344-345), invalid cursor (:223), repo error (:239), tcount==0 short-circuit (:174) and tcount==nil fall-through (:193), empty ThreadRoomID (:209), not-subscribed/sub-store-error (:123, :134), reply-ID 400 (:283), limits table (:253). Nothing weakened.

**low — inverted-range guard untested.** threads.go:99-101 (`if ceiling.Before(floor) { ceiling = floor }`) — now reachable only when `accessSince > now+clockSkewTolerance`. It's the uncovered statement keeping `GetThreadMessages` at 95%. A one-case test with a far-future `accessSince` asserting `gotCeiling.Equal(gotFloor)` would close it.

**nitpick — strict-mock wiring duplicated inline.** threads_test.go:353-367 hand-builds the full `service.New(...)` call with all 8 deps and a config literal; extracting a `newServiceStrictRooms(t)` helper next to `newService`/`newServiceWithRoomMock` would keep wiring in one place. Not blocking.

**nitpick — comment drift.** threads_test.go:172-173 says "TCount == 0 means the parent has never received a reply" while the implementation comment (threads.go:75) says it means "all replies have been deleted". Cosmetic inconsistency only.

**Structure** — table-driven used where variation exists (`_Limits`); single-scenario tests appropriately not tabled. Test names descriptive. No shared mutable state; test independence holds.

**Overall: approve from a test-automation standpoint.**

## Bug & security

Verified the new bounds in `threads.go:85-101` against the actual CQL in `cassrepo/thread_messages.go:29` (`created_at < ? AND created_at >= ?`), the access gates in `utils.go`, and the removed room-times path. Tests pass (`go test -race`, all green); **`make sast` passes: gosec=PASS, govulncheck=PASS, semgrep=PASS, 0 findings** — no SAST issues introduced (blocking CI gate satisfied).

### Findings

**low — Room-existence NotFound silently dropped** (`threads.go:38-41` vs removed `resolveRoomTimesOrError`, `room_times.go:27-29`). Previously a subscribed caller with a valid parent in a room whose Mongo doc is missing got `NotFound("room not found")`; now the read succeeds. Access is still correctly gated: `getAccessSince` (utils.go:14-25) returns Forbidden for non-subscribers before anything else, and `findMessage` (utils.go:55-57) rejects a parent whose `msg.RoomID != roomID` with NotFound — so cross-room reads remain blocked, and `ThreadRoomID` is only ever taken from that verified parent. No room-delete flow exists in room-service, so the orphaned-room state is theoretical. Acceptable; disclosed in the PR description.

**low — Inverted-range guard is now effectively dead** (`threads.go:98-101`). `ceiling = now+1h < floor` requires `accessSince > now+1h`; but any past-dated parent then fails `msg.CreatedAt.Before(*accessSince)` at threads.go:47 and returns Forbidden first. The guard only fires for a parent itself future-dated beyond skew. Harmless defensive code; the comment slightly overstates reachability.

**nitpick — Moving floor across pagination pages** (`threads.go:92-94`). `floor = now - historyFloor` is recomputed per page while the cursor is a gocql PageState resumed against fresh bind values. No duplicates/skips in the DESC direction; the only effect is the floor creeping forward by inter-page latency, which can clip rows sitting exactly at the historyFloor boundary mid-pagination. Pre-existing pattern in the bucket-walk handlers too; not a regression.

### Verified-sound (no findings)

- **Boundary inclusivity:** a reply created exactly at `accessSince` is included (`created_at >= floor`), consistent with the parent gate. A reply at exactly `ceiling` (`now+1h`) is excluded by `<` — irrelevant in practice. Zero-time handling is gone by construction: both bounds derive from `now`; `accessSince` nil means full access and correctly leaves `floor = now - historyFloor`.
- **Dropped `createdAt` floor:** cannot change results — no reply predates its room; the historyFloor clamp is preserved (parity with `walkBounds`).
- **Core bug fix is correct:** the old `lastMsgAt+1ms` ceiling did hide fresh replies since thread fan-out never bumps `rooms.lastMsgAt`; the regression test pins the new behavior.
- **Meta removal:** `pkg/natsrouter/register.go:21,68` uses plain `json.Unmarshal` — no `DisallowUnknownFields` — so legacy payloads carrying `meta` decode cleanly (covered by test). No remaining Go references to the removed field; chat-frontend's `fetchThreadMessages` never sent `meta`; `docs/client-api.md` updated in-PR per the client-facing-handler rule.
- **Concurrency:** the `sync.WaitGroup` fan-out is fully removed, calls now sequential — no shared-state issues remain; error precedence (validation 400s before infra errors) preserved trivially by ordering.

No critical/high/medium findings. The change is sound.
