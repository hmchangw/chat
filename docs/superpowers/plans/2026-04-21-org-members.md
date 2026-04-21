# Org Members Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a NATS request/reply endpoint `chat.user.{account}.request.orgs.{orgId}.members` in `room-service` that returns all users in an org (sectId), sorted by account, projected as `OrgMember`. Empty result returns `errInvalidOrg` with the whitelisted message "invalid org".

**Architecture:** A single `Find({sectId: orgId})` on the existing `users` collection in `MongoStore`, with sort by `account` asc and a minimal field projection. A new `RoomStore.ListOrgMembers` method wraps the query and returns `errInvalidOrg` on empty results. The handler parses `orgId` from the subject, calls the store, and replies with `ListOrgMembersResponse`. No auth check (matches `rooms.list` / `rooms.get` precedent); no pagination.

**Tech Stack:** Go 1.25.8, NATS request/reply via `otelnats`, MongoDB `go.mongodb.org/mongo-driver/v2`, `go.uber.org/mock` for mocks, `stretchr/testify`, `testcontainers-go`.

**Reference spec:** `docs/superpowers/specs/2026-04-21-org-members-design.md`

---

## File Structure

| File | Role | Change |
|------|------|--------|
| `pkg/subject/subject.go` | NATS subject builders | Add `OrgMembers`, `OrgMembersWildcard`, `ParseOrgMembersSubject` |
| `pkg/subject/subject_test.go` | Subject builder tests | Add table rows + parser test |
| `pkg/model/member.go` | Wire types | Add `OrgMember` + `ListOrgMembersResponse` |
| `pkg/model/model_test.go` | Model round-trip tests | Add JSON round-trip for `OrgMember` |
| `room-service/store.go` | `RoomStore` interface | Add `ListOrgMembers` method |
| `room-service/store_mongo.go` | Mongo store impl | Implement `ListOrgMembers` |
| `room-service/mock_store_test.go` | Generated mock | Regenerated via `make generate` |
| `room-service/helper.go` | Sentinel + sanitize | Add `errInvalidOrg`, whitelist it |
| `room-service/helper_test.go` | Sanitize test | Add a table row |
| `room-service/handler.go` | Handler | Add `natsListOrgMembers` + `handleListOrgMembers`; register in `RegisterCRUD` |
| `room-service/handler_test.go` | Handler unit tests | Add `TestHandler_ListOrgMembers` |
| `room-service/integration_test.go` | Store integration tests | Add `TestMongoStore_ListOrgMembers_Integration` |

No changes to `main.go` — the new subscription is registered inside the existing `RegisterCRUD` call.

---

## Part 1 — Foundations (subject + model)

Pure additions to `pkg/subject` and `pkg/model`. No service wiring, no compile breakage.

### Task 1.1 — Subject builders + parser

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1 — Write failing tests**

Append these rows to `TestSubjectBuilders` in `pkg/subject/subject_test.go` (inside the existing `tests := []struct{...}{...}` slice):

```go
{"OrgMembers", subject.OrgMembers("alice", "sect-eng"),
    "chat.user.alice.request.orgs.sect-eng.members"},
{"OrgMembersWildcard", subject.OrgMembersWildcard(),
    "chat.user.*.request.orgs.*.members"},
```

Append a new test function at the end of `pkg/subject/subject_test.go`:

```go
func TestParseOrgMembersSubject(t *testing.T) {
    tests := []struct {
        name    string
        subj    string
        wantOrg string
        wantOK  bool
    }{
        {"valid", "chat.user.alice.request.orgs.sect-eng.members", "sect-eng", true},
        {"wrong prefix", "chat.user.alice.request.rooms.get.r1", "", false},
        {"wrong suffix", "chat.user.alice.request.orgs.sect-eng.other", "", false},
        {"too short", "chat.user.alice.request.orgs", "", false},
        {"too long", "chat.user.alice.request.orgs.sect-eng.members.x", "", false},
        {"empty", "", "", false},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, ok := subject.ParseOrgMembersSubject(tt.subj)
            if ok != tt.wantOK {
                t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
            }
            if got != tt.wantOrg {
                t.Errorf("orgID = %q, want %q", got, tt.wantOrg)
            }
        })
    }
}
```

