# `listRoomMembers` — Extra Enrichment Fields (orgDescription + individual sect/account/employee)

**Date:** 2026-06-25
**Status:** Draft
**Service:** `room-service` (+ `pkg/model`)

## Summary

Add four new display fields to the `member.list` RPC response (`chat.user.{account}.request.room.{roomID}.{siteID}.member.list`), all populated **only when `enrich=true`**, mirroring the existing display-field contract (`bson:"-"` + `json:",omitempty"`, never persisted, elided when empty):

- **Org rows** (`type:"org"`) gain `orgDescription`.
- **Individual rows** (`type:"individual"`) gain `sectName`, `accountName` (the account in capital letters), and `employeeId`.

No change to the response envelope, the two-path lookup, ordering, or the lean (`enrich=false`) record. The four fields slot into the existing enrichment machinery.

## Requirements

| Field | Applies to | Value | Source |
|---|---|---|---|
| `orgDescription` | org | The org's description, dept-first (deptDescription preferred, sectDescription fallback), omitted when empty | new User-doc fields (below) |
| `sectName` | individual | The member's own section name | `user.SectName` |
| `accountName` | individual | The account in capitals, e.g. `alice` → `ALICE` | `strings.ToUpper(member.account)` |
| `employeeId` | individual | The member's employee ID | `user.EmployeeID` |

All four are enrich-only. The lean record (`enrich=false`) is unchanged.

## Data sourcing decision

`orgDescription` has no existing source — orgs are not a stored entity; they are derived from each user's `sectId`/`deptId`, and no description field exists anywhere today. Decision (confirmed with the requester): **denormalize the description onto the User doc**, populated by the same external HR/directory sync that already owns `users` and populates `SectName`/`DeptName`/`EmployeeID`. No new collection, no `$lookup`, no new index.

Because an org row's `member.id` may be either a `sectId` or a `deptId` (dept-aware adds, per the 2026-05-19 org→individual upgrade spec), the description must exist on both axes (`SectDescription`, `DeptDescription`) and be resolved dept-first — exactly the tiebreak already used for `orgName` in `orgdisplay.go`.

The individual fields (`sectName`, `employeeId`) already live on `User`; `accountName` is a pure Go transform of the already-present `member.account` and needs no datastore read.

## Data models

### `pkg/model/user.go`

Add two fields, populated externally (no repo code writes `users`), matching the plain-tag style of the surrounding `Sect*`/`Dept*` fields:

```go
SectDescription string `json:"sectDescription" bson:"sectDescription"`
DeptDescription string `json:"deptDescription" bson:"deptDescription"`
```

Existing user docs that pre-date the sync extension carry empty descriptions → `orgDescription` is simply omitted from the wire (same graceful degradation as a dept-only user with no sect names today).

### `pkg/model/member.go` — `RoomMemberEntry`

Add four display fields with the established `bson:"-"` + `json:",omitempty"` contract:

```go
// Populated when type="individual" (enrich=true):
SectName    string `json:"sectName,omitempty"    bson:"-"`
AccountName string `json:"accountName,omitempty" bson:"-"` // strings.ToUpper(account)
EmployeeID  string `json:"employeeId,omitempty"  bson:"-"`
// Populated when type="org" (enrich=true):
OrgDescription string `json:"orgDescription,omitempty" bson:"-"`
```

JSON keys (`sectName`, `accountName`, `employeeId`, `orgDescription`) do not collide with the existing `orgName`. `bson:"-"` keeps them out of `room_members` persistence; `omitempty` elides them on the wire when `enrich=false` or unset.

## Store layer — enrichment

Individuals appear in **both** store paths (`getRoomMembers` reads `room_members`; `getRoomSubscriptions` is the fallback when a room has no `room_members` docs), so the new individual fields must be wired into both. Orgs only ever appear in the `room_members` path.

### A. Individuals — `room_members` path (`getRoomMembers` + `enrichRoomMembersStages`)

1. **`enrichRoomMembersStages`** — extend the `_userMatch` `$lookup` inner `$project` to also emit `sectName` and `employeeId` (today it emits `engName`, `chineseName`).
2. **`$set display`** — fold `sectName` and `employeeId` into the parallel `display` sub-doc alongside the existing `engName`/`chineseName`.
3. **`roomMemberEnrichedDisplay`** decode struct — add `SectName string bson:"sectName,omitempty"` and `EmployeeID string bson:"employeeId,omitempty"`.
4. **Go decode loop** (`getRoomMembers`) — copy `d.SectName`/`d.EmployeeID` onto the entry, and set `rm.Member.AccountName = strings.ToUpper(rm.Member.Account)`.

`engName`/`chineseName` continue to come only from the user join; bots (no user doc) keep empty name/sect/employee fields in this path, as today. `accountName` is set from `member.account` regardless.

### B. Individuals — subscriptions fallback (`getRoomSubscriptions` + `attachUserDisplayNames`)

1. **`findUsersForDisplay`** — add `sectName` and `employeeId` to the projection (the `users.account` index still covers the `$in`).
2. **`attachUserDisplayNames`** — in the human-account copy branch, set `SectName = u.SectName` and `EmployeeID = u.EmployeeID`.
3. Set `AccountName = strings.ToUpper(members[i].Member.Account)` for every individual row (humans and bots).

Bot accounts (`.bot`) resolve via `apps` (Name only), so they keep empty `sectName`/`employeeId`; their `accountName` is the uppercased bot account.

### C. Orgs — `orgdisplay.go` rollup (`fetchOrgDisplayUsers` + `attachOrgDisplay`)

`orgDescription` resolution mirrors `orgName` (`orgDisplaySectName`) exactly, except the empty-fallback is `""` rather than the orgID — a missing description should be omitted, not replaced by an id.

