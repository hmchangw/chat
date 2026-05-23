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
