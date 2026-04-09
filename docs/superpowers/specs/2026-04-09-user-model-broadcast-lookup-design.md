# User Model Update & Broadcast Worker User Lookup

**Date:** 2026-04-09
**Status:** Approved

## Summary

Update the `User` model to include `EngName`, `ChineseName`, and `EmployeeID` fields (removing the `Name` field). Create a shared `pkg/userstore` package for reusable MongoDB user lookups. Migrate `broadcast-worker` from querying the `employee` collection to querying the `users` collection via the shared package.

## Motivation

- The `User` model currently has a generic `Name` field that doesn't distinguish between English and Chinese names
- The `broadcast-worker` queries the `employee` collection for display name enrichment, but this data belongs in the `users` collection
- Multiple services will need user lookups by account in the future — a shared package avoids duplication

## Design

### 1. User Model Changes

**File:** `pkg/model/user.go`

Remove the `Name` field. Add three new fields:

```go
type User struct {
    ID          string `json:"id" bson:"_id"`
    Account     string `json:"account" bson:"account"`
    SiteID      string `json:"siteId" bson:"siteId"`
    EngName     string `json:"engName" bson:"engName"`
    ChineseName string `json:"chineseName" bson:"chineseName"`
    EmployeeID  string `json:"employeeId" bson:"employeeId"`
}
```

### 2. Shared `pkg/userstore` Package

New package following the `pkg/roomkeystore` precedent for shared store implementations.

**File:** `pkg/userstore/userstore.go`

```go
type UserStore interface {
    FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
}
```

- `mongoStore` struct holding a `*mongo.Collection` (the `users` collection)
- `NewMongoStore(col *mongo.Collection) UserStore` constructor
- `FindUsersByAccounts` queries with `{"account": {"$in": accounts}}`, projecting `_id`, `account`, `engName`, `chineseName`, `employeeId`

### 3. Broadcast Worker Changes

**Store interface (`store.go`):**
- Remove `FindEmployeesByAccountNames` method from the `Store` interface
- The handler receives a separate `userstore.UserStore` dependency instead

**Store implementation (`store_mongo.go`):**
- Remove `empCol` field from `mongoStore`
- Remove `FindEmployeesByAccountNames` implementation
- Update `NewMongoStore` to accept only `roomCol` and `subCol`

**Main wiring (`main.go`):**
- Replace `db.Collection("employee")` with `db.Collection("users")` passed to `userstore.NewMongoStore`
- Pass the `UserStore` instance to the handler constructor

**Handler (`handler.go`):**
- Add `userstore.UserStore` field to the handler struct
- Replace `employeeMap map[string]model.Employee` with `userMap map[string]model.User`
- Rename store call from `FindEmployeesByAccountNames` to `FindUsersByAccounts` (called on the `UserStore`)
- Update `buildClientMessage`: map `User.ChineseName` and `User.EngName` to `Participant` (previously `Employee.Name` -> `ChineseName` and `Employee.EngName` -> `EngName`)
- Update `buildMentionParticipants`: same field mapping change

**Tests (`handler_test.go`):**
- Update mocks to use `UserStore` mock instead of `Store.FindEmployeesByAccountNames`
- Update test data to use `model.User` instead of `model.Employee`

**Integration tests (`integration_test.go`):**
- Insert test data into `users` collection instead of `employee` collection
- Use `model.User` field names (`account`, `engName`, `chineseName`) instead of Employee field names

**Generated mocks (`mock_store_test.go`):**
- Regenerated via `make generate` after interface changes

### 4. Unchanged

- `pkg/model/employee.go` — kept as-is
- `employee` MongoDB collection — kept as-is
- All other services — no changes

## Approach Rationale

- **Shared package (Option B1):** Follows the `pkg/roomkeystore` precedent. Interface + implementation defined in the shared package, since this is designed for multi-service reuse.
- **Interface naming (`UserStore`):** Follows the `<Domain>Store` convention from CLAUDE.md and avoids collision with potential future shared stores (`roomstore`, `subscriptionstore`).
- **Employee collection retained:** Per requirement, even though `broadcast-worker` will no longer use it.
