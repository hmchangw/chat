# Branch Review — claude/upbeat-galileo-hzn9cc

**Date:** 2026-06-26
**Base:** fork point `630aea6` (shallow clone; `main` shares no merge-base, so the review diff is `630aea6..HEAD`)
**Services touched (1):** room-service
**Shared packages:** pkg/model (User, RoomMemberEntry)

## Feature
Four enrich-only display fields on the `member.list` RPC: `orgDescription` (org rows) and `sectName`/`accountName`/`employeeId` (individual rows), plumbed through pkg/model, the two room-service enrichment paths (room_members aggregation + subscriptions fallback), and the org rollup (`orgdisplay.go`). `accountName = strings.ToUpper(account)`.

## Findings by severity
| Severity | Count |
|---|---|
| critical | 0 |
| high | 0 |
| medium | 3 |
| low | 5 |
| nitpick | 5 |

## Top-line risk: LOW
No correctness or security defects. gosec passes (govulncheck/semgrep blocked by proxy — they run in CI); lint 0 issues; unit tests green; integration tests compile (Docker unavailable locally — executed in CI). Reviewers confirmed correct BSON field alignment across all projections, no new DB round-trips/N+1, and `docs/client-api.md` updated in-branch. Two cosmetic items were fixed during the review (restored a dropped test comment; documented the description lex-max) — commit 12b5fb3.
