# Replace `{account}` / `username` with `account` — Design

## Goal

Replace the ambiguous `{account}` placeholder in NATS subject naming and the
`username` field in user-related model types with the more explicit term
`account`, throughout code, tests, and documentation.

The existing terminology is confusing:

- Docs (`nats-subject-naming.md`, `CLAUDE.md`) use `chat.user.{account}.*`, but
  the code in `pkg/subject/subject.go` already uses a parameter named
  `username`. Both refer to the same thing — the human-readable user handle.
- The model field `User.Username` is really the account identifier used in
  subjects and auth.
- Separately, several Go types carry a `UserID` field that holds the UUID
  primary key (`Message.UserID`, `Participant.UserID`,
  `SubscriptionUpdateEvent.UserID`, `InviterID`, `InviteeID`,
  `mentionedUserIDs`). These stay as UUIDs and are NOT part of this rename.

After this change:

- The account handle (previously `username`) is consistently called `account`
  in code, BSON, JSON, and documentation.
- The NATS subject placeholder `{account}` becomes `{account}`.
- UUID-bearing fields remain untouched.

## Non-goals

- No changes to `User.ID` (UUID primary key).
- No changes to UUID-bearing fields: `Message.UserID`, `Participant.UserID`,
  `SubscriptionUpdateEvent.UserID`, `InviterID`, `InviteeID`,
  `mentionedUserIDs`.
- No MongoDB data migration scripts — this is a dev-branch refactor. Existing
  documents with the old `username` BSON key will not be read by the new code.
- No Keycloak realm export changes — `username` there is Keycloak's own field
  name and is out of scope.

## Scope

### 1. Model changes (`pkg/model/`)

| Struct | Old field | New field | JSON tag | BSON tag |
|---|---|---|---|---|
| `User` | `Username` | `Account` | `account` | `account` |
| `SubscriptionUser` | `Username` | `Account` | `account` | `account` |
| `Participant` | `Username` | `Account` | `account` | `account` |
| `Message` | `Username` | `UserAccount` | `userAccount` | `userAccount` |
| `InviteMemberRequest` | `InviteeAccount` | `InviteeAccount` | `inviteeAccount` | — |
| `CreateRoomRequest` | `CreatedByUsername` | `CreatedByAccount` | `createdByAccount` | — |

Rationale:

- `User`, `SubscriptionUser`, and `Participant` are typed containers where the
  field clearly refers to "this thing's account". Use plain `Account`.
- `Message` is a flat struct with separate `UserID` (UUID) and handle fields.
  Use `UserAccount` to pair clearly with `UserID`.
- `InviteMemberRequest` and `CreateRoomRequest` already use role prefixes
  (`Invitee`, `CreatedBy`) — `InviteeAccount` / `CreatedByAccount` keep the
  existing disambiguation.

### 2. Subject builders (`pkg/subject/`)

- Rename every `username` parameter in `subject.go` to `account`.
- `ParseUserRoomSubject` and `ParseUserRoomSiteSubject` return named values
  `account, roomID[, siteID], ok`.
- `MsgHistoryPattern`, `MsgNextPattern`, `MsgSurroundingPattern`,
  `MsgGetPattern` replace `{account}` placeholders with `{account}`.
- Update `subject_test.go` accordingly.

### 3. NATS router (`pkg/natsrouter/`)

- Update `params.go`, `params_test.go`, `router_test.go`, `example_test.go`,
  and `README.md` to use `{account}` as the placeholder example wherever
  `{account}` is currently used.

### 4. Services

Every service that currently references `model.User.Account`,
`model.Message.UserAccount`, `model.Participant.Account`,
`model.SubscriptionUser.Account`, `InviteeAccount`, or `CreatedByUsername`
must be updated:

- Struct field accesses → new field names.
- Local variables/parameters named `username` that hold the account string →
  renamed to `account`.
- Method names like `GetUserByUsername` → `GetUserByAccount`.
- MongoDB query keys `"account"` → `"account"` where they target the
  renamed fields.
- Mocks regenerated via `make generate`.
- All unit and integration tests updated.

Services affected (based on grep):

- `auth-service`
- `room-service`, `room-worker`
- `message-gatekeeper`, `message-worker`
- `broadcast-worker`
- `notification-worker`
- `inbox-worker`
- `history-service`
- `pkg/roomkeysender`
- `pkg/oidc`

### 5. Documentation

Files to update:

- `docs/nats-subject-naming.md` — all `{account}` → `{account}`, plus
  text/table references.
- `CLAUDE.md` — `chat.user.{account}.…` examples in the Subject Naming section.
- `pkg/natsrouter/README.md`
- `message-worker/README.md`
- All existing specs under `docs/superpowers/specs/` that use `{account}`.
- All existing plans under `docs/superpowers/plans/` that use `{account}`.

## Verification

1. `make generate` — regenerate mocks after store interface signature changes.
2. `make fmt` — format all Go files.
3. `make lint` — golangci-lint must pass.
4. `make test` — all unit tests must pass with the race detector.

Integration tests are not required to run locally for this refactor (they
need Docker), but they will be updated for compilation and must lint-clean.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| Accidentally renaming UUID-bearing fields (`Message.UserID` etc.) | Only rename fields listed in the table above; never rename on the word `UserID` alone. Grep for `UserID` hits and confirm they refer to UUIDs before editing. |
| BSON tag change breaks existing data | Acceptable — dev-branch refactor; no prod data migration in scope. |
| Keycloak `username` references in realm export | Explicitly out of scope; do not edit `auth-service/deploy/keycloak/realm-export.json`. |
| Test helpers seeding `"account"` BSON documents | Update the helpers and any `testdata/` fixtures. |
| Missed references in comments/docs | Final `grep -n "{account}"` and `grep -n "account"` sweep before commit, manually review remaining hits (Keycloak, external clients). |

## Execution order

1. Update `pkg/model/` types and run `make test SERVICE=pkg/model`.
2. Update `pkg/subject/` builders and tests.
3. Update `pkg/natsrouter/` and other `pkg/` packages.
4. Update each service in turn (`auth-service`, `room-service`, …).
5. Run `make generate` to refresh mocks.
6. Run `make fmt && make lint && make test`.
7. Update docs (`docs/nats-subject-naming.md`, `CLAUDE.md`, specs, plans,
   READMEs).
8. Final grep sweep.
9. Commit and push to `claude/replace-userid-with-account-e8Q7t`.
