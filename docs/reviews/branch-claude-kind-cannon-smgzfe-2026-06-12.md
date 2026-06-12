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
