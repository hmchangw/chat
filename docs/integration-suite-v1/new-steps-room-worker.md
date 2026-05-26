# New Step Phrasings — room-worker member operations

Steps used in `features/pipeline/room-member-ops.feature` that are not yet in the
registered vocabulary (`make -C tools/integration-suite steps`). Each row gives the
Go `ctx.Step` regex that must be registered in a `*_steps_test.go` file before the
scenario can be promoted to `@status:approved`.

| Step phrasing (Gherkin text) | Go regex | Suggested file | Notes |
|---|---|---|---|
| `"alice" is the owner of channel room "project-alpha"` | `^"([^"]+)" is the owner of channel room "([^"]+)"$` | `room_steps_test.go` | Creates the room via `RoomCreate` (existing step helper) and records the actor as owner; uses `suiteWorld.Prefix()` for the room name. |
| `"alice" requests to add member "bob" to room "project-alpha"` | `^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$` | `room_steps_test.go` | Publishes `AddMembersRequest{RoomID, Users:[bob]}` on `subject.MemberAdd(account, roomID, site)`. Captures reply in `suiteWorld.LastResponse`. |
| `"alice" requests to add member "bob" to room "project-beta" without a request ID` | `^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)" without a request ID$` | `room_steps_test.go` | Same as above but suppresses the `X-Request-ID` header injection. Requires Part-2 JetStream publish primitive; register as a stub that returns an error (mirrors `submitsMessageWith` pattern). |
| `"alice" requests to add "alice" to the DM room between "alice" and "bob"` | `^"([^"]+)" requests to add "([^"]+)" to the DM room between "([^"]+)" and "([^"]+)"$` | `room_steps_test.go` | Looks up the DM room ID from fixture state, then publishes `AddMembersRequest` on `subject.MemberAdd`. |
| `a DM room exists between "alice" and "bob"` | `^a DM room exists between "([^"]+)" and "([^"]+)"$` | `room_steps_test.go` | Uses `subject.RoomCreateDMSync` (sync DM create endpoint) to establish the DM room and stash its ID for subsequent steps. |
| `"alice" is the owner of restricted channel room "classified"` | `^"([^"]+)" is the owner of restricted channel room "([^"]+)"$` | `room_steps_test.go` | Same as the non-restricted variant but sets `Restricted: true` in the `CreateRoomRequest`. Requires room-service to expose a restricted-room flag on the create path. |
| `"bob" is a member of room "project-delta"` | `^"([^"]+)" is a member of room "([^"]+)"$` | `room_steps_test.go` | Issues an add-member request as the room's owner on behalf of `bob`; waits for the async job result or list-members confirmation. |
| `"bob" is an owner of room "project-iota"` | `^"([^"]+)" is an owner of room "([^"]+)"$` | `room_steps_test.go` | Issues an add-member request then a role-update request (owner → owner target) for the named user. |
| `"bob" requests to leave room "project-delta"` | `^"([^"]+)" requests to leave room "([^"]+)"$` | `room_steps_test.go` | Publishes `RemoveMemberRequest{RoomID, Requester:bob, Account:bob}` on `subject.MemberRemove(account, roomID, site)`. |
| `"bob" requests to remove member "carol" from room "project-epsilon"` | `^"([^"]+)" requests to remove member "([^"]+)" from room "([^"]+)"$` | `room_steps_test.go` | Publishes `RemoveMemberRequest{RoomID, Requester:actor, Account:target}` on `subject.MemberRemove`. |
| `"alice" receives an async job error for operation "room.member.add"` | `^"([^"]+)" receives an async job error for operation "([^"]+)"$` | `room_steps_test.go` | Pre-arms a core-NATS subscription on `subject.UserResponse(account, requestID)`, then asserts `AsyncJobResult{status:"error", operation:<op>}` arrives within the declared budget. Requires Part-2 decoupled-reply primitive. |
| `a subscription exists in MongoDB for "bob" in room "project-gamma"` | `^a subscription exists in MongoDB for "([^"]+)" in room "([^"]+)"$` | `room_steps_test.go` | Requires Part-2 Mongo observation primitive. |
| `no subscription exists in MongoDB for "bob" in room "project-eta"` | `^no subscription exists in MongoDB for "([^"]+)" in room "([^"]+)"$` | `room_steps_test.go` | Requires Part-2 Mongo observation primitive. |
| `"bob"'s subscription in room "project-mu" contains role "owner"` | `^"([^"]+)"'s subscription in room "([^"]+)" contains role "([^"]+)"$` | `room_steps_test.go` | Requires Part-2 Mongo observation primitive. |
| `an outbox event of type "member_added" is published to site "site-remote"` | `^an outbox event of type "([^"]+)" is published to site "([^"]+)"$` | `room_steps_test.go` | Requires Part-2 JetStream stream-peek on `OUTBOX_{siteID}`. |

## Notes on Part-1 vs Part-2 steps

Steps that observe Mongo state (`a subscription exists in MongoDB …`, `no subscription exists in MongoDB …`, `"bob"'s subscription … contains role "owner"`) and steps that observe JetStream streams (`an outbox event of type … is published to site …`) are Part-2 primitives. They appear in `@blindspot`-tagged scenarios and must NOT be wired to live assertions until the corresponding harness primitive exists.

The `without a request ID` variant of the add-member step is also Part-2 (it requires suppressing the auto-injected `X-Request-ID` header and using the JetStream publish path rather than NATS request/reply). It should be stubbed to return an error with a blindspot message, mirroring the `submitsMessageWith` pattern in `messaging_steps_test.go`.

The `"alice" receives an async job error for operation …` step requires a pre-armed NATS subscription on `chat.user.{account}.response.{requestID}` combined with the JetStream publish primitive — both Part-2.