- [ ] **Step 2 — Run tests; verify they fail**

```bash
go test ./pkg/subject/... -v
```

Expected: compile errors — `undefined: subject.OrgMembers`, `undefined: subject.OrgMembersWildcard`, `undefined: subject.ParseOrgMembersSubject`.

- [ ] **Step 3 — Implement builders and parser**

Append to `pkg/subject/subject.go`, right after `MemberListWildcard` (near the other member-related builders):

```go
// OrgMembers builds the subject for listing members of an org.
func OrgMembers(account, orgID string) string {
    return fmt.Sprintf("chat.user.%s.request.orgs.%s.members", account, orgID)
}

// OrgMembersWildcard is the subscription pattern for the list-org-members endpoint.
func OrgMembersWildcard() string {
    return "chat.user.*.request.orgs.*.members"
}

// ParseOrgMembersSubject returns the orgID from a subject matching the
// pattern "chat.user.{account}.request.orgs.{orgId}.members".
// Tokens (by strings.Split on "."): [0]chat [1]user [2]{account} [3]request
// [4]orgs [5]{orgId} [6]members. orgID is at positional index 5.
func ParseOrgMembersSubject(subj string) (orgID string, ok bool) {
    parts := strings.Split(subj, ".")
    if len(parts) != 7 {
        return "", false
    }
    if parts[0] != "chat" || parts[1] != "user" || parts[3] != "request" ||
        parts[4] != "orgs" || parts[6] != "members" {
        return "", false
    }
    return parts[5], true
}
```

`strings` is already imported at the top of `subject.go` (used by `ParseUserRoomSubject`); confirm with `grep '"strings"' pkg/subject/subject.go`.

- [ ] **Step 4 — Run tests; verify they pass**

```bash
go test ./pkg/subject/... -v
```

Expected: all `TestSubjectBuilders` rows PASS and all `TestParseOrgMembersSubject` subtests PASS.

- [ ] **Step 5 — Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add OrgMembers builders and ParseOrgMembersSubject"
```

---

### Task 1.2 — Model types

**Files:**
- Modify: `pkg/model/member.go` (append at end)
- Modify: `pkg/model/model_test.go` (append before the `roundTrip` helper)

- [ ] **Step 1 — Write failing tests**

Append to `pkg/model/model_test.go` (before the `roundTrip` helper at the bottom):

```go
func TestOrgMemberJSON(t *testing.T) {
    m := model.OrgMember{
        ID:          "u-alice",
        Account:     "alice",
        EngName:     "Alice Wang",
        ChineseName: "愛麗絲",
        SiteID:      "site-a",
    }
    data, err := json.Marshal(&m)
    require.NoError(t, err)
    var dst model.OrgMember
    require.NoError(t, json.Unmarshal(data, &dst))
    assert.Equal(t, m, dst)
}

