# `listRoomMembers` Extra Enrichment Fields — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add four enrich-only display fields to the `member.list` RPC — `orgDescription` on org rows and `sectName` / `accountName` / `employeeId` on individual rows.

**Architecture:** All four fields ride the existing `enrich=true` machinery in `room-service`. The individual fields come from the `users` join (room_members path) and the `users` batch (subscriptions fallback); `accountName` is a Go `strings.ToUpper(account)` transform; `orgDescription` is resolved dept-first in the `orgdisplay.go` rollup from two new denormalized `users` fields. No new index, no extra round-trip, no store-interface change.

**Tech Stack:** Go 1.25, MongoDB (`mongo-driver/v2`), `stretchr/testify`, `go.uber.org/mock`, testcontainers.

**Spec:** `docs/superpowers/specs/2026-06-25-listroommembers-extra-fields-design.md`

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `pkg/model/user.go` | `User` domain type | Add `SectDescription`, `DeptDescription` (plain json+bson) |
| `pkg/model/member.go` | `RoomMemberEntry` wire type | Add `SectName`, `AccountName`, `EmployeeID`, `OrgDescription` (`bson:"-"`, `json:",omitempty"`) |
| `pkg/model/model_test.go` | Model round-trip tests | Extend User + RoomMemberEntry display-field tests |
| `room-service/orgdisplay.go` | Org-member rollup | Add description fields to structs, roll up in `buildOrgDisplay`, new `orgDisplayDescription` |
| `room-service/orgdisplay_test.go` | Rollup unit tests | `TestOrgDisplayDescription` + `buildOrgDisplay` description cases |
| `room-service/store_mongo.go` | Mongo store | Enrich-stage projections, decode loop, `attachUserDisplayNames`, `findUsersForDisplay`, `fetchOrgDisplayUsers`, `attachOrgDisplay`, `strings` import |
| `room-service/integration_test.go` | Store integration tests | Extend `_Enrich_` + bot tests; new `_OrgDescription_` test |
| `room-service/handler_test.go` | Handler unit tests | Extend `member.list` enrich-passthrough case |
| `docs/client-api.md` | Client API reference | `RoomMemberEntry` table + JSON example |

**Test commands** (CLAUDE.md mandates `make`):
- Model package: `make test SERVICE=pkg/model`
- room-service unit: `make test SERVICE=room-service`
- room-service integration (Docker): `make test-integration SERVICE=room-service`

**Comment rule (user directive):** every comment you write or touch must be ≤ 2 lines. The final task runs `/simplify` and a comment-length sweep.

---

### Task 1: Add description fields to the `User` model

**Files:**
- Modify: `pkg/model/user.go:18-23`
- Test: `pkg/model/model_test.go:32-40`

- [ ] **Step 1: Extend the failing test**

In `pkg/model/model_test.go`, replace `TestUserJSON_WithSectAndDept` (lines 32-40) with:

```go
func TestUserJSON_WithSectAndDept(t *testing.T) {
	u := model.User{
		ID: "u1", Account: "alice", SiteID: "site-a",
		SectID: "S", SectName: "Sect", SectTCName: "部", SectDescription: "Sect desc",
		DeptID: "D", DeptName: "Dept", DeptTCName: "處", DeptDescription: "Dept desc",
		EngName: "Alice", ChineseName: "爱丽丝",
	}
	roundTrip(t, &u, &model.User{})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — compile error `unknown field 'SectDescription' in struct literal of type model.User`.

- [ ] **Step 3: Add the fields**

In `pkg/model/user.go`, add `SectDescription` after `SectTCName` and `DeptDescription` after `DeptTCName` so the block reads:

```go
	SectID          string     `json:"sectId"          bson:"sectId"`
	SectName        string     `json:"sectName"        bson:"sectName"`
	SectTCName      string     `json:"sectTCName"      bson:"sectTCName"`
	SectDescription string     `json:"sectDescription" bson:"sectDescription"`
	DeptID          string     `json:"deptId"          bson:"deptId"`
	DeptName        string     `json:"deptName"        bson:"deptName"`
	DeptTCName      string     `json:"deptTCName"      bson:"deptTCName"`
	DeptDescription string     `json:"deptDescription" bson:"deptDescription"`
```

(Run `make fmt` later to settle struct-tag alignment.)

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/user.go pkg/model/model_test.go
git commit -m "feat(model): add Sect/DeptDescription to User"
```

---

### Task 2: Add the four display fields to `RoomMemberEntry`

