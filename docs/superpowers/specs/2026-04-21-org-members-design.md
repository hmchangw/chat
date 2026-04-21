# Get Org Members Design

**Date:** 2026-04-21
**Status:** Draft
**Service:** `room-service`

## Summary

Add a NATS request/reply endpoint in `room-service` that returns all users belonging to an org (sectId). The feature supports the "expand org" flow in the client UI: when the room members list returns an `org`-type entry, the client calls this endpoint to list the individual users that make up that org.

The implementation is a direct `Find({sectId: orgId})` query against the `users` collection, projecting only the fields the UI needs, sorted by `account` ascending. When the query returns zero rows (orgId matches no users), the handler returns a `errInvalidOrg` sentinel which is whitelisted by `sanitizeError` so the client sees a clear `"invalid org"` message.

## Scope

Covers:
- The NATS request/reply endpoint `chat.user.{account}.request.orgs.{orgId}.members`.
- A new `OrgMember` wire type projecting the user fields the UI needs.
- A new `ListOrgMembers` store method on `RoomStore`.
- The `errInvalidOrg` sentinel and its `sanitizeError` whitelist entry.
- Unit and integration tests; mock regeneration; subject-pkg additions.

Out of scope:
- Pagination — orgs are returned in full. (Revisit if a specific org grows unwieldy.)
- Authorization — any authenticated user can query any org, consistent with the existing `rooms.list` / `rooms.get` precedent. Org membership is effectively the corporate directory; no privacy check is added.
- Cross-site federation — `sectId` is global across sites in this codebase, so no `siteID` is needed in the subject.
- Org metadata endpoints (`org.info`, etc.) — not planned; the `orgs.*` namespace can grow later if needed.

## NATS Subjects

### Request/Reply (client → `room-service`)

| Operation | Subject | Queue Group |
|-----------|---------|-------------|
| List org members | `chat.user.{account}.request.orgs.{orgId}.members` | `room-service` |

The handler parses `orgId` from the subject. No request body is required; any body sent is ignored.

### Subject-package additions (`pkg/subject/subject.go`)

```go
// OrgMembers builds the subject for listing members of an org.
func OrgMembers(account, orgID string) string {
    return fmt.Sprintf("chat.user.%s.request.orgs.%s.members", account, orgID)
}

// OrgMembersWildcard is the subscription pattern for listing org members.
func OrgMembersWildcard() string {
    return "chat.user.*.request.orgs.*.members"
}
```

Because the subject shape is unique in this namespace, a new lightweight parser `ParseOrgMembersSubject(subj) (orgID string, ok bool)` is added alongside the builders. It returns the `orgId` token (positional index 5 — tokens are `[0]chat [1]user [2]{account} [3]request [4]orgs [5]{orgId} [6]members`) after validating the prefix/suffix.

## Data Models

### Response (`pkg/model/member.go`)

```go
// OrgMember is the wire projection returned by the list-org-members endpoint.
// Only fields the UI actually renders are included — EmployeeID, SectID, and
// SectName are intentionally omitted (redundant or irrelevant for the caller,
// who already knows which orgId they asked about).
type OrgMember struct {
    ID          string `json:"id"          bson:"_id"`
    Account     string `json:"account"     bson:"account"`
    EngName     string `json:"engName"     bson:"engName"`
    ChineseName string `json:"chineseName" bson:"chineseName"`
    SiteID      string `json:"siteId"      bson:"siteId"`
}

type ListOrgMembersResponse struct {
    Members []OrgMember `json:"members"`
}
```

No `ListOrgMembersRequest` type: the request has no body.

## Store Layer

### Interface (`room-service/store.go`)

```go
type RoomStore interface {
    // ...existing methods...

    // ListOrgMembers returns all users whose sectId equals orgID, projected as
    // OrgMember rows sorted by account ascending. Returns errInvalidOrg if the
    // query matches no users (treated as "orgId is not valid").
    ListOrgMembers(ctx context.Context, orgID string) ([]model.OrgMember, error)
}
```

### Mongo implementation (`room-service/store_mongo.go`)

```go
func (s *MongoStore) ListOrgMembers(ctx context.Context, orgID string) ([]model.OrgMember, error) {
    opts := options.Find().
        SetSort(bson.D{{Key: "account", Value: 1}}).
        SetProjection(bson.M{"_id": 1, "account": 1, "engName": 1, "chineseName": 1, "siteId": 1})
    cursor, err := s.users.Find(ctx, bson.M{"sectId": orgID}, opts)
    if err != nil {
        return nil, fmt.Errorf("find users for org %q: %w", orgID, err)
    }
    defer cursor.Close(ctx)

    var members []model.OrgMember
    if err := cursor.All(ctx, &members); err != nil {
        return nil, fmt.Errorf("decode users for org %q: %w", orgID, err)
    }
    if len(members) == 0 {
        return nil, errInvalidOrg
    }
    return members, nil
}
```

Reuses the existing `users` collection handle already present on `MongoStore` (added by the room-members enrichment path).

## Handler Layer

### Registration (`Handler.RegisterCRUD`)

```go
if _, err := nc.QueueSubscribe(subject.OrgMembersWildcard(), queue, h.natsListOrgMembers); err != nil {
    return fmt.Errorf("subscribe org members: %w", err)
}
```

### NATS entry point

