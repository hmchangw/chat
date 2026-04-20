# Room Members List Design

**Date:** 2026-04-20
**Status:** Draft
**Service:** `room-service`

## Summary

Add a NATS request/reply endpoint and a `RoomStore` method to return the members of a room. The feature serves two audiences through a single call:

- **Internal callers** (other handlers inside `room-service`) — fast path: bare `RoomMember` rows, no joins. Default when `enrich=false`.
- **Client UI** — enriched path: adds per-member display data. For individuals: `engName`, `chineseName`, and `isOwner` (derived from the subscription's roles). For orgs: `sectName` and `memberCount` (total users in the org). Triggered by `enrich=true` in the request body.

The lookup prefers the `room_members` collection (the membership-source table populated by invite flows). If `room_members` has no docs for the room, the store falls back to the `subscriptions` collection and synthesizes `RoomMember` entries (`type="individual"`, `ID=sub.ID`) so callers always see the same response shape. When `enrich=true`, the pipeline conditionally appends `$lookup` stages against `users` (and derives `isOwner` from `subscriptions.roles`) to attach display data.

The response shape is the same in both modes: a single `ListRoomMembersResponse{Members []RoomMember}`. Enrichment fields live on `RoomMemberEntry` with `bson:"-"` + `json:",omitempty"`, so they never participate in persistence and are elided from the wire when empty.

All requests run at the room's home site. NATS gateways route cross-site requests to the owning cluster transparently; `room-service` only subscribes on its own `siteID`.

## Scope

Covers:
- The NATS request/reply endpoint `chat.user.{account}.request.room.{roomID}.{siteID}.member.list`.
- Authorization guard (requester must hold a subscription to the room).
- Optional `limit` / `offset` pagination with a stable sort.
- A single `ListRoomMembers(ctx, roomID, limit, offset, enrich)` method on `RoomStore`. Two-path lookup (existence probe on `room_members`, fall back to `subscriptions`). When `enrich=true`, the pipeline appends `$lookup` stages to attach `engName`/`chineseName`/`isOwner` to individuals and `sectName`/`memberCount` to orgs.
- Display fields added to `RoomMemberEntry` with `bson:"-"` + `json:",omitempty"` — never persisted, elided from the wire when empty.
- Unit and integration tests, mocks, and subject-pkg additions.

Out of scope:
- Expanding `org` entries in `room_members` into individual users (clients render the org entry as-is, using `sectName` + `memberCount`).
- Cross-service NATS contracts — this is internal-to-room-service and client-facing only; other services continue to use their own subscription queries.
- Any refactoring of `handleInvite` / `handleRemoveMember` to share the membership check.
- Caching, presence, or read-receipt data on members.

## NATS Subjects

### Request/Reply (client or other caller → `room-service`)

| Operation | Subject | Queue Group |
|-----------|---------|-------------|
| List members | `chat.user.{account}.request.room.{roomID}.{siteID}.member.list` | `room-service` |

The subject follows the same pattern as `member.invite`, `member.remove`, `member.role-update`. The handler parses the requester `account` and `roomID` from the subject via `subject.ParseUserRoomSubject`. The `{siteID}` is the room's site.

### Subject-package additions (`pkg/subject/subject.go`)

```go
func MemberList(account, roomID, siteID string) string {
    return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.list", account, roomID, siteID)
}

func MemberListWildcard(siteID string) string {
    return fmt.Sprintf("chat.user.*.request.room.*.%s.member.list", siteID)
}
```

## Data Models

### Request / Response (`pkg/model/member.go`)

```go
type ListRoomMembersRequest struct {
    Limit  *int `json:"limit,omitempty"`
    Offset *int `json:"offset,omitempty"`
    Enrich bool `json:"enrich,omitempty"`   // false (default) → bare; true → enriched
}

type ListRoomMembersResponse struct {
    Members []RoomMember `json:"members"`
}
```

`Limit` and `Offset` are pointers to distinguish "unset" from `0`. `roomID` and requester `account` come from the NATS subject, not the body. An empty request body is valid and means "no pagination, bare response". A single response type serves both modes — enrichment fields on each member are populated only when `Enrich=true` and elided otherwise via `omitempty`.

Wire example (`enrich=true`):
```json
{"members":[
  {"id":"rm2","rid":"r1","ts":"...","member":{"id":"org-1","type":"org","sectName":"Engineering","memberCount":42}},
  {"id":"rm1","rid":"r1","ts":"...","member":{"id":"u-1","type":"individual","account":"alice","engName":"Alice Wang","chineseName":"愛麗絲","isOwner":true}}
]}
```

Wire example (`enrich=false`, same data):
```json
{"members":[
  {"id":"rm2","rid":"r1","ts":"...","member":{"id":"org-1","type":"org"}},
  {"id":"rm1","rid":"r1","ts":"...","member":{"id":"u-1","type":"individual","account":"alice"}}
]}
```

### Response element — `RoomMember` (extended)

`RoomMember` itself is unchanged. `RoomMemberEntry` gains five display fields, all tagged `bson:"-"` (never persisted) and `json:",omitempty"` (elided from the wire when empty). The persisted shape of `room_members` documents is therefore unchanged — only the wire/API shape grows when `enrich=true`.

```go
type RoomMember struct {
    ID     string          `json:"id"     bson:"_id"`
    RoomID string          `json:"rid"    bson:"rid"`
    Ts     time.Time       `json:"ts"     bson:"ts"`
    Member RoomMemberEntry `json:"member" bson:"member"`
}

type RoomMemberEntry struct {
    ID      string         `json:"id"                bson:"id"`
    Type    RoomMemberType `json:"type"              bson:"type"`
    Account string         `json:"account,omitempty" bson:"account,omitempty"`

    // Display fields — never persisted (bson:"-"); populated only when enrich=true.
    // Populated when type="individual":
    EngName     string `json:"engName,omitempty"     bson:"-"`
    ChineseName string `json:"chineseName,omitempty" bson:"-"`
    IsOwner     bool   `json:"isOwner,omitempty"     bson:"-"`
    // Populated when type="org":
    SectName  string `json:"sectName,omitempty"    bson:"-"`
    MemberCount int    `json:"memberCount,omitempty"   bson:"-"`
}
```

Why `bson:"-"` rather than `bson:",omitempty"`: these fields are not part of the `room_members` collection schema. `bson:"-"` prevents writes from polluting persistence even if a caller forgets to clear them, and prevents reads from picking up stray values that were never supposed to be there.

## Store Layer

### Interface (`room-service/store.go`)

```go
type RoomStore interface {
    // ...existing methods...

    // ListRoomMembers returns the members of roomID. When enrich=true, the
    // returned RoomMember.Member entries carry display fields populated via
    // $lookup stages against users and subscriptions. When enrich=false,
    // those fields are left zero.
    ListRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error)
}
```

### Mongo implementation (`room-service/store_mongo.go`)

```go
func (s *MongoStore) ListRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    // 1. Existence probe — does room_members have any doc for this room?
    err := s.roomMembers.FindOne(ctx, bson.M{"rid": roomID}).Err()
    switch {
    case err == nil:
        return s.getRoomMembers(ctx, roomID, limit, offset, enrich)
    case errors.Is(err, mongo.ErrNoDocuments):
        return s.getRoomSubscriptions(ctx, roomID, limit, offset, enrich)
    default:
        return nil, fmt.Errorf("probe room_members for %q: %w", roomID, err)
    }
}
```

The probe is a bounded `FindOne`; it distinguishes "pagination past end" from "collection empty", which a paginated page-load can't.

#### `getRoomMembers` — paginated `room_members` (conditional enrichment)

Aggregation pipeline. The base stages are always present — match, typeOrder addField, sort, skip, limit, project. When `enrich=true`, additional `$lookup` stages are appended after the page is sliced (so enrichment runs on only the page-size rows, not the full collection):

```go
pipeline := mongo.Pipeline{
    {{Key: "$match", Value: bson.M{"rid": roomID}}},
    {{Key: "$addFields", Value: bson.M{
        "typeOrder": bson.M{"$cond": bson.A{
            bson.M{"$eq": bson.A{"$member.type", "org"}}, 0, 1,
        }},
    }}},
    {{Key: "$sort", Value: bson.D{
        {Key: "typeOrder", Value: 1},
        {Key: "ts", Value: 1},
        {Key: "_id", Value: 1}, // tiebreaker for stable pagination
    }}},
}
if offset != nil {
    pipeline = append(pipeline, bson.D{{Key: "$skip", Value: int64(*offset)}})
}
if limit != nil {
    pipeline = append(pipeline, bson.D{{Key: "$limit", Value: int64(*limit)}})
}

if enrich {
    // For individuals: attach engName/chineseName from users collection.
    // For orgs: attach sectName + memberCount via users collection.
    // For individuals: attach isOwner by looking up the subscription's roles.
    // $lookup stages use $expr so they only match rows where member.type fits.
    pipeline = append(pipeline, enrichmentStages(roomID)...)
}

// Final $project strips helper fields (typeOrder, lookup arrays).
pipeline = append(pipeline, bson.D{{Key: "$project", Value: bson.M{
    "typeOrder": 0,
    // also drop any temp lookup arrays introduced by enrichmentStages
}}})
```

Because the display fields on `RoomMemberEntry` are tagged `bson:"-"`, the driver will refuse to decode them from BSON — any enrichment written to `member.engName`/`member.isOwner`/etc. would be silently dropped during `cursor.All`. The aggregation must therefore write those fields into a **parallel `display` sub-document**, and Go-side post-processing copies them onto `RoomMember.Member` before returning. In practice:

1. Run the aggregation with `$set` placing the enrichment into e.g. `display.engName`, `display.isOwner`, etc. on a parallel temp field (not inside `member`, to avoid the `bson:"-"` filter).
2. Decode into a small local struct `roomMemberRow { RoomMember; Display roomMemberDisplay }`, where `roomMemberDisplay` has normal bson tags for the same five fields.
3. Post-process: for each row, copy `Display.*` → `row.RoomMember.Member.*` (these are all in-memory fields at that point, so the `bson:"-"` tag is irrelevant).

This keeps persistence semantics strict (display fields never round-trip through BSON on `room_members`) while still using Mongo to do the joins.

#### `getRoomSubscriptions` — paginated `subscriptions` fallback (conditional enrichment)

```go
opts := options.Find().SetSort(bson.D{{Key: "joinedAt", Value: 1}, {Key: "_id", Value: 1}})
if offset != nil {
    opts.SetSkip(int64(*offset))
}
if limit != nil {
    opts.SetLimit(int64(*limit))
}
cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID}, opts)
// ...
var subs []model.Subscription
if err := cursor.All(ctx, &subs); err != nil { ... }

members := make([]model.RoomMember, 0, len(subs))
for i := range subs {
    sub := &subs[i]
    entry := model.RoomMemberEntry{
        ID:      sub.User.ID,
        Type:    model.RoomMemberIndividual,
        Account: sub.User.Account,
    }
    if enrich {
        entry.IsOwner = hasRole(sub.Roles, model.RoleOwner)
    }
    members = append(members, model.RoomMember{
        ID: sub.ID, RoomID: roomID, Ts: sub.JoinedAt, Member: entry,
    })
}

// For enrich=true, batch-load the display names in a single query to avoid
// N+1. All rows in this path are individuals — no org lookup is needed.
if enrich && len(members) > 0 {
    accounts := make([]string, 0, len(members))
    for _, m := range members {
        accounts = append(accounts, m.Member.Account)
    }
    userCur, err := s.users.Find(ctx, bson.M{"account": bson.M{"$in": accounts}},
        options.Find().SetProjection(bson.M{"account": 1, "engName": 1, "chineseName": 1}))
    if err != nil {
        return nil, fmt.Errorf("find users for %q: %w", roomID, err)
    }
    var users []model.User
    if err := userCur.All(ctx, &users); err != nil {
        return nil, fmt.Errorf("decode users for %q: %w", roomID, err)
    }
    byAccount := make(map[string]*model.User, len(users))
    for i := range users {
        byAccount[users[i].Account] = &users[i]
    }
    for i := range members {
        if u, ok := byAccount[members[i].Member.Account]; ok {
            members[i].Member.EngName = u.EngName
            members[i].Member.ChineseName = u.ChineseName
        }
    }
}
return members, nil
```

Cost model: one query when `enrich=false`; two queries (subs page + user batch) when `enrich=true`. No N+1.

#### Enrichment stages for the `room_members` pipeline

When `enrich=true`, append these stages after `$skip`/`$limit`. All stages use `$expr` conditions on `member.type` so they only match rows of the matching kind:

1. **Individuals — display names:** `$lookup` into `users` with sub-pipeline `{$match: {$expr: {$and: [{$eq: ["$account", "$$acct"]}, {$eq: ["$$type", "individual"]}]}}}`; project `{engName, chineseName}`. Flatten with `$arrayElemAt`.
2. **Individuals — isOwner:** `$lookup` into `subscriptions` with sub-pipeline `{$match: {$expr: {$and: [{$eq: ["$roomId", roomID]}, {$eq: ["$u.account", "$$acct"]}, {$eq: ["$$type", "individual"]}]}}}`; project `{roles}`. In `$addFields`, compute `isOwner = {$in: ["owner", {$ifNull: [{$arrayElemAt: ["$tmp.roles", 0]}, []]}]}`.
3. **Orgs — sectName + memberCount:** `$lookup` into `users` with sub-pipeline `{$match: {$expr: {$and: [{$eq: ["$sectId", "$$orgId"]}, {$eq: ["$$type", "org"]}]}}}`; `$group: {_id: null, sectName: {$first: "$sectName"}, memberCount: {$sum: 1}}`. Flatten.
4. **Final `$set`:** write the resolved values into a non-`member` temp field (e.g., `display`) so the BSON codec can decode them into a Go-side struct with normal bson tags, then post-process in Go to copy them onto `member.*`.

### Sort contract

Regardless of which path served the request, and whether `enrich=true` or `false`:
- Orgs first, individuals second (fallback path has only individuals, so this is trivially satisfied).
- Within each group, `ts` ascending (oldest first).
- `_id` tiebreaker to keep pagination stable when rows share the same `ts`.

## Handler Layer

### Registration (`Handler.RegisterCRUD` in `handler.go`)

```go
if _, err := nc.QueueSubscribe(subject.MemberListWildcard(h.siteID), queue, h.natsListMembers); err != nil {
    return fmt.Errorf("subscribe member list: %w", err)
}
```

### NATS entry point

```go
func (h *Handler) natsListMembers(m otelnats.Msg) {
    resp, err := h.handleListMembers(m.Context(), m.Msg.Subject, m.Msg.Data)
    if err != nil {
        slog.Error("list members failed", "error", err)
        natsutil.ReplyError(m.Msg, sanitizeError(err))
        return
    }
    if err := m.Msg.Respond(resp); err != nil {
        slog.Error("failed to respond to list-members", "error", err)
    }
}
```

### Business logic

```go
func (h *Handler) handleListMembers(ctx context.Context, subj string, data []byte) ([]byte, error) {
    requesterAccount, roomID, ok := subject.ParseUserRoomSubject(subj)
    if !ok {
        return nil, fmt.Errorf("invalid list-members subject")
    }

    _, err := h.store.GetSubscription(ctx, requesterAccount, roomID)
    switch {
    case errors.Is(err, model.ErrSubscriptionNotFound):
        return nil, errNotRoomMember
    case err != nil:
        return nil, fmt.Errorf("check room membership: %w", err)
    }

    var req model.ListRoomMembersRequest
    if len(data) > 0 {
        if err := json.Unmarshal(data, &req); err != nil {
            return nil, fmt.Errorf("invalid request: %w", err)
        }
    }
    if req.Limit != nil && *req.Limit <= 0 {
        return nil, fmt.Errorf("limit must be > 0")
    }
    if req.Offset != nil && *req.Offset < 0 {
        return nil, fmt.Errorf("offset must be >= 0")
    }

    members, err := h.store.ListRoomMembers(ctx, roomID, req.Limit, req.Offset, req.Enrich)
    if err != nil {
        return nil, fmt.Errorf("get room members: %w", err)
    }
    return json.Marshal(model.ListRoomMembersResponse{Members: members})
}
```

The handler no longer branches on response type — `Enrich` is plumbed through to the store and the same `ListRoomMembersResponse` is returned in both modes.

### Error sentinel (`helper.go`)

```go
errNotRoomMember = errors.New("only room members can list members")
```

Add to the `sanitizeError` whitelist so the client sees the user-friendly message.

## Response Flow (request/reply)

1. Caller sends `nc.Request(subject.MemberList(account, roomID, siteID), body, timeout)`.
2. NATS delivers to a `room-service` instance subscribed on `MemberListWildcard(h.siteID)` (queue group `room-service`). Cross-site requests are routed by NATS gateways to the room's home site.
3. Handler authorizes (requester must have a subscription), parses pagination + `Enrich`, and calls `store.ListRoomMembers(..., req.Enrich)`. The store runs the two-path lookup and appends enrichment stages when `enrich=true`.
4. On success, `m.Msg.Respond(json(ListRoomMembersResponse{...}))` publishes to the reply inbox NATS attached to the request. The response shape is the same in both modes; only the optional `member.*` display fields differ. On error, `natsutil.ReplyError(m.Msg, sanitizeError(err))` publishes a `model.ErrorResponse`.
5. Caller receives the reply on its inbox; no reply subject is built in service code.

## Testing

### Unit tests — `handler_test.go`

Table-driven `TestHandler_ListMembers` using `NewMockRoomStore`:

| # | Scenario | Mock setup | Expected |
|---|----------|------------|----------|
| 1 | Bare happy path | `GetSubscription` ok; `ListRoomMembers(_,_,_,_,false)` returns 2 bare members | 2 members in resp, no display fields |
| 2 | Bare fallback path | same as #1 but store returns synthesized individuals | resp matches; all `Type == individual`, no display fields |
| 3 | Requester not a member | `GetSubscription` → `ErrSubscriptionNotFound` | error `errNotRoomMember`; `sanitizeError` returns its text |
| 4 | Invalid subject | subject doesn't match `chat.user.*.room.*` | error contains "invalid list-members subject" |
| 5 | Invalid JSON body | body = `[]byte("{not json")` | error contains "invalid request" |
| 6 | Empty body | `data = nil`; store called with `enrich=false` and nil pagination | succeeds |
| 7 | Non-positive limit (negative or zero) | `Limit=-1` or `Limit=0` | error "limit must be > 0" |
| 8 | Negative offset | `Offset=-1` | error "offset must be >= 0" |
| 9 | Pagination passed through | `Limit=10, Offset=5` | store called with matching `*int` values |
| 10 | Auth probe infra error | `GetSubscription` → generic err | wrapped "check room membership" |
| 11 | Store error on list | `ListRoomMembers` → err | wrapped "get room members" |
| 12 | Enrich=true passed through | body `{"enrich":true}` | store called with `enrich=true`; returned members carry `EngName`/`IsOwner`/`SectName`/`MemberCount` from the mock; response JSON contains `engName`/`isOwner`/etc. fields |
| 13 | Enrich=false passed through (explicit) | body `{"enrich":false}` | store called with `enrich=false`; response JSON does NOT contain any display fields |

Plus `TestSanitizeError_NotRoomMember` to confirm the new sentinel is whitelisted.

### Integration tests — `integration_test.go` (`//go:build integration`)

Reuse the existing `setupMongo(t)` testcontainers helper:

| # | Scenario | Setup | Expected |
|---|----------|-------|----------|
| 1 | Returns room_members when populated | 2 individual + 1 org docs in `room_members` for R | 3 docs, orgs first, each group by `ts` asc |
| 2 | Falls back to subscriptions when empty | 3 subscriptions, 0 room_members for R | 3 members, all `Type=individual`, `ID==sub.ID` |
| 3 | Limit only | 5 docs, `limit=2` | 2 docs from start |
| 4 | Offset only | 5 docs, `offset=2` | 3 docs from index 2 |
| 5 | Limit + offset | 5 docs, `limit=2, offset=1` | 2 docs at indices 1,2 |
| 6 | Empty room | nothing seeded | empty slice, no error |
| 7 | room_members with only orgs | 2 org docs only | both returned; no fallback |
| 8 | Synthesized `ID == sub.ID` | sub with `_id="sub-xyz"` | returned `RoomMember.ID == "sub-xyz"` |
| 9 | Stable pagination with same `ts` | 3 docs sharing `ts` | `_id` tiebreaker — `(limit=1, offset=0/1/2)` covers all three exactly once |
| 10 | Enrich=true — individual (room_members path) | seed 1 individual `room_member` + matching user (`engName`, `chineseName`) + subscription with `roles=[owner]`; call with `enrich=true` | `Member.EngName`, `ChineseName`, `IsOwner=true` populated on returned `RoomMember` |
| 11 | Enrich=true — org (room_members path) | seed 1 org `room_member` (id=sect-1) + 3 users with `sectId=sect-1` and `sectName="Eng"`; call with `enrich=true` | `Member.SectName="Eng"`, `MemberCount=3` |
| 12 | Enrich=true — non-owner individual | same as #10 but `roles=[member]` | `IsOwner=false`; corresponding JSON field omitted |
| 13 | Enrich=true — subscriptions fallback | 2 subscriptions, 0 room_members; seed matching users; call with `enrich=true` | 2 members, each with `IsOwner` from `roles` and `EngName`/`ChineseName` from users |
| 14 | Enrich=true — sort + pagination match bare behavior | reuse a pagination seed set; compare ordering and page slicing with `enrich=false` | identical order, identical page membership |
| 15 | Enrich=false on same seed set as #10 | call with `enrich=false` | display fields are all zero on every returned member (JSON output does not contain `engName`, `isOwner`, `sectName`, etc.) |

### Coverage and discipline

- Minimum 80% coverage across `room-service`, target 90%+ on the new handler + store methods (CLAUDE.md §4).
- TDD Red-Green-Refactor: write failing tests first, then store method, then handler.
- No shared state between tests; each uses its own `gomock.Controller`.
- Run `make generate SERVICE=room-service` after updating `store.go`.
- Use `-race` via `make test`.

## Files Changed

| File | Change |
|------|--------|
| `pkg/subject/subject.go` | Add `MemberList` + `MemberListWildcard` |
| `pkg/subject/subject_test.go` | Add test cases for the new builders |
| `pkg/model/member.go` | Add `ListRoomMembersRequest` (with `Enrich` field) and `ListRoomMembersResponse`. Extend `RoomMemberEntry` with five display fields (`EngName`, `ChineseName`, `IsOwner`, `SectName`, `MemberCount`) — all tagged `bson:"-"` + `json:",omitempty"` |
| `pkg/model/model_test.go` | Add round-trip test cases for the new types; add a round-trip test that confirms a populated `RoomMemberEntry` with display fields marshals to JSON correctly AND that those fields are absent from BSON marshal output |
| `room-service/store.go` | Add `ListRoomMembers(ctx, roomID, limit, offset, enrich)` to `RoomStore` interface |
| `room-service/store_mongo.go` | Implement `ListRoomMembers` plus its private helpers `getRoomMembers` (aggregation pipeline with conditional enrichment stages) and `getRoomSubscriptions` (Find + conditional batch user lookup) |
| `room-service/mock_store_test.go` | Regenerated via `make generate SERVICE=room-service` |
| `room-service/handler.go` | Add `natsListMembers`, `handleListMembers` (plumbs `req.Enrich` straight to the store); register in `RegisterCRUD` |
| `room-service/handler_test.go` | Add `TestHandler_ListMembers` table test (covers bare + enriched paths) + `TestSanitizeError_NotRoomMember` |
| `room-service/helper.go` | Add `errNotRoomMember` sentinel; extend `sanitizeError` whitelist |
| `room-service/integration_test.go` | Add `ListRoomMembers` integration cases (bare + enriched) |

No changes to `main.go` are needed: the wildcard subscription is registered inside the existing `RegisterCRUD` call.