**Files:**
- Modify: `pkg/model/member.go:49-65`
- Test: `pkg/model/model_test.go:1573-1682`

- [ ] **Step 1: Extend the failing tests**

In `pkg/model/model_test.go`, replace `TestRoomMemberEntry_DisplayFields_JSON` (lines 1573-1590) with:

```go
func TestRoomMemberEntry_DisplayFields_JSON(t *testing.T) {
	entry := model.RoomMemberEntry{
		ID: "u1", Type: model.RoomMemberIndividual, Account: "alice",
		EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
		SectName: "Cardiology", AccountName: "ALICE", EmployeeID: "E10293",
	}
	data, err := json.Marshal(&entry)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "u1", got["id"])
	assert.Equal(t, "individual", got["type"])
	assert.Equal(t, "alice", got["account"])
	assert.Equal(t, "Alice Wang", got["engName"])
	assert.Equal(t, "愛麗絲", got["chineseName"])
	assert.Equal(t, true, got["isOwner"])
	assert.Equal(t, "Cardiology", got["sectName"])
	assert.Equal(t, "ALICE", got["accountName"])
	assert.Equal(t, "E10293", got["employeeId"])
}
```

Replace `TestRoomMemberEntry_DisplayFields_OmittedWhenZero` (lines 1592-1604) with:

```go
func TestRoomMemberEntry_DisplayFields_OmittedWhenZero(t *testing.T) {
	entry := model.RoomMemberEntry{
		ID: "u1", Type: model.RoomMemberIndividual, Account: "alice",
	}
	data, err := json.Marshal(&entry)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	for _, k := range []string{"engName", "chineseName", "name", "isOwner", "orgName", "memberCount", "sectName", "accountName", "employeeId", "orgDescription"} {
		_, present := got[k]
		assert.False(t, present, "display field %q should be omitted when zero", k)
	}
}
```

Replace `TestRoomMemberEntry_DisplayFields_NotPersistedToBSON` (lines 1606-1622) with:

```go
func TestRoomMemberEntry_DisplayFields_NotPersistedToBSON(t *testing.T) {
	entry := model.RoomMemberEntry{
		ID: "org-1", Type: model.RoomMemberOrg,
		OrgName: "Engineering", MemberCount: 42, OrgDescription: "Eng dept",
		SectName: "Cardiology", AccountName: "ALICE", EmployeeID: "E10293",
	}
	data, err := bson.Marshal(&entry)
	require.NoError(t, err)

	var got bson.M
	require.NoError(t, bson.Unmarshal(data, &got))
	assert.Equal(t, "org-1", got["id"])
	assert.Equal(t, "org", got["type"])
	for _, k := range []string{"engName", "chineseName", "name", "isOwner", "orgName", "memberCount", "sectName", "accountName", "employeeId", "orgDescription"} {
		_, present := got[k]
		assert.False(t, present, "display field %q must not be persisted to BSON", k)
	}
}
```

In `TestRoomMemberEntry_BotName_RoundTrip`, replace the first subtest body (lines 1625-1647, `"bot member name round-trips via JSON"`) with:

```go
	t.Run("bot member name round-trips via JSON", func(t *testing.T) {
		entry := model.RoomMemberEntry{
			ID:          "u-bot",
			Type:        model.RoomMemberIndividual,
			Account:     "weather.bot",
			Name:        "Weather App",
			AccountName: "WEATHER.BOT",
		}
		data, err := json.Marshal(&entry)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		assert.Equal(t, "Weather App", raw["name"])
		assert.Equal(t, "WEATHER.BOT", raw["accountName"])
		_, hasEngName := raw["engName"]
		assert.False(t, hasEngName, "engName must be absent for bot entry")
		_, hasChineseName := raw["chineseName"]
		assert.False(t, hasChineseName, "chineseName must be absent for bot entry")

		var dst model.RoomMemberEntry
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, entry, dst)
	})
```

And replace the `"orgName round-trips via JSON"` subtest (lines 1664-1681) with:

