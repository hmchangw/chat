# Branch Review: claude/eager-curie-b7m3qz — subscription.list pagination

- **Date:** 2026-06-11
- **Branch:** `claude/eager-curie-b7m3qz`
- **Diff base:** `28ef362` (branch point off `claude/cool-davinci-6lYhU`; per request, only this session's 9 commits are in scope — the cool-davinci work underneath is awaiting its own merge and is NOT re-reviewed here)
- **Services touched:** 1 — `user-service` (+ shared `pkg/mongoutil`, `docs/client-api.md`)
- **Review agents:** 6 (1 per-service generalist + 5 global lenses), run in parallel

## Executive summary

The branch adds offset/limit pagination (default 40, cap `MAX_SUBSCRIPTION_LIMIT`) to the
`subscription.list` RPC across all three `type` views, moving favorite filtering and
self-DM-first ordering from in-memory Go into the Mongo pipeline, and changing `total` to
the full filtered count. Unit, integration, lint, and gosec gates are green; coverage on
the touched handler package is 96.2%.

**No critical findings.** The single high finding is a wire-type width inconsistency
(`Total` is `int` while the plumbing is `int64`). Mediums are split between two accepted
design trade-offs (full-set join before sort; `$count` walk per page — both bounded by the
~1000-sub per-account cap), one pre-existing input-validation gap worth fixing while we're
here (`updatedWithinDays` has no upper bound), and style/robustness items. Two lens claims
were fact-checked and **corrected during synthesis** (marked inline): the "unused
constructor" claim and the "don't commit spec/plan files" claim are both wrong per repo
state/convention.

**Finding counts (deduplicated across lenses):**

| Severity | Count |
|----------|-------|
| critical | 0 |
| high     | 1 |
| medium   | 6 |
| low      | 7 |
| nitpick  | 3 |

**Top-line risk assessment:** LOW. The change is well-tested (TDD heuristic passes, mocks
fresh, page math pinned by integration tests against real Mongo), behavior-preserving for
the favorite/self-DM semantics, and documented in `docs/client-api.md`. The known
consumer-visible effect — the frontend sidebar truncating to 40 items per bucket until it
adopts paging — is a deliberate, spec-documented follow-up, not a defect.