1. **`orgDisplayUser`** — add `DeptDescription bson:"deptDescription"` and `SectDescription bson:"sectDescription"`.
2. **`fetchOrgDisplayUsers`** projection — add `deptDescription: 1`, `sectDescription: 1`.
3. **`orgDisplayAgg`** — add `deptDescription`, `sectDescription` accumulators.
4. **`buildOrgDisplay`** — accumulate descriptions per branch with the same lexicographic-max rule as the name fields (dept branch sets `deptDescription`; sect branch sets `sectDescription`), preserving the existing dept-precedence / no-double-count semantics.
5. **`orgDisplayDescription(a *orgDisplayAgg) string`** — new helper: dept-first (`deptDescription` when `isDept` and non-empty), else `sectDescription`, else `""`. (Unlike `orgDisplaySectName`, no orgID fallback.)
6. **`attachOrgDisplay`** — set `members[i].Member.OrgDescription = orgDisplayDescription(agg[id])` for org rows.

`strings` is added to the `store_mongo.go` import set for the two `ToUpper` calls.

## Wire / client API

`docs/client-api.md` §List Members → `RoomMemberEntry` table and the JSON example:

- Add `sectName` (string, individual, enrich-only), `accountName` (string, individual, enrich-only — account in capitals), `employeeId` (string, individual, enrich-only).
- Add `orgDescription` (string, org, enrich-only).
- Update the success-response JSON example to show the new fields on an enriched individual and (a new) enriched org row.

This handler's subject begins with `chat.user.`, so the doc update ships in the same PR (CLAUDE.md §5).

## Testing (TDD — red first)

- **`pkg/model/model_test.go`** — extend the `RoomMemberEntry` round-trip: new fields marshal to JSON when set, are **absent** from BSON output (`bson:"-"`), and are omitted from JSON when empty.
- **`room-service/orgdisplay_test.go`** — `orgDisplayDescription` cases: dept-first wins; sect fallback when no dept match (or empty dept description); both-empty → `""`; nil aggregate → `""`. Extend `buildOrgDisplay` cases to assert descriptions roll up alongside names.
- **`room-service/handler_test.go`** — the enrich-passthrough case asserts the four new fields surface on the response (individual + org) when the mocked store returns them, and are absent on the `enrich=false` case.
- **`room-service/integration_test.go`** — extend the enriched cases:
  - individual via `room_members` path → `sectName`/`accountName`/`employeeId` populated from the seeded user;
  - individual via subscriptions fallback → same three fields populated;
  - org via `room_members` path → `orgDescription` resolved dept-first from seeded users;
  - `accountName` equals the uppercased account; `enrich=false` yields none of the four.

Coverage stays at/above the CLAUDE.md floor (80% min, 90%+ for the touched store/handler logic).

## Files changed

| File | Change |
|------|--------|
| `pkg/model/user.go` | Add `SectDescription`, `DeptDescription` |
| `pkg/model/member.go` | Add `SectName`, `AccountName`, `EmployeeID`, `OrgDescription` to `RoomMemberEntry` (`bson:"-"`, `json:",omitempty"`) |
| `pkg/model/model_test.go` | Round-trip cases for the new fields (JSON present / BSON absent / empty-omitted) |
| `room-service/store_mongo.go` | `enrichRoomMembersStages` projection + `$set display`; `roomMemberEnrichedDisplay` struct; `getRoomMembers` decode loop (`AccountName`, `SectName`, `EmployeeID`); `findUsersForDisplay` projection; `attachUserDisplayNames` copy; `fetchOrgDisplayUsers` projection; add `strings` import |
| `room-service/orgdisplay.go` | `orgDisplayUser` + `orgDisplayAgg` description fields; `buildOrgDisplay` rollup; new `orgDisplayDescription`; `attachOrgDisplay` sets `OrgDescription` |
| `room-service/orgdisplay_test.go` | `orgDisplayDescription` + `buildOrgDisplay` description cases |
| `room-service/handler_test.go` | Enrich-passthrough assertions for the four fields |
| `room-service/integration_test.go` | Enriched individual (both paths) + org cases |
| `docs/client-api.md` | §List Members `RoomMemberEntry` table + JSON example |

No `store.go` interface change (the `ListRoomMembers` signature is unchanged), so no mock regeneration is required.

## Performance & indexes

- **No new index.** Descriptions are never a query key — they are projected fields on queries already covered by `users.account` (fallback path) and `(sectId, account)` / `(deptId, account)` (org rollup). The projections grow by two fields; the filters are unchanged.
- **No new round-trips.** Individual extras ride the existing user join/batch; `accountName` is a Go transform; org descriptions ride the existing `fetchOrgDisplayUsers` batch. The member-list hot path keeps its current query count.

## Decisions (resolved during brainstorming)

1. **Individual `sectName` = `user.SectName`** directly — not dept-first-resolved, no `sectTCName` variant. (Dept-first applies only to `orgDescription`/`orgName`, because an org row's id can be a deptId.)
2. **`accountName` is set for every individual row, including bots** (`weather.bot` → `WEATHER.BOT`); `sectName`/`employeeId` stay empty for bots (no user doc).
3. **`orgDescription` empty-fallback is `""`** (omitted), not the orgID.

## Out of scope

- Backfilling existing User docs with descriptions — owned by the external HR/directory sync.
- OIDC claim extension for the description fields — `users` population does not flow through OIDC.
- TC-name variants of any new field (`sectTCName`, a `*TCName` description) — not requested.
- Expanding org rows into individual users — unchanged; orgs remain a single collapsed row carrying `orgName`/`memberCount`/`orgDescription`.
- Frontend rendering of the new fields — backend exposes them; UI uptake is separate.
