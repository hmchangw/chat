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

## Service: room-service
**Verdict:** well-scoped, pattern-compliant, no scope drift; stays within the service's member.list assembly responsibility.

- **(a) Correctness vs conventions** — new fields use the `bson:"-"` display convention; projections extended precisely at every query site; `AccountName` derived Go-side rather than in Mongo. Both paths updated symmetrically. No issues.
- **(b) Scope / refactor** — no drift; `orgdisplay.go` extended, not repurposed.
- **(c) Abstraction** — `[medium]` `orgDisplayDescription` (orgdisplay.go:118) reuses the dept-first rollup, but its dept guard (`deptDescription != ""`) differs from `orgDisplaySectName`'s (`deptName != "" || deptTCName != ""`). Correct per the "no orgID fallback" rule and covered by tests; a readability trap — now documented.
- **(d) Design coherence** — coherent; all four fields display-only; bots correctly get `AccountName` but empty `SectName`/`EmployeeID`.
- **(e) Patterns** — stream/subject/idgen N/A (request/reply enrichment); MongoDB projections precise; no new `$lookup` (the existing one only gains two projected fields). Compliant.
- **(f) Client-API doc rule** — SATISFIED: `docs/client-api.md` updated in-branch (field table + JSON example).
- `[nitpick]` deleted `// NO room_members docs inserted` test note — FIXED.

## Go Expert
- `[medium → false positive]` User struct tag alignment (user.go:18-25) — flagged inconsistent, but `gofmt -l` is clean: the alignment IS gofmt-canonical (golangci-lint fmt produced it; lint 0 issues). No action.
- `[medium → already addressed]` duplicate `strings.ToUpper(acct)` at store_mongo.go:571 and :822 — two sites is below the extraction threshold (no helper warranted); both already carry clarifying comments (added in /simplify). No action.
- `[low]` else-after-org-branch guard style (store_mongo.go:565) — cosmetic; current is correct.
- `[low → addressed]` description lex-max undocumented — documented in commit 12b5fb3.
- `[nitpick]` key-order / trailing spaces — gofmt-canonical; no action.

**Summary:** `bson:"-"`+`omitempty` matches existing neighbors; error wrapping clean; no premature abstraction. No critical/high.
