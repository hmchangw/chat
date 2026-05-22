# Branch Review — `claude/modest-mccarthy-KarZE`

**Date:** 2026-05-22
**Base:** `origin/main`
**Mode:** spec-review (no Go/pkg code changes; only a new spec document at `docs/specs/message-reactions-table.md`)
**Reviewed artifact:** `docs/specs/message-reactions-table.md`

## Executive summary

This branch adds a single spec document proposing the extraction of message reactions from an embedded `MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>` column on four message tables into a dedicated side table `message_reactions` keyed by `((message_id), emoji)`. Hydration moves to read-time in `history-service` via errgroup parallel single-partition queries. Writes are out of scope (deferred to a future addReaction PR).

The `branch_review` skill's standard precondition (require Go/pkg code changes) was bypassed because the user explicitly requested a spec review. Five expert lenses ran in parallel against the spec text and supporting context (CLAUDE.md, existing schema, existing cassrepo code, existing client-api doc).

### Findings by severity

| Severity | Cassandra | Go | Conventions | Tests | Scope/Risk | **Total** |
|---|---|---|---|---|---|---|
| critical | 2 | 3 | 1 | 2 | 1 | **9** |
| high | 3 | 4 | 4 | 4 | 3 | **18** |
| medium | 4 | 4 | 4 | 3 | 4 | **19** |
| low | 3 | 3 | 5 | 2 | 3 | **16** |
| nitpick | 3 | 3 | 3 | 2 | 3 | **14** |

### Top-line risk assessment

The spec is **directionally correct but ships with one ship-blocker** and several "must resolve before the implementer starts coding" items:

- **Ship-blocker:** the spec's stated motivation in §14 (tombstone elimination by moving off the embedded `MAP`) is partly wrong — the side table relocates the tombstone problem but does not eliminate it. The motivation paragraph must be rewritten, or the design pivoted to row-per-reactor (`PRIMARY KEY ((message_id), emoji, user_id)`), which the spec doesn't evaluate.
- **High-priority decisions deferred but blocking:** compaction strategy (LCS recommended), empty-set delete race, federation note, and the SET-must-be-unfrozen confirmation. All four are schema-shape decisions that the addReaction PR cannot defer.
- **Process gap:** spec claims no `docs/client-api.md` update is needed because the wire shape is unchanged, but CLAUDE.md §5 is a hard rule with no "wire shape unchanged" carve-out. Either update the doc with a one-line clarification or get an explicit exemption written into CLAUDE.md.
- **Test plan gaps:** missing `-race` parallelism assertions, ctx-cancellation test, and explicit `testutil.CassandraKeyspace` contract for the new integration test file.

The implementation itself is small (~1–2 days for one engineer) once the spec is corrected. None of the findings argue against the side-table direction — they argue for tightening the spec text before implementation begins.