```go
func (h *Handler) natsListOrgMembers(m otelnats.Msg) {
    resp, err := h.handleListOrgMembers(m.Context(), m.Msg.Subject)
    if err != nil {
        slog.Error("list org members failed", "subject", m.Msg.Subject, "error", err)
        natsutil.ReplyError(m.Msg, sanitizeError(err))
        return
    }
    if err := m.Msg.Respond(resp); err != nil {
        slog.Error("failed to respond to list-org-members", "error", err)
    }
}
```

Subject logged as a structured field; no identifier interpolation in error messages (same pattern as the room-members handler after the CodeRabbit review).

### Business logic

```go
func (h *Handler) handleListOrgMembers(ctx context.Context, subj string) ([]byte, error) {
    orgID, ok := subject.ParseOrgMembersSubject(subj)
    if !ok {
        return nil, fmt.Errorf("invalid org-members subject")
    }
    members, err := h.store.ListOrgMembers(ctx, orgID)
    if err != nil {
        if errors.Is(err, errInvalidOrg) {
            return nil, errInvalidOrg
        }
        return nil, fmt.Errorf("get org members: %w", err)
    }
    return json.Marshal(model.ListOrgMembersResponse{Members: members})
}
```

No auth check, consistent with `RoomsList` / `RoomsGet`. No pagination params.

### Error sentinel (`helper.go`)

```go
errInvalidOrg = errors.New("invalid org")
```

Added to the `sanitizeError` whitelist. Client sees the message verbatim. Since the caller already owns the `orgId` (it was in their own subject), the sentinel does not echo it back.

## Response Flow

1. Client sends `nc.Request(subject.OrgMembers(account, orgID), nil, timeout)`.
2. NATS delivers to a `room-service` instance subscribed on `OrgMembersWildcard()` (queue group `room-service`).
3. Handler parses `orgID` from the subject, calls `store.ListOrgMembers`.
4. On success: `m.Msg.Respond(json(ListOrgMembersResponse{...}))`.
5. On `errInvalidOrg`: `natsutil.ReplyError(..., "invalid org")`.
6. On any other error: `natsutil.ReplyError(..., "internal error")` via `sanitizeError` default.

## Testing

### Unit tests — `handler_test.go`

Table-driven `TestHandler_ListOrgMembers` using `NewMockRoomStore`:

| # | Scenario | Mock setup | Expected |
|---|----------|------------|----------|
| 1 | Happy path | `ListOrgMembers(_, "sect-eng")` returns 3 `OrgMember` rows | resp decodes to the same 3 rows |
| 2 | Invalid subject | subject does not match the org-members pattern | error contains `"invalid org-members subject"` |
| 3 | Empty org | `ListOrgMembers` returns `errInvalidOrg` | error is `errInvalidOrg`; `sanitizeError` returns `"invalid org"` |
| 4 | Store error (generic) | `ListOrgMembers` returns generic err | wrapped `"get org members"` |

Plus `TestSanitizeError_InvalidOrg` to confirm the new sentinel is whitelisted.

### Integration tests — `integration_test.go` (`//go:build integration`)

`TestMongoStore_ListOrgMembers_Integration` with subtests:

| # | Scenario | Setup | Expected |
|---|----------|-------|----------|
| 1 | Returns members sorted by account asc | seed 3 users with `sectId="sect-eng"` and different accounts | 3 rows returned in alphabetical order by `account` |
| 2 | Filters by sectId only | seed 2 users with `sect-eng` and 2 with `sect-ops` | query for `sect-eng` returns exactly those 2, none of the others |
| 3 | Empty org returns errInvalidOrg | no matching users | error wraps `errInvalidOrg` (assert via `errors.Is`) |
| 4 | Projection excludes non-listed fields | seed user with `EmployeeID`, `SectID`, `SectName` populated | returned `OrgMember` has only `id`, `account`, `engName`, `chineseName`, `siteId` populated; other fields of the underlying user are never surfaced |

### Coverage

- Minimum 80% coverage across `room-service`, target 90%+ on the new handler + store methods (CLAUDE.md §4).
- TDD Red-Green-Refactor: tests first, store+handler to green, commit.

## Files Changed

| File | Change |
|------|--------|
| `pkg/subject/subject.go` | Add `OrgMembers`, `OrgMembersWildcard`, `ParseOrgMembersSubject` |
| `pkg/subject/subject_test.go` | Add test rows |
| `pkg/model/member.go` | Add `OrgMember` and `ListOrgMembersResponse` |
| `pkg/model/model_test.go` | Add round-trip test for `OrgMember` |
| `room-service/store.go` | Add `ListOrgMembers` to `RoomStore` interface |
| `room-service/store_mongo.go` | Implement `ListOrgMembers` |
| `room-service/mock_store_test.go` | Regenerated via `make generate SERVICE=room-service` |
| `room-service/handler.go` | Add `natsListOrgMembers`, `handleListOrgMembers`; register in `RegisterCRUD` |
| `room-service/handler_test.go` | Add `TestHandler_ListOrgMembers` + `TestSanitizeError_InvalidOrg` |
| `room-service/helper.go` | Add `errInvalidOrg` sentinel; extend `sanitizeError` whitelist |
| `room-service/integration_test.go` | Add `TestMongoStore_ListOrgMembers_Integration` |

No changes to `main.go` — the new subscription registers inside the existing `RegisterCRUD` call.