```go
	t.Run("orgName and orgDescription round-trip via JSON", func(t *testing.T) {
		entry := model.RoomMemberEntry{
			ID:             "sect-eng",
			Type:           model.RoomMemberOrg,
			OrgName:        "Engineering",
			OrgDescription: "Eng dept",
		}
		data, err := json.Marshal(&entry)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		assert.Equal(t, "Engineering", raw["orgName"])
		assert.Equal(t, "Eng dept", raw["orgDescription"])
		_, hasSectName := raw["sectName"]
		assert.False(t, hasSectName, "sectName absent on an org entry")

		var dst model.RoomMemberEntry
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, entry, dst)
	})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — compile error `unknown field 'SectName' in struct literal of type model.RoomMemberEntry`.

- [ ] **Step 3: Add the fields**

In `pkg/model/member.go`, after the `MemberCount` field (line 64), inside the display-fields block, add:

```go
	// Individual extras (enrich=true): section, uppercased account, employee id.
	SectName    string `json:"sectName,omitempty"    bson:"-"`
	AccountName string `json:"accountName,omitempty" bson:"-"`
	EmployeeID  string `json:"employeeId,omitempty"  bson:"-"`
	// Org extra (enrich=true): description, dept-first.
	OrgDescription string `json:"orgDescription,omitempty" bson:"-"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/member.go pkg/model/model_test.go
git commit -m "feat(model): add sectName/accountName/employeeId/orgDescription to RoomMemberEntry"
```

---

### Task 3: Resolve `orgDescription` in the org rollup

**Files:**
- Modify: `room-service/orgdisplay.go:7-26` (structs), `:42-85` (`buildOrgDisplay`), add `orgDisplayDescription` after `:104`
- Modify: `room-service/store_mongo.go:606-632` (`fetchOrgDisplayUsers` projection), `:582-599` (`attachOrgDisplay`)
- Test: `room-service/orgdisplay_test.go` (unit), `room-service/integration_test.go` (new test)

- [ ] **Step 1: Write the failing unit tests**

In `room-service/orgdisplay_test.go`, add a new test function after `TestOrgDisplaySectName`:

```go
func TestOrgDisplayDescription(t *testing.T) {
	t.Run("nil aggregate yields empty", func(t *testing.T) {
		assert.Empty(t, orgDisplayDescription(nil))
	})

	t.Run("dept description wins when dept match present", func(t *testing.T) {
		a := &orgDisplayAgg{isDept: true, deptDescription: "Dept desc", sectDescription: "Sect desc"}
		assert.Equal(t, "Dept desc", orgDisplayDescription(a))
	})

	t.Run("empty dept description falls through to sect", func(t *testing.T) {
		a := &orgDisplayAgg{isDept: true, deptDescription: "", sectDescription: "Sect desc"}
		assert.Equal(t, "Sect desc", orgDisplayDescription(a))
	})

	t.Run("sect-only org uses sect description", func(t *testing.T) {
		a := &orgDisplayAgg{isDept: false, sectDescription: "Sect desc"}
		assert.Equal(t, "Sect desc", orgDisplayDescription(a))
	})

	t.Run("all empty yields empty (no orgID fallback)", func(t *testing.T) {
		a := &orgDisplayAgg{isDept: true}
		assert.Empty(t, orgDisplayDescription(a))
	})
}
```

And inside `TestBuildOrgDisplay`, add two subtests before its closing brace (after line 124):

```go
	t.Run("descriptions roll up alongside names", func(t *testing.T) {
		users := []orgDisplayUser{
			{DeptID: "X", DeptName: "Eng Dept", DeptDescription: "Dept desc"},
			{SectID: "X", SectName: "Eng Sect", SectDescription: "Sect desc"},
		}
		got := buildOrgDisplay([]string{"X"}, users)
		a := got["X"]
		require.NotNil(t, a)
		assert.Equal(t, "Dept desc", a.deptDescription)
		assert.Equal(t, "Sect desc", a.sectDescription)
	})

	t.Run("description uses lexicographic max within a branch", func(t *testing.T) {
		users := []orgDisplayUser{
			{SectID: "X", SectDescription: "Alpha"},
			{SectID: "X", SectDescription: "Zeta"},
		}
		got := buildOrgDisplay([]string{"X"}, users)
		require.NotNil(t, got["X"])
		assert.Equal(t, "Zeta", got["X"].sectDescription)
	})
```

- [ ] **Step 2: Run unit tests to verify they fail**

Run: `make test SERVICE=room-service`
Expected: FAIL — compile error `unknown field 'deptDescription' in struct literal of type main.orgDisplayUser` and `undefined: orgDisplayDescription`.

- [ ] **Step 3: Add struct fields**

In `room-service/orgdisplay.go`, replace `orgDisplayUser` (lines 7-14) with:

```go
type orgDisplayUser struct {
	DeptID          string `bson:"deptId"`
	SectID          string `bson:"sectId"`
	DeptName        string `bson:"deptName"`
	DeptTCName      string `bson:"deptTCName"`
	SectName        string `bson:"sectName"`
	SectTCName      string `bson:"sectTCName"`
	DeptDescription string `bson:"deptDescription"`
	SectDescription string `bson:"sectDescription"`
}
```