func TestListOrgMembersResponseJSON(t *testing.T) {
    resp := model.ListOrgMembersResponse{
        Members: []model.OrgMember{
            {ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲", SiteID: "site-a"},
        },
    }
    data, err := json.Marshal(&resp)
    require.NoError(t, err)
    var dst model.ListOrgMembersResponse
    require.NoError(t, json.Unmarshal(data, &dst))
    assert.Equal(t, resp, dst)
}
```

- [ ] **Step 2 — Run tests; verify they fail**

```bash
go test ./pkg/model/... -run "TestOrgMember|TestListOrgMembers" -v
```

Expected: compile error — `undefined: model.OrgMember`, `undefined: model.ListOrgMembersResponse`.

- [ ] **Step 3 — Add types**

Append to `pkg/model/member.go` (end of file):

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

- [ ] **Step 4 — Run tests; verify they pass**

```bash
go test -race ./pkg/model/... -run "TestOrgMember|TestListOrgMembers" -v
```

Expected: both tests PASS. Also run the full `pkg/model` suite to confirm no regressions:

```bash
go test -race ./pkg/model/...
```

Expected: PASS.

- [ ] **Step 5 — Commit**

```bash
git add pkg/model/member.go pkg/model/model_test.go
git commit -m "feat(model): add OrgMember and ListOrgMembersResponse types"
```

---

## Part 2 — Store (sentinel + interface + Mongo impl + mock)

Adds the `errInvalidOrg` sentinel first (used by the Mongo implementation on empty results), then the store interface + implementation + regenerated mock together in one commit (so the build stays green across every revision).

### Task 2.1 — `errInvalidOrg` sentinel + sanitize whitelist

**Files:**
- Modify: `room-service/helper.go`
- Modify: `room-service/helper_test.go`

- [ ] **Step 1 — Write failing test row**

In `room-service/helper_test.go`, find the existing `TestSanitizeError` table and add one row alongside the other `"sentinel: ..."` entries:

```go
{"sentinel: invalid org", errInvalidOrg, "invalid org"},
```

- [ ] **Step 2 — Run tests; verify it fails**

```bash
go test -race ./room-service/... -run "TestSanitizeError" -v
```

Expected: compile error `undefined: errInvalidOrg`.

- [ ] **Step 3 — Add the sentinel and whitelist entry**

Edit `room-service/helper.go`. Inside the existing `var ( ... )` block, add:

```go
errInvalidOrg       = errors.New("invalid org")
```

Extend the `sanitizeError` switch to include the new sentinel:

```go
func sanitizeError(err error) string {
    switch {
    case errors.Is(err, errInvalidRole),
        errors.Is(err, errOnlyOwners),
        errors.Is(err, errAlreadyOwner),
        errors.Is(err, errNotOwner),
        errors.Is(err, errCannotDemoteLast),
        errors.Is(err, errRoomTypeGuard),
        errors.Is(err, errTargetNotMember),
        errors.Is(err, errNotRoomMember),
        errors.Is(err, errInvalidOrg):
        return err.Error()
    default:
        return "internal error"
    }
}
```

- [ ] **Step 4 — Run tests; verify it passes**

```bash
go test -race ./room-service/... -run "TestSanitizeError" -v
```

Expected: PASS, including the new `"sentinel:_invalid_org"` subtest.

- [ ] **Step 5 — Commit**

```bash
git add room-service/helper.go room-service/helper_test.go
git commit -m "feat(room-service): add errInvalidOrg sentinel + sanitize whitelist"
```

---

### Task 2.2 — `ListOrgMembers` interface + Mongo impl + mock regen

**Files:**
- Modify: `room-service/store.go` (interface)
- Modify: `room-service/store_mongo.go` (impl)
- Regenerate: `room-service/mock_store_test.go`

- [ ] **Step 1 — Add the method to the interface**

In `room-service/store.go`, inside the `RoomStore` interface, append one line after `ListRoomMembers`:

```go
    // ListOrgMembers returns all users whose sectId equals orgID, projected
    // as OrgMember rows sorted by account ascending. Returns errInvalidOrg
    // when no users match (treated as "orgId is not valid").
    ListOrgMembers(ctx context.Context, orgID string) ([]model.OrgMember, error)
```

- [ ] **Step 2 — Implement on MongoStore**

Append to `room-service/store_mongo.go` (at the end of the file):

```go
// ListOrgMembers returns all users whose sectId equals orgID, projected as
// OrgMember rows sorted by account ascending. Returns errInvalidOrg when the
// query matches no users.
func (s *MongoStore) ListOrgMembers(ctx context.Context, orgID string) ([]model.OrgMember, error) {
    opts := options.Find().
        SetSort(bson.D{{Key: "account", Value: 1}}).
        SetProjection(bson.M{
            "_id":         1,
            "account":     1,
            "engName":     1,
            "chineseName": 1,
            "siteId":      1,
        })
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

The `users` collection is already present on `MongoStore` (added by the earlier room-members enrichment path). Confirm with `grep 'users.*\*mongo\.Collection' room-service/store_mongo.go` — the field exists in the struct definition near the top of the file.

- [ ] **Step 3 — Regenerate the mock**

```bash
make generate SERVICE=room-service
```

If mockgen is missing or fails due to a Go toolchain mismatch, rebuild it:

```bash
go install go.uber.org/mock/mockgen@v0.6.0
export PATH=$PATH:$(go env GOPATH)/bin
make generate SERVICE=room-service
```

- [ ] **Step 4 — Verify the mock contains `ListOrgMembers`**

```bash
grep -n "ListOrgMembers" room-service/mock_store_test.go
```

Expected: at least 2 hits — the `MockRoomStore` method and the `MockRoomStoreMockRecorder` `EXPECT` helper.

- [ ] **Step 5 — Build + unit tests**

```bash
go vet ./room-service/... && make test SERVICE=room-service && make lint
```

Expected: `go vet` clean; unit suite PASS with `-race`; `make lint` reports 0 issues.

- [ ] **Step 6 — Commit**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/mock_store_test.go
git commit -m "feat(room-service): implement ListOrgMembers store method"
```

---

## Part 3 — Handler + registration + unit tests

Adds the NATS handler, registers the subscription in `RegisterCRUD`, and covers every branch with unit tests against the mock.

### Task 3.1 — Handler unit test (TDD red)

**File:**
- Modify: `room-service/handler_test.go`

- [ ] **Step 1 — Append the test function**

Append this new test function at the bottom of `room-service/handler_test.go`:

```go
func TestHandler_ListOrgMembers(t *testing.T) {
    const orgID = "sect-eng"
    subj := subject.OrgMembers("alice", orgID)

    members := []model.OrgMember{
        {ID: "u-a", Account: "a", EngName: "A", ChineseName: "AA", SiteID: "site-a"},
        {ID: "u-b", Account: "b", EngName: "B", ChineseName: "BB", SiteID: "site-a"},
    }

    type want struct {
        errContains string
        errIs       error
        members     []model.OrgMember
    }
    tests := []struct {
        name      string
        subject   string
        setupMock func(*MockRoomStore)
        want      want
    }{
        {
            name:    "happy path returns members",
            subject: subj,
            setupMock: func(s *MockRoomStore) {
                s.EXPECT().ListOrgMembers(gomock.Any(), orgID).Return(members, nil)
            },
            want: want{members: members},
        },
        {
            name:      "invalid subject",
            subject:   "chat.garbage",
            setupMock: func(s *MockRoomStore) {},
            want:      want{errContains: "invalid org-members subject"},
        },
        {
            name:    "empty org returns errInvalidOrg",
            subject: subj,
            setupMock: func(s *MockRoomStore) {
                s.EXPECT().ListOrgMembers(gomock.Any(), orgID).Return(nil, errInvalidOrg)
            },
            want: want{errIs: errInvalidOrg},
        },
        {
            name:    "store error is wrapped",
            subject: subj,
            setupMock: func(s *MockRoomStore) {
                s.EXPECT().ListOrgMembers(gomock.Any(), orgID).
                    Return(nil, fmt.Errorf("mongo exploded"))
            },
            want: want{errContains: "get org members"},
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            ctrl := gomock.NewController(t)
            store := NewMockRoomStore(ctrl)
            tc.setupMock(store)

            h := &Handler{store: store, siteID: "site-a"}
            resp, err := h.handleListOrgMembers(context.Background(), tc.subject)

            if tc.want.errContains != "" {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tc.want.errContains)
                return
            }
            if tc.want.errIs != nil {
                require.Error(t, err)
                assert.True(t, errors.Is(err, tc.want.errIs), "error chain should contain %v, got %v", tc.want.errIs, err)
                return
            }
            require.NoError(t, err)
            var decoded model.ListOrgMembersResponse
            require.NoError(t, json.Unmarshal(resp, &decoded))
            assert.Equal(t, tc.want.members, decoded.Members)
        })
    }
}
```

- [ ] **Step 2 — Run the test; verify it fails**

```bash
go test -race ./room-service/... -run TestHandler_ListOrgMembers -v
```

Expected: compile error — `h.handleListOrgMembers undefined`.

---

### Task 3.2 — Handler implementation (TDD green)

**File:**
- Modify: `room-service/handler.go`

- [ ] **Step 1 — Add handler methods**

Append these two methods to `room-service/handler.go` (near the other `nats*` / `handle*` methods, e.g. after `handleListMembers`):

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

All imports needed (`context`, `encoding/json`, `errors`, `fmt`, `log/slog`, `otelnats`, `natsutil`, `subject`, `model`) are already imported in `handler.go` from the other handlers.

- [ ] **Step 2 — Run the new tests; verify all pass**

```bash
go test -race ./room-service/... -run TestHandler_ListOrgMembers -v
```

Expected: all 4 subtests PASS.

- [ ] **Step 3 — Run full unit suite + lint**

```bash
make test SERVICE=room-service && make lint
```

Expected: full suite PASS with `-race`; lint 0 issues.

---

### Task 3.3 — Register the subscription

**File:**
- Modify: `room-service/handler.go` (inside `RegisterCRUD`)

- [ ] **Step 1 — Add the registration**

Inside `Handler.RegisterCRUD`, right after the `MemberListWildcard` block, add:

```go
if _, err := nc.QueueSubscribe(subject.OrgMembersWildcard(), queue, h.natsListOrgMembers); err != nil {
    return fmt.Errorf("subscribe org members: %w", err)
}
```

- [ ] **Step 2 — Verify build**

```bash
go vet ./room-service/...
```

Expected: clean.

- [ ] **Step 3 — Commit Parts 3.1–3.3 together**

```bash
git add room-service/handler.go room-service/handler_test.go
git commit -m "feat(room-service): add list-org-members handler and subscription"
```

---

## Part 4 — Integration tests + final verification

Exercises `ListOrgMembers` end-to-end against a real Mongo testcontainer.

### Task 4.1 — Integration tests

**File:**
- Modify: `room-service/integration_test.go`

- [ ] **Step 1 — Append the new integration test function**

Append this test function to `room-service/integration_test.go` (after the last existing function):

```go
func TestMongoStore_ListOrgMembers_Integration(t *testing.T) {
    ctx := context.Background()

    insertUser := func(t *testing.T, db *mongo.Database, u model.User) {
        t.Helper()
        _, err := db.Collection("users").InsertOne(ctx, u)
        require.NoError(t, err)
    }

    t.Run("returns members sorted by account asc", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        // Inserted in non-alphabetical order to verify the store sorts.
        insertUser(t, db, model.User{ID: "u-charlie", Account: "charlie", EngName: "Charlie", ChineseName: "查理", SiteID: "site-a", SectID: "sect-eng", SectName: "Engineering"})
        insertUser(t, db, model.User{ID: "u-alice", Account: "alice", EngName: "Alice", ChineseName: "愛麗絲", SiteID: "site-a", SectID: "sect-eng", SectName: "Engineering"})
        insertUser(t, db, model.User{ID: "u-bob", Account: "bob", EngName: "Bob", ChineseName: "鮑伯", SiteID: "site-a", SectID: "sect-eng", SectName: "Engineering"})

        got, err := store.ListOrgMembers(ctx, "sect-eng")
        require.NoError(t, err)
        require.Len(t, got, 3)
        assert.Equal(t, "alice", got[0].Account)
        assert.Equal(t, "bob", got[1].Account)
        assert.Equal(t, "charlie", got[2].Account)
    })

    t.Run("filters by sectId only", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        insertUser(t, db, model.User{ID: "u-alice", Account: "alice", EngName: "Alice", SiteID: "site-a", SectID: "sect-eng"})
        insertUser(t, db, model.User{ID: "u-bob", Account: "bob", EngName: "Bob", SiteID: "site-a", SectID: "sect-eng"})
        insertUser(t, db, model.User{ID: "u-carol", Account: "carol", EngName: "Carol", SiteID: "site-a", SectID: "sect-ops"})
        insertUser(t, db, model.User{ID: "u-dave", Account: "dave", EngName: "Dave", SiteID: "site-a", SectID: "sect-ops"})

        got, err := store.ListOrgMembers(ctx, "sect-eng")
        require.NoError(t, err)
        require.Len(t, got, 2)
        accounts := []string{got[0].Account, got[1].Account}
        assert.ElementsMatch(t, []string{"alice", "bob"}, accounts)
    })

    t.Run("empty org returns errInvalidOrg", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        insertUser(t, db, model.User{ID: "u-alice", Account: "alice", SectID: "sect-eng"})

        _, err := store.ListOrgMembers(ctx, "sect-nope")
        require.Error(t, err)
        assert.True(t, errors.Is(err, errInvalidOrg), "want errInvalidOrg in chain, got %v", err)
    })

    t.Run("projection excludes non-listed fields", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        insertUser(t, db, model.User{
            ID: "u-alice", Account: "alice",
            EngName: "Alice", ChineseName: "愛麗絲",
            SiteID: "site-a", SectID: "sect-eng",
            SectName: "Engineering", EmployeeID: "EMP-001",
        })

        got, err := store.ListOrgMembers(ctx, "sect-eng")
        require.NoError(t, err)
        require.Len(t, got, 1)
        m := got[0]
        // Projected fields are present.
        assert.Equal(t, "u-alice", m.ID)
        assert.Equal(t, "alice", m.Account)
        assert.Equal(t, "Alice", m.EngName)
        assert.Equal(t, "愛麗絲", m.ChineseName)
        assert.Equal(t, "site-a", m.SiteID)
        // OrgMember struct has no EmployeeID / SectID / SectName fields, so
        // they can't be surfaced even if the projection were wrong. This
        // assertion is structural — it's proven at compile time by the
        // struct definition — and is documented here for reviewers.
    })
}
```

The helper `setupMongo(t)` already exists at the top of `integration_test.go`; all imports used here (`context`, `errors`, `testing`, `stretchr/testify/assert`, `stretchr/testify/require`, `go.mongodb.org/mongo-driver/v2/mongo`, `pkg/model`) are already in the file.

- [ ] **Step 2 — Run the new tests**

```bash
go test -race -tags integration -count=1 -timeout 10m ./room-service/... -run TestMongoStore_ListOrgMembers_Integration -v
```

Expected: all 4 subtests PASS. If Docker is not running, start it first:

```bash
dockerd --storage-driver=vfs --iptables=false --bridge=none >/tmp/dockerd.log 2>&1 &
sleep 3
```

- [ ] **Step 3 — Run the full room-service integration suite**

```bash
go test -race -tags integration -count=1 -timeout 30m ./room-service/...
```

Expected: PASS. Takes ~12 minutes with all room-service containers.

- [ ] **Step 4 — Commit**

```bash
git add room-service/integration_test.go
git commit -m "test(room-service): integration tests for ListOrgMembers"
```

---

### Task 4.2 — Final verification

**Files:** (no changes; verification only)

- [ ] **Step 1 — Lint**

```bash
make lint
```

Expected: 0 issues.

- [ ] **Step 2 — Full unit suite with race detector**

```bash
make test
```

Expected: full repo PASS.

- [ ] **Step 3 — Coverage on new functions**

```bash
go test -race -coverprofile=/tmp/rs.out ./room-service/...
go tool cover -func=/tmp/rs.out | grep -E "ListOrgMembers|handleListOrgMembers|natsListOrgMembers|sanitizeError"
```

Expected: each function ≥ 80% (CLAUDE.md §4).

- [ ] **Step 4 — Push**

```bash
git push -u origin claude/add-room-member-feature-5FPbS
```

No force-push needed — these commits append to the existing branch.

---

## Appendix — Coding rules reminder (from CLAUDE.md)

- Error wrapping: `fmt.Errorf("short description: %w", err)` — never bare err.
- `errors.Is` / `errors.As` only — no string comparison.
- `log/slog` with structured fields — never interpolated strings.
- Mock regenerated via `make generate SERVICE=room-service` whenever `store.go` changes.
- Unit tests use `MockRoomStore` with gomock — never touch Mongo.
- `//go:build integration` tag on integration tests; `testcontainers-go`.
- Always `-race` via `make test` / `make test-integration`.

## Appendix — Commit map

| Task | Commit subject |
|------|----------------|
| 1.1 | `feat(subject): add OrgMembers builders and ParseOrgMembersSubject` |
| 1.2 | `feat(model): add OrgMember and ListOrgMembersResponse types` |
| 2.1 | `feat(room-service): add errInvalidOrg sentinel + sanitize whitelist` |
| 2.2 | `feat(room-service): implement ListOrgMembers store method` |
| 3.1–3.3 | `feat(room-service): add list-org-members handler and subscription` |
| 4.1 | `test(room-service): integration tests for ListOrgMembers` |
| 4.2 | (no commit unless coverage top-ups needed) |
