# New step phrasings used in room-service.feature

These step phrasings appear in
`tools/integration-suite/features/service/room-service.feature` but are NOT
registered in any existing `*_steps_test.go` file. They must be implemented
(or the scenarios must be reworded to use existing vocabulary) before
`make integration-suite-lint` will pass and the scenarios can be promoted to
`@status:approved`.

Existing registered steps (relevant subset from `make -C tools/integration-suite steps`):

| Kind  | Registered regex |
|-------|-----------------|
| Given | `^user "([^"]+)" is authenticated$` |
| When  | `^"([^"]+)" creates channel room "([^"]+)"$` |
| When  | `^"([^"]+)" requests role "([^"]+)" for member "([^"]+)" in room "([^"]+)"$` |
| Then  | `^the response is a (\w+) error$` |
| Then  | `^the response is successful$` |

---

## New steps needed

| Step kind | Suggested regex | Used in scenario(s) |
|-----------|----------------|---------------------|
| When | `^"([^"]+)" requests to create a DM with user "([^"]+)"$` | "Creating a DM with yourself is rejected"; "Creating a DM that already exists returns the existing room ID" |
| When | `^"([^"]+)" requests to create a channel room without a request ID header$` | "Create room with no X-Request-ID header is rejected" |
| When | `^"([^"]+)" requests to create a channel room with a name of (\d+) runes$` | "Creating a channel with a name longer than 100 runes is rejected" |
| When | `^"([^"]+)" requests to create an empty room$` | "Creating a room with no users, orgs, channels, or name is rejected" |
| When | `^"([^"]+)" requests to create a channel room "([^"]+)" with bot user "([^"]+)"$` | "Adding a bot account to a channel create request is rejected" |
| Given | `^"([^"]+)" has created channel room "([^"]+)"$` | Multiple scenarios (setup step for add-member, list-members, etc.) |
| Given | `^"([^"]+)" has added "([^"]+)" to room "([^"]+)"$` | Multiple scenarios (add-member, remove-member flows) |
| Given | `^room "([^"]+)" is marked as restricted$` | "A non-owner member cannot add to a restricted channel" |
| Given | `^"([^"]+)" has created a DM room with "([^"]+)"$` | "Adding a member to a DM room is rejected" |
| When | `^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$` | Multiple add-member scenarios |
| When | `^"([^"]+)" requests to add member "([^"]+)" to the DM room with "([^"]+)"$` | "Adding a member to a DM room is rejected" |
| When | `^"([^"]+)" requests to remove member "([^"]+)" from room "([^"]+)"$` | Multiple remove-member scenarios |
| When | `^"([^"]+)" requests to remove a member from room "([^"]+)" with no account or orgId$` | "Remove member with neither account nor orgId is rejected" |
| When | `^"([^"]+)" requests to list members of room "([^"]+)"$` | "A non-member cannot list members"; "A subscribed member can list members" |
| When | `^"([^"]+)" requests to list members of room "([^"]+)" with limit (\d+)$` | "Listing members with a zero limit is rejected" |
| When | `^the room info batch RPC is called with (\d+) room IDs$` | "RoomsInfoBatch with an empty roomIds list is rejected"; "RoomsInfoBatch with too many room IDs is rejected" |
| When | `^the room info batch RPC is called with room ID "([^"]+)"$` | "RoomsInfoBatch for an unknown room returns Found=false" |
| Then | `^the response contains a room info entry with "([^"]+)" set to "([^"]+)"$` | "RoomsInfoBatch for an unknown room returns Found=false" |
| Then | `^the response contains a "([^"]+)" error with an existing room ID$` | "Creating a DM that already exists returns the existing room ID" |
| When | `^"([^"]+)" requests read receipts for message "([^"]+)" in room "([^"]+)"$` | "A non-member cannot query read receipts"; "A member who is not the message sender cannot view read receipts" |
| When | `^"([^"]+)" requests read receipts with no message ID in room "([^"]+)"$` | "Read receipt request with an empty message ID is rejected" |

## Implementation notes

**`"([^"]+)" has created channel room "([^"]+)"`**
Wraps the existing `createsChannelRoom` step and stores the returned `roomId`
on the world object. Needed as a `Given` setup step (vs. `When` assertion step).
The create-room reply shape is `{"status":"accepted","roomId":"...","roomType":"channel"}`.

**`"([^"]+)" has added "([^"]+)" to room "([^"]+)"`**
Calls `subject.MemberAdd(actor, prefixedRoom, site)` with an `AddMembersRequest{Users:[target]}`.
Reply is `{"status":"accepted"}`. Used for test setup only.

**`room "([^"]+)" is marked as restricted`**
Requires direct Mongo update (`rooms.updateOne({_id: roomID}, {$set: {restricted: true}})`)
— not achievable via NATS request/reply in Part 1. This step is a Part 2
prerequisite and its scenario should remain `@blindspot` or `# TODO` until the
Mongo-seeding primitive lands.

**`the room info batch RPC is called with N room IDs`**
When N=0: sends `{"roomIds":[]}` on `subject.RoomsInfoBatch(site)`.
When N=1001: sends 1001 placeholder IDs (fill with e.g. `"x"+strconv.Itoa(i)`).
This is a server-to-server subject with no user account — the step should use
a service-level NATS connection, not a user credential.

**`the room info batch RPC is called with room ID "X"`**
Sends `{"roomIds":["X"]}` and captures the response for the `Then` assertion.

**`the response contains a room info entry with "found" set to "false"`**
Unmarshals `model.RoomsInfoBatchResponse` and asserts `Rooms[0].Found == false`.

**`the response contains a "dm already exists" error with an existing room ID`**
Unmarshals `model.ErrorResponse` and asserts `Error == "dm already exists"` and
`RoomID != ""`.