Replace `orgDisplayAgg` (lines 19-26) with:

```go
type orgDisplayAgg struct {
	isDept          bool
	deptName        string
	deptTCName      string
	sectName        string
	sectTCName      string
	deptDescription string
	sectDescription string
	memberCount     int
}
```

- [ ] **Step 4: Roll descriptions up in `buildOrgDisplay`**

In the dept-match branch (after the `DeptTCName` block, currently lines 67-69), add:

```go
			if u.DeptDescription > a.deptDescription {
				a.deptDescription = u.DeptDescription
			}
```

In the sect-match branch (after the `SectTCName` block, currently lines 79-81), add:

```go
			if u.SectDescription > a.sectDescription {
				a.sectDescription = u.SectDescription
			}
```

- [ ] **Step 5: Add the `orgDisplayDescription` helper**

In `room-service/orgdisplay.go`, after `orgDisplaySectName` (after line 104), add:

```go
// orgDisplayDescription renders an org's description with the same dept-first
// tiebreak as orgDisplaySectName, but with an empty fallback (omitted on the wire).
func orgDisplayDescription(a *orgDisplayAgg) string {
	if a == nil {
		return ""
	}
	if a.isDept && a.deptDescription != "" {
		return a.deptDescription
	}
	return a.sectDescription
}
```

- [ ] **Step 6: Run unit tests to verify they pass**

Run: `make test SERVICE=room-service`
Expected: PASS.

- [ ] **Step 7: Wire the projection and attach the field**

In `room-service/store_mongo.go`, in `fetchOrgDisplayUsers` projection (lines 612-620), add two keys:

```go
		options.Find().SetProjection(bson.M{
			"_id":             0,
			"deptId":          1,
			"sectId":          1,
			"deptName":        1,
			"deptTCName":      1,
			"sectName":        1,
			"sectTCName":      1,
			"deptDescription": 1,
			"sectDescription": 1,
		}),
```

In `attachOrgDisplay`, after the `OrgName` assignment (line 596), add:

```go
		members[i].Member.OrgDescription = orgDisplayDescription(agg[id])
```

- [ ] **Step 8: Write the failing integration test**

In `room-service/integration_test.go`, add a new test function (place it after `TestMongoStore_ListRoomMembers_OrgDisplay_FallbackToOrgId_Integration`, line 2684):

```go
func TestMongoStore_ListRoomMembers_OrgDescription_Integration(t *testing.T) {
	ctx := context.Background()

	t.Run("dept-first description wins", func(t *testing.T) {
		db := setupMongo(t)
		store := NewMongoStore(db)
		require.NoError(t, store.EnsureIndexes(ctx))

		const roomID = "room-1"
		_, err := db.Collection("users").InsertOne(ctx, model.User{
			ID: "u_alice", Account: "alice", SiteID: "site-a",
			DeptID: "X", DeptName: "Engineering", DeptDescription: "Dept desc",
		})
		require.NoError(t, err)
		_, err = db.Collection("users").InsertOne(ctx, model.User{
			ID: "u_bob", Account: "bob", SiteID: "site-a",
			SectID: "X", SectName: "Sect", SectDescription: "Sect desc",
		})
		require.NoError(t, err)
		_, err = db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: idgen.GenerateUUIDv7(), RoomID: roomID,
			Member: model.RoomMemberEntry{ID: "X", Type: model.RoomMemberOrg},
		})
		require.NoError(t, err)

		got, err := store.ListRoomMembers(ctx, roomID, nil, nil, true)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "Dept desc", got[0].Member.OrgDescription)
	})

	t.Run("sect-only org uses sect description", func(t *testing.T) {
		db := setupMongo(t)
		store := NewMongoStore(db)
		require.NoError(t, store.EnsureIndexes(ctx))

		const roomID = "room-2"
		_, err := db.Collection("users").InsertOne(ctx, model.User{
			ID: "u_c", Account: "c", SiteID: "site-a",
			SectID: "S", SectName: "Sect", SectDescription: "Sect desc",
		})
		require.NoError(t, err)
		_, err = db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: idgen.GenerateUUIDv7(), RoomID: roomID,
			Member: model.RoomMemberEntry{ID: "S", Type: model.RoomMemberOrg},
		})
		require.NoError(t, err)

		got, err := store.ListRoomMembers(ctx, roomID, nil, nil, true)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "Sect desc", got[0].Member.OrgDescription)
	})

	t.Run("missing description omitted", func(t *testing.T) {
		db := setupMongo(t)
		store := NewMongoStore(db)
		require.NoError(t, store.EnsureIndexes(ctx))

		const roomID = "room-3"
		_, err := db.Collection("users").InsertOne(ctx, model.User{
			ID: "u_d", Account: "d", SiteID: "site-a",
			SectID: "S2", SectName: "Sect2",
		})
		require.NoError(t, err)
		_, err = db.Collection("room_members").InsertOne(ctx, model.RoomMember{
			ID: idgen.GenerateUUIDv7(), RoomID: roomID,
			Member: model.RoomMemberEntry{ID: "S2", Type: model.RoomMemberOrg},
		})
		require.NoError(t, err)

		got, err := store.ListRoomMembers(ctx, roomID, nil, nil, true)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Empty(t, got[0].Member.OrgDescription)
		assert.Equal(t, "Sect2", got[0].Member.OrgName)
	})
}
```

