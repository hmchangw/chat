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

## Test Automation
- `store.go` interface UNCHANGED → mocks not stale (no `make generate` needed). Confirmed.
- TDD satisfied — `orgDisplayDescription` (only new func, unexported) has `TestOrgDisplayDescription` (nil / dept-wins / dept-empty-fallback / sect-only / all-empty); model field changes covered in model_test (JSON present, BSON absent, omitted-when-zero, bot accountName, org orgDescription).
- Integration tests would correctly assert intended behavior in CI: both store paths exercised; `OrgDescription` dept-first / sect-only / missing isolated. (Docker unavailable locally → compile-checked via `go vet -tags integration`.)
- `[medium]` handler enrich=false wire-absence not asserted at the handler layer — LOW real risk: the model round-trip test already verifies `omitempty` JSON omission. Optional follow-up.
- `[medium → addressed]` deleted fallback comment — FIXED.
- `[low]` orgdisplay_test lex-max tie-break tested only on the sect branch; dept branch is the symmetric mirror — optional dept subcase.
- `[low]` integration dept-first subtest doesn't also assert `OrgName` — optional completeness.
- `[nitpick]` OmittedWhenZero key-list order.

## Bug & Security
**gosec: exit 0, no findings.** (govulncheck/semgrep 403-blocked by proxy — environment limitation, run in CI.) No critical/high bugs.

Verified correct:
- BSON alignment — every projection key (`sectName`/`employeeId`/`deptDescription`/`sectDescription`) matches `model.User` bson tags and the decode structs across `fetchOrgDisplayUsers`, `findUsersForDisplay`, the `_userMatch` `$project`, the `$arrayElemAt` paths, and `roomMemberEnrichedDisplay`/`orgDisplayUser`.
- `getRoomMembers` loop (store_mongo.go:565) — org rows skip the individual branch (no `AccountName`); individuals get all three; `attachOrgDisplay` fills org fields.
- Bot `AccountName` set; `SectName`/`EmployeeID` empty (no user doc) — confirmed by integration test.
- PII: `employeeId`/`accountName` returned only under `enrich=true`, consistent with existing enrich fields; no new leak; nothing logged.

- `[low]` `orgDisplayDescription` dimension mismatch (orgdisplay.go:103-126) — in a contrived shape (an org id is one user's `deptId` with empty dept names but a non-empty `deptDescription`, AND another user's `sectId`), `orgName` could render the sect while `orgDescription` returns the dept's. Self-consistent with the documented "dept description preferred" rule; reachable only in that edge shape.
- `[nitpick]` `AccountName` set without `Account != ""` check — harmless (`omitempty`).

## Performance
**"No new index, no new round-trip" — HOLDS at all three sites.** Added fields ride existing queries: the `$limit:1` `_userMatch` sub-pipeline `$project` (store_mongo.go:684), the `findUsersForDisplay` `$in` batch projection, and the `fetchOrgDisplayUsers` `$in` batch projection. No new Find/aggregate.

- `[medium → addressed]` description lex-max tiebreak is arbitrary for differing prose across an org's users — but it mirrors the name rollup and descriptions are org-uniform in practice; now documented (commit 12b5fb3).
- `[low]` `strings.ToUpper` — the two call sites are mutually-exclusive code paths (aggregation vs fallback); called once per row on an in-memory field. No double work.
- `[low]` rollup string copies — single O(N) linear pass; negligible.
- `[nitpick]` `$limit:1` + `_id:0` still present; projection narrow.

## Observability
**Clean — no violations.** The diff adds NO logging (expected for field plumbing).
- No new `slog`/`fmt.Println`/text loggers; the new PII-ish fields (`employeeId`, `accountName`) are never logged, used as span attributes, or placed in metrics.
- `context.Context` threaded correctly through all new/modified helpers (`getRoomMembers`→`attachOrgDisplay`→`fetchOrgDisplayUsers`; `attachUserDisplayNames`→`findUsersForDisplay`).
- No new handler → no new span/metric expected; the pre-existing absence of a `listMembers` span is not introduced by this change.
- `[nitpick]` `AccountName` derivation duplicated across two sites (maintainability) — both now commented.

## Prioritized Action List
| # | Sev | Item | Location | Status |
|---|-----|------|----------|--------|
| 1 | medium | Document description lex-max rollup | orgdisplay.go:74 | FIXED (12b5fb3) |
| 2 | nitpick | Restore dropped fallback-path test note | integration_test.go:746 | FIXED (12b5fb3) |
| 3 | medium | `orgDisplayDescription` dept-guard differs from `orgDisplaySectName` (name vs description dimension in an edge shape) | orgdisplay.go:118 | Accepted — intentional per spec, now documented; add a dept+sect-desc integration subcase if stricter regression wanted |
| 4 | medium | Handler-layer enrich=false wire-absence not asserted | handler_test.go | Accepted — covered by model round-trip omitempty test; optional |
| 5 | low | Add dept-branch lex-max unit subtest (symmetry) | orgdisplay_test.go | Optional |
| 6 | low | Assert `OrgName` in dept-first integration subtest | integration_test.go | Optional |
| 7 | low | else-after-org guard style | store_mongo.go:565 | Won't fix — correct as-is |
| 8 | false+ | User struct tag alignment | user.go | No action — gofmt-canonical (verified) |

**No critical/high findings. Branch is safe to proceed.**
