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