- [ ] **Step 9: Run the integration test to verify it passes**

Run: `make test-integration SERVICE=room-service`
Expected: PASS (`TestMongoStore_ListRoomMembers_OrgDescription_Integration` green).

- [ ] **Step 10: Commit**

```bash
git add room-service/orgdisplay.go room-service/orgdisplay_test.go room-service/store_mongo.go room-service/integration_test.go
git commit -m "feat(room-service): resolve orgDescription dept-first in org rollup"
```

---

### Task 4: Individual extras via the `room_members` path

**Files:**
- Modify: `room-service/store_mongo.go:3-18` (imports), `:646-650` (`roomMemberEnrichedDisplay`), `:658-712` (`enrichRoomMembersStages`), `:556-568` (`getRoomMembers` decode loop)
- Test: `room-service/integration_test.go:648-676`

- [ ] **Step 1: Extend the failing integration test**

In `room-service/integration_test.go`, replace the `"individual enrichment via room_members path"` subtest (lines 648-676) with:

```go
	t.Run("individual enrichment via room_members path", func(t *testing.T) {
		db := setupMongo(t)
		store := NewMongoStore(db)
		base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

		insertUser(t, db, model.User{
			ID: "u-alice", Account: "alice", SiteID: "site-a",
			EngName: "Alice Wang", ChineseName: "愛麗絲",
			SectName: "Cardiology", EmployeeID: "E10293",
		})
		insertSub(t, db, model.Subscription{
			ID: "sub-alice", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
			RoomID: "r1", SiteID: "site-a",
			Roles: []model.Role{model.RoleOwner}, JoinedAt: base,
		})
		insertRM(t, db, model.RoomMember{
			ID: "rm-alice", RoomID: "r1", Ts: base,
			Member: model.RoomMemberEntry{ID: "u-alice", Type: model.RoomMemberIndividual, Account: "alice"},
		})

		got, err := store.ListRoomMembers(ctx, "r1", nil, nil, true)
		require.NoError(t, err)
		require.Len(t, got, 1)
		m := got[0].Member
		assert.Equal(t, "Alice Wang", m.EngName)
		assert.Equal(t, "愛麗絲", m.ChineseName)
		assert.True(t, m.IsOwner)
		assert.Equal(t, "Cardiology", m.SectName)
		assert.Equal(t, "ALICE", m.AccountName)
		assert.Equal(t, "E10293", m.EmployeeID)
		assert.Empty(t, m.OrgName)
		assert.Zero(t, m.MemberCount)
	})
```

- [ ] **Step 2: Run the integration test to verify it fails**

Run: `make test-integration SERVICE=room-service`
Expected: FAIL — `m.SectName` is `""` (not yet enriched), assertion `"Cardiology"` fails.

- [ ] **Step 3: Add `strings` to the import block**

In `room-service/store_mongo.go`, add `"strings"` to the standard-library import group (after `"regexp"`, line 8):

```go
	"regexp"
	"strings"
	"time"
```

- [ ] **Step 4: Add decode fields to `roomMemberEnrichedDisplay`**

Replace the struct (lines 646-650) with:

```go
type roomMemberEnrichedDisplay struct {
	EngName     string `bson:"engName,omitempty"`
	ChineseName string `bson:"chineseName,omitempty"`
	IsOwner     bool   `bson:"isOwner,omitempty"`
	SectName    string `bson:"sectName,omitempty"`
	EmployeeID  string `bson:"employeeId,omitempty"`
}
```

- [ ] **Step 5: Project + set the new fields in `enrichRoomMembersStages`**

In the `_userMatch` `$lookup` inner `$project` (line 673), add `sectName` and `employeeId`:

```go
				bson.M{"$project": bson.M{"engName": 1, "chineseName": 1, "sectName": 1, "employeeId": 1, "_id": 0}},
```

In the `$set` "display" sub-document (lines 696-708), add two entries after `chineseName`:

```go
				"sectName":    bson.M{"$arrayElemAt": bson.A{"$_userMatch.sectName", 0}},
				"employeeId":  bson.M{"$arrayElemAt": bson.A{"$_userMatch.employeeId", 0}},
```

- [ ] **Step 6: Copy the fields in the `getRoomMembers` decode loop**

Replace the decode loop (lines 558-568) with:

```go
	for i := range rows {
		rm := rows[i].RoomMember
		d := rows[i].Display
		rm.Member.EngName = d.EngName
		rm.Member.ChineseName = d.ChineseName
		rm.Member.IsOwner = d.IsOwner
		if rm.Member.Type == model.RoomMemberOrg {
			orgIDs = append(orgIDs, rm.Member.ID)
		} else {
			rm.Member.SectName = d.SectName
			rm.Member.EmployeeID = d.EmployeeID
			rm.Member.AccountName = strings.ToUpper(rm.Member.Account)
		}
		members[i] = rm
	}
```

- [ ] **Step 7: Run the integration test to verify it passes**

Run: `make test-integration SERVICE=room-service`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add room-service/store_mongo.go room-service/integration_test.go
git commit -m "feat(room-service): enrich individual sectName/accountName/employeeId on room_members path"
```

---

### Task 5: Individual extras via the subscriptions fallback

**Files:**
- Modify: `room-service/store_mongo.go:823-842` (`findUsersForDisplay`), `:803-816` (`attachUserDisplayNames` loop)
- Test: `room-service/integration_test.go:727-755` (fallback), `:757-781` (enrich=false), `:992-993` (bot)

- [ ] **Step 1: Extend the failing integration tests**

In `room-service/integration_test.go`, replace the `"enrichment via subscriptions fallback"` subtest (lines 727-755) with:

```go
	t.Run("enrichment via subscriptions fallback", func(t *testing.T) {
		db := setupMongo(t)
		store := NewMongoStore(db)
		base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

		insertUser(t, db, model.User{ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲", SectName: "Cardiology", EmployeeID: "E10293"})
		insertUser(t, db, model.User{ID: "u-bob", Account: "bob", EngName: "Bob", ChineseName: "鮑伯"})
		insertSub(t, db, model.Subscription{
			ID: "sub-a", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
			RoomID: "r1", Roles: []model.Role{model.RoleOwner}, JoinedAt: base.Add(10 * time.Second),
		})
		insertSub(t, db, model.Subscription{
			ID: "sub-b", User: model.SubscriptionUser{ID: "u-bob", Account: "bob"},
			RoomID: "r1", Roles: []model.Role{model.RoleMember}, JoinedAt: base.Add(20 * time.Second),
		})

		got, err := store.ListRoomMembers(ctx, "r1", nil, nil, true)
		require.NoError(t, err)
		require.Len(t, got, 2)

		alice, bob := got[0].Member, got[1].Member
		assert.Equal(t, "alice", alice.Account)
		assert.Equal(t, "Alice Wang", alice.EngName)
		assert.True(t, alice.IsOwner)
		assert.Equal(t, "Cardiology", alice.SectName)
		assert.Equal(t, "ALICE", alice.AccountName)
		assert.Equal(t, "E10293", alice.EmployeeID)
		assert.Equal(t, "bob", bob.Account)
		assert.Equal(t, "Bob", bob.EngName)
		assert.False(t, bob.IsOwner)
		assert.Equal(t, "BOB", bob.AccountName)
		assert.Empty(t, bob.SectName)
	})
```

In the `"enrich=false leaves display fields zero on same seed data"` subtest, after the existing `assert.Zero(t, m.MemberCount)` (line 780), add:

```go
		assert.Empty(t, m.SectName)
		assert.Empty(t, m.AccountName)
		assert.Empty(t, m.EmployeeID)
		assert.Empty(t, m.OrgDescription)
```

In `TestMongoStore_ListRoomMembers_BotEnrichment_Integration`, after the `bot.Name` assertion (line 993), add bot/human `accountName` checks:

```go
		assert.Equal(t, "WEATHER.BOT", bot.AccountName, "bot gets uppercased account")
		assert.Empty(t, bot.SectName, "bot has no user doc → no sectName")
		assert.Equal(t, "ALICE", human.AccountName)
```

- [ ] **Step 2: Run the integration tests to verify they fail**

Run: `make test-integration SERVICE=room-service`
Expected: FAIL — `alice.SectName` is `""`, `alice.AccountName` is `""`.

- [ ] **Step 3: Project the new fields in `findUsersForDisplay`**

Replace the projection (line 826) with:

```go
		options.Find().SetProjection(bson.M{"_id": 0, "account": 1, "engName": 1, "chineseName": 1, "sectName": 1, "employeeId": 1}),
```

- [ ] **Step 4: Set the fields in `attachUserDisplayNames`**

Replace the per-member loop (lines 803-816) with:

```go
	for i := range members {
		if members[i].Member.Type != model.RoomMemberIndividual {
			continue
		}
		acct := members[i].Member.Account
		members[i].Member.AccountName = strings.ToUpper(acct)
		if u, ok := userByAccount[acct]; ok {
			members[i].Member.EngName = u.EngName
			members[i].Member.ChineseName = u.ChineseName
			members[i].Member.SectName = u.SectName
			members[i].Member.EmployeeID = u.EmployeeID
			continue
		}
		if name, ok := appByAssistant[acct]; ok {
			members[i].Member.Name = name
		}
	}
```

- [ ] **Step 5: Run the integration tests to verify they pass**

Run: `make test-integration SERVICE=room-service`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add room-service/store_mongo.go room-service/integration_test.go
git commit -m "feat(room-service): enrich individual extras on subscriptions fallback path"
```

---

### Task 6: Lock the handler passthrough contract

**Files:**
- Test: `room-service/handler_test.go:1811-1837`

The `listMembers` handler (`handler.go:367-397`) returns the store result verbatim — no handler code changes. This step pins that the new fields pass through.

- [ ] **Step 1: Extend the enrich-passthrough test**

In `room-service/handler_test.go`, replace the `"enrich=true passed through to store"` table case (lines 1811-1837) with:

```go
		{
			name: "enrich=true passed through to store",
			body: []byte(`{"enrich":true}`),
			setupMock: func(s *MockRoomStore) {
				s.EXPECT().CheckMembership(gomock.Any(), requester, roomID).
					Return(nil)
				s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil), true).
					Return([]model.RoomMember{
						{
							ID: "rm1", RoomID: roomID, Ts: time.Unix(1, 0).UTC(),
							Member: model.RoomMemberEntry{
								ID: "alice", Type: model.RoomMemberIndividual, Account: "alice",
								EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
								SectName: "Cardiology", AccountName: "ALICE", EmployeeID: "E10293",
							},
						},
						{
							ID: "rm2", RoomID: roomID, Ts: time.Unix(2, 0).UTC(),
							Member: model.RoomMemberEntry{
								ID: "DEPT-100", Type: model.RoomMemberOrg,
								OrgName: "Cardiology Department", OrgDescription: "Inpatient care", MemberCount: 42,
							},
						},
					}, nil)
			},
			want: want{members: []model.RoomMember{
				{
					ID: "rm1", RoomID: roomID, Ts: time.Unix(1, 0).UTC(),
					Member: model.RoomMemberEntry{
						ID: "alice", Type: model.RoomMemberIndividual, Account: "alice",
						EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
						SectName: "Cardiology", AccountName: "ALICE", EmployeeID: "E10293",
					},
				},
				{
					ID: "rm2", RoomID: roomID, Ts: time.Unix(2, 0).UTC(),
					Member: model.RoomMemberEntry{
						ID: "DEPT-100", Type: model.RoomMemberOrg,
						OrgName: "Cardiology Department", OrgDescription: "Inpatient care", MemberCount: 42,
					},
				},
			}},
		},
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `make test SERVICE=room-service`
Expected: PASS (no production change needed — the handler already returns `Members` verbatim).

- [ ] **Step 3: Commit**

```bash
git add room-service/handler_test.go
git commit -m "test(room-service): pin new enrich fields pass through member.list handler"
```

---

### Task 7: Document the new fields in `docs/client-api.md`

**Files:**
- Modify: `docs/client-api.md:1268` (enrich note), `:1289-1320` (`RoomMemberEntry` table + JSON example)

- [ ] **Step 1: Update the `enrich` request-field note**

Replace line 1268 with:

```markdown
| `enrich` | boolean | no | When `true`, populates the display fields (`engName`, `chineseName`, `name`, `isOwner`, `sectName`, `accountName`, `employeeId`, `orgName`, `memberCount`, `orgDescription`) on each entry. Omitted-or-`false` returns the lean record only. |
```

- [ ] **Step 2: Add the new rows to the `RoomMemberEntry` table**

In the `RoomMemberEntry` field table, add after the `chineseName` row (line 1297):

```markdown
| `sectName` | string | Optional. The member's section name. Populated only when `enrich: true` and entry is an individual. |
| `accountName` | string | Optional. The account in capital letters (e.g. `alice` → `ALICE`). Populated only when `enrich: true` and entry is an individual (including bots). |
| `employeeId` | string | Optional. The member's employee ID. Populated only when `enrich: true` and entry is an individual. |
```

And add after the `memberCount` row (line 1301):

```markdown
| `orgDescription` | string | Optional. Org's description (dept description preferred, sect description fallback; omitted when empty). Populated only when `enrich: true` and entry is an org. |
```

- [ ] **Step 3: Update the success-response JSON example**

Replace the JSON example block (lines 1303-1321) with:

```json
{
  "members": [
    {
      "id": "01970a4f8c2d7c9a01970a4f8c2d7c9b",
      "rid": "01970a4f8c2d7c9aQ",
      "ts": "2026-05-01T10:00:00Z",
      "member": {
        "id": "01970a4f8c2d7c9a01970a4f8c2d7c9a",
        "type": "individual",
        "account": "alice",
        "engName": "Alice",
        "chineseName": "愛麗絲",
        "isOwner": true,
        "sectName": "Cardiology",
        "accountName": "ALICE",
        "employeeId": "E10293"
      }
    },
    {
      "id": "01970a4f8c2d7c9a01970a4f8c2d7c9c",
      "rid": "01970a4f8c2d7c9aQ",
      "ts": "2026-05-01T10:05:00Z",
      "member": {
        "id": "DEPT-100",
        "type": "org",
        "orgName": "Cardiology Department",
        "orgDescription": "Inpatient & outpatient cardiac care",
        "memberCount": 42
      }
    }
  ]
}
```

- [ ] **Step 4: Commit**

```bash
git add docs/client-api.md
git commit -m "docs(client-api): document new member.list enrich fields"
```

---

### Task 8: Full verification + quality gate (`/simplify`, ≤ 2-line comments)

**Files:** none new — verification and cleanup only.

- [ ] **Step 1: Format and lint**

Run: `make fmt && make lint`
Expected: no diff after fmt beyond alignment; lint exits 0.

- [ ] **Step 2: Full unit + integration suite**

Run: `make test SERVICE=pkg/model && make test SERVICE=room-service && make test-integration SERVICE=room-service`
Expected: all PASS. Confirm coverage stays ≥ 80% (target ≥ 90% for store/handler): `go tool cover -func=coverage.out` after `make test SERVICE=room-service` if a number is needed.

- [ ] **Step 3: SAST gate**

Run: `make sast`
Expected: no medium+ findings (the change adds no `exec`, crypto, or unsafe conversions).

- [ ] **Step 4: Run `/simplify` on the working diff**

Invoke the `simplify` skill on the branch diff. Apply its reuse/simplification/efficiency/altitude fixes. Re-run Step 2 after applying.

- [ ] **Step 5: Comment-length sweep (≤ 2 lines)**

Verify no comment in any changed hunk exceeds two lines. Check the comments added/touched in this plan:
`pkg/model/member.go` (the two new display-field comments) and `room-service/orgdisplay.go` (`orgDisplayDescription` doc). Shorten any that grew past two lines during `/simplify`.

- [ ] **Step 6: Commit any cleanup**

```bash
git add -A
git commit -m "refactor(room-service): simplify per /simplify; cap comments at 2 lines"
```

- [ ] **Step 7: Push**

```bash
git push -u origin claude/upbeat-galileo-hzn9cc
```

---

## Notes for the implementer

- **No store-interface change:** `ListRoomMembers`'s signature is unchanged, so `make generate` is NOT required and `mock_store_test.go` is untouched.
- **`accountName` is set for individuals only:** on the `room_members` path it is set in the non-org branch; on the fallback path every row is an individual. Org rows have no `account`, so `accountName` is never set on them.
- **`orgDescription` is omitted, never `orgID`:** unlike `orgName` (which falls back to the orgID), `orgDescription` falls back to `""` and is elided by `omitempty`.
- **Bots:** `accountName` is set (uppercased bot account); `sectName`/`employeeId` stay empty (no user doc).
