# Step library — final list

After dedup. 75 unique step phrasings, grouped by surface. Ready for
implementation in `tools/integration-suite/*_steps_test.go`.

Status legend (carried from the proposal's Arch-grounding column):
  ✓ arch-grounded   — at least one cited spec doc backs every use
  ◇ blindspot       — every using scenario is correctly tagged @blindspot
  ◐ mixed           — split across both
  ⚠ code-only       — citation omission to fix in feature header

## Auth & users (2 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 1 | `^user "([^"]+)" is authenticated$` | EXISTS — nkey gen → POST /auth → stash JWT+seed | (existing) | 1 | auth_steps_test.go | already registered |
| 2 | `^user "([^"]+)" is authenticated in site "([^"]+)"$` | EXISTS — same as above but stashes JWT+seed scoped to the named site | (existing) | 1 | auth_steps_test.go | already registered; absorbs the dropped `from site` near-duplicate (dedup #1) |

---

## Rooms — create & info (10 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 3 | `^"([^"]+)" creates channel room "([^"]+)"$` | EXISTS — posts CreateRoomRequest for a channel room; captures reply as LastResponse | (existing) | 1 | room_steps_test.go | already registered |
| 4 | `^"([^"]+)" requests to create a DM with user "([^"]+)"$` | Posts CreateRoomRequest for a DM between actor and target; captures reply as LastResponse | room-service.feature:39,97 | 1 | room_steps_test.go | ⚠ feature header needs `2026-04-28-create-room-design.md` citation |
| 5 | `^"([^"]+)" requests to create a channel room without a request ID header$` | Sends CreateRoomRequest suppressing the auto-injected X-Request-ID NATS header; captures reply as LastResponse | room-service.feature:53 | 1 | room_steps_test.go | ◇ |
| 6 | `^"([^"]+)" requests to create a channel room with a name of (\d+) runes$` | Generates a name string of exactly N Unicode runes and sends CreateRoomRequest; captures reply as LastResponse | room-service.feature:64 | 1 | room_steps_test.go | ⚠ feature header needs `2026-04-28-create-room-design.md` citation |
| 7 | `^"([^"]+)" requests to create an empty room$` | Sends CreateRoomRequest with all fields zero/absent; captures reply as LastResponse | room-service.feature:74 | 1 | room_steps_test.go | ⚠ feature header needs `2026-04-28-create-room-design.md` citation |
| 8 | `^"([^"]+)" requests to create a channel room "([^"]+)" with bot user "([^"]+)"$` | Sends CreateRoomRequest for a channel with a bot-suffix or p_-prefix account in the users list; captures reply as LastResponse | room-service.feature:85 | 1 | room_steps_test.go | ⚠ feature header needs `2026-04-28-create-room-design.md` citation |
| 9 | `^the room info batch RPC is called with (\d+) room IDs$` | Issues RoomsInfoBatch via service-level NATS connection (no user credential) with N placeholder room IDs; captures reply as LastResponse | room-service.feature:282,290 | 1 | room_steps_test.go | ✓ |
| 10 | `^the room info batch RPC is called with room ID "([^"]+)"$` | Issues RoomsInfoBatch with a single literal room ID; captures RoomsInfoBatchResponse as LastResponse | room-service.feature:300 | 1 | room_steps_test.go | ✓ |
| 11 | `^the response contains a room info entry with "([^"]+)" set to "([^"]+)"$` | Unmarshals RoomsInfoBatchResponse and asserts Rooms[0].\<field\> == \<value\> | room-service.feature:302 | 1 | room_steps_test.go | ✓ |
| 12 | `^the response contains a "([^"]+)" error with an existing room ID$` | Asserts ErrorResponse.Error == slug and ErrorResponse.RoomID is non-empty | room-service.feature:98 | 1 | room_steps_test.go | ◇ |

---

## Rooms — DM fixture & action (2 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 13 | `^"([^"]+)" has created a DM room with "([^"]+)"$` | Setup fixture: calls DM create RPC and stores resulting room ID on the world; does not set LastResponse | room-service.feature:140 | 2 | room_steps_test.go | ◇ depends on step 4 helper landing first |
| 14 | `^"([^"]+)" requests to add member "([^"]+)" to the DM room with "([^"]+)"$` | Looks up DM room ID from world state (set by step 13), then issues add-member RPC; captures reply as LastResponse | room-service.feature:143 | 2 | room_steps_test.go | ◇ |

---

## Room members — setup fixtures / Given (7 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 15 | `^"([^"]+)" has created channel room "([^"]+)"$` | Setup fixture: calls the channel-create helper and stores room ID on world; does not set LastResponse | room-service.feature:111,126,154,166,198,210,216,227,246,257,266,315,325,344; room-member-ops.feature (multiple) | 1 | room_steps_test.go | ◐ |
| 16 | `^"([^"]+)" is the owner of channel room "([^"]+)"$` | Alias fixture for `has created channel room`: registers actor as owner and stores room ID on world | room-member-ops.feature:41,57,85,141,155,169,199,209,225,237,252 | 1 | room_steps_test.go | ✓ implemented as thin alias calling the same helper as step 15 |
| 17 | `^"([^"]+)" is the owner of restricted channel room "([^"]+)"$` | Like step 16 but sets Restricted:true in CreateRoomRequest | room-member-ops.feature:85 | 2 | room_steps_test.go | ✓ |
| 18 | `^"([^"]+)" has added "([^"]+)" to room "([^"]+)"$` | Setup fixture: issues AddMembersRequest on behalf of actor; reply {"status":"accepted"} asserted internally | room-service.feature:127,170,198,210,216,227 | 1 | room_steps_test.go | ✓ |
| 19 | `^"([^"]+)" is a member of room "([^"]+)"$` | Setup fixture: adds named user to room via add-member RPC as the room owner; waits for acceptance | room-member-ops.feature:88,141,157,159,197,226,239,253 | 1 | room_steps_test.go | ✓ |
| 20 | `^"([^"]+)" is an owner of room "([^"]+)"$` | Setup fixture: issues add-member + role-update(owner) for named user | room-member-ops.feature:210 | 1 | room_steps_test.go | ✓ |
| 21 | `^a DM room exists between "([^"]+)" and "([^"]+)"$` | Setup fixture: calls DM-create sync endpoint and stashes room ID on world | room-member-ops.feature:71 | 1 | room_steps_test.go | ✓ |

---

## Rooms — MongoDB & multi-site fixtures (2 steps, Part-2)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 22 | `^room "([^"]+)" is marked as restricted$` | Directly updates rooms collection in MongoDB to set restricted:true for the named room | room-service.feature:128 | 2 | room_steps_test.go | ⚠ scenario at line 120 missing @blindspot tag; feature header missing spec citation |
| 23 | `^channel room "([^"]+)" is homed on site "([^"]+)" with member "([^"]+)"$` | Multi-site setup: creates room on named site via site-scoped NATS and adds named member | message-persistence.feature:228 | 2 | room_steps_test.go | ◇ |

---

## Room members — When verbs (11 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 24 | `^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$` | Publishes AddMembersRequest{Users:[target]} on subject.MemberAdd; captures reply as LastResponse | room-service.feature:113,129,155; room-member-ops.feature:43,113 | 1 | room_steps_test.go | ◐ |
| 25 | `^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)" without a request ID$` | Same as step 24 but suppresses the auto-injected X-Request-ID NATS header | room-member-ops.feature:58 | 2 | room_steps_test.go | ◇ |
| 26 | `^"([^"]+)" requests to add "([^"]+)" to the DM room between "([^"]+)" and "([^"]+)"$` | Looks up DM room ID from world state (set by step 21), then issues add-member RPC; captures reply as LastResponse | room-member-ops.feature:73 | 1 | room_steps_test.go | ✓ |
| 27 | `^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)" that does not exist$` | Sends AddMembersRequest for a literal room name that has not been created; captures reply as LastResponse | room-member-ops.feature:100 | 1 | room_steps_test.go | ✓ |
| 28 | `^"([^"]+)" requests to remove member "([^"]+)" from room "([^"]+)"$` | Publishes RemoveMemberRequest{Requester:actor, Account:target} on subject.MemberRemove; captures reply as LastResponse | room-service.feature:172,188,201,213,228; room-member-ops.feature:159,171 | 1 | room_steps_test.go | ✓ |
| 29 | `^"([^"]+)" requests to remove a member from room "([^"]+)" with no account or orgId$` | Sends RemoveMemberRequest with both Account and OrgID empty; captures reply as LastResponse | room-service.feature:230 | 1 | room_steps_test.go | ✓ |
| 30 | `^"([^"]+)" requests to leave room "([^"]+)"$` | Self-leave: sends RemoveMemberRequest{Requester:actor, Account:actor}; captures reply as LastResponse | room-member-ops.feature:144,184 | 1 | room_steps_test.go | ✓ |
| 31 | `^"([^"]+)" requests to list members of room "([^"]+)"$` | Issues list-members RPC on subject.MemberList; captures reply as LastResponse | room-service.feature:247,258 | 1 | room_steps_test.go | ✓ |
| 32 | `^"([^"]+)" requests to list members of room "([^"]+)" with limit (\d+)$` | Issues list-members RPC with explicit Limit field in request; captures reply as LastResponse | room-service.feature:269 | 1 | room_steps_test.go | ✓ |
| 33 | `^"([^"]+)" requests read receipts for message "([^"]+)" in room "([^"]+)"$` | Issues read-receipt RPC with given message ID; captures reply as LastResponse | room-service.feature:317,345 | 1 | room_steps_test.go | ◐ |
| 34 | `^"([^"]+)" requests read receipts with no message ID in room "([^"]+)"$` | Issues read-receipt RPC with MessageID:"" to trigger empty-field validation; captures reply as LastResponse | room-service.feature:328 | 1 | room_steps_test.go | ✓ |

---

## Room members — Then assertions (4 steps, Part-2)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 35 | `^within (\d+)s "([^"]+)" receives an async job error for operation "([^"]+)"$` | Pre-arms a core-NATS subscription on chat.user.{account}.response.{requestID} and asserts AsyncJobResult{status:"error", operation:\<op\>} within deadline | room-member-ops.feature:59 | 2 | room_steps_test.go | ◇ |
| 36 | `^within (\d+)s a subscription exists in MongoDB for "([^"]+)" in room "([^"]+)"$` | Queries MongoDB subscriptions collection and asserts document present within deadline | room-member-ops.feature:115 | 2 | room_steps_test.go | ◇ |
| 37 | `^within (\d+)s no subscription exists in MongoDB for "([^"]+)" in room "([^"]+)"$` | Queries MongoDB subscriptions collection and asserts document absent within deadline | room-member-ops.feature:186 | 2 | room_steps_test.go | ◇ |
| 38 | `^within (\d+)s "([^"]+)"'s subscription in room "([^"]+)" contains role "([^"]+)"$` | Queries MongoDB subscriptions collection and asserts Roles array contains named role within deadline | room-member-ops.feature:255 | 2 | room_steps_test.go | ◇ |

---

## Messaging — submit / edit / delete (17 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 39 | `^"([^"]+)" submits a valid message to room "([^"]+)"$` | JetStream publish on MESSAGES_\<site\> for a well-formed message; pre-arms decoupled reply subscription | message-submission-validation.feature:43 | 2 | messaging_steps_test.go | ◇ |
| 40 | `^"([^"]+)" submits a message with content larger than 20 KB to room "([^"]+)"$` | Constructs 20\*1024+1 byte content then JetStream-publishes to MESSAGES_\<site\>; pre-arms decoupled reply | message-submission-validation.feature:64 | 2 | messaging_steps_test.go | ◐ |
| 41 | `^"([^"]+)" submits a thread reply to room "([^"]+)" with threadParentMessageId set but no threadParentMessageCreatedAt$` | JetStream publish: threadParentMessageId set to valid 20-char base62, threadParentMessageCreatedAt absent | message-submission-validation.feature:81,215 | 2 | messaging_steps_test.go | ◐ |
| 42 | `^"([^"]+)" submits a thread reply to room "([^"]+)" with a malformed threadParentMessageId$` | JetStream publish: threadParentMessageId is not a valid 20-char base62 string | message-submission-validation.feature:97 | 2 | messaging_steps_test.go | ⚠ scenario Source cites only handler.go; spec doc does not document this specific ID-format check |
| 43 | `^"([^"]+)" submits a message to room "([^"]+)" quoting a message from a different thread$` | JetStream publish with a quotedParentMessageId whose thread context differs from the submitter's; requires Cassandra seed | message-submission-validation.feature:117 | 2 | messaging_steps_test.go | ◇ |
| 44 | `^"([^"]+)" submits a main-room message to room "([^"]+)" quoting a thread reply$` | JetStream publish where quotedParentMessageId refers to a message with non-empty ThreadParentID | message-submission-validation.feature:135 | 2 | messaging_steps_test.go | ◇ |
| 45 | `^"([^"]+)" submits a message to room "([^"]+)" quoting a non-existent message id$` | JetStream publish with a well-formed but never-persisted 20-char base62 as quotedParentMessageId | message-submission-validation.feature:152 | 2 | messaging_steps_test.go | ◇ |
| 46 | `^"([^"]+)" submits a message to room "([^"]+)" on behalf of a non-member$` | JetStream publish on actor's subject for a room the actor has no subscription in | message-submission-validation.feature:175 | 2 | messaging_steps_test.go | ◇ |
| 47 | `^"([^"]+)" submits a top-level message to room "([^"]+)"$` | JetStream publish with no threadParentMessageId; named distinctly for large-room scenario readability (see dedup #4 note) | message-submission-validation.feature:196 | 2 | messaging_steps_test.go | ◇ |
| 48 | `^"([^"]+)" publishes a message on a subject for site "([^"]+)" to room "([^"]+)"$` | JetStream publish where the siteID token in the subject differs from the gatekeeper's SITE_ID | message-submission-validation.feature:325 | 2 | messaging_steps_test.go | ◇ |
| 49 | `^"([^"]+)" requests to edit message "([^"]*)" in room "([^"]+)" with new content "([^"]+)"$` | NATS request/reply on subject.MsgEdit with EditMessageRequest{MessageID, NewMsg}; captures reply as LastResponse | message-submission-validation.feature:237,253; history-service.feature:173,209 | 1 | messaging_steps_test.go | ◐ merged from two near-duplicate phrasings (dedup #3); `[^"]*` allows empty messageId |
| 50 | `^"([^"]+)" requests to edit message "([^"]+)" in room "([^"]+)" with empty content$` | NATS request/reply on subject.MsgEdit with newMsg:""; captures reply as LastResponse | message-submission-validation.feature:268 | 2 | messaging_steps_test.go | ◇ |
| 51 | `^"([^"]+)" requests to delete message "([^"]+)" in room "([^"]+)"$` | NATS request/reply on subject.MsgDelete with DeleteMessageRequest{MessageID}; captures reply as LastResponse | message-submission-validation.feature:286,300 | 1 | messaging_steps_test.go | ◐ |
| 52 | `^a channel room "([^"]+)" exists with more than ([0-9]+) members$` | MongoDB seeding fixture: inserts a rooms document with userCount above threshold | message-submission-validation.feature:196,213 | 2 | messaging_steps_test.go | ◇ |
| 53 | `^"([^"]+)" is a regular member of room "([^"]+)"$` | MongoDB seeding fixture: inserts a subscriptions document with roles:["member"] for the named user | message-submission-validation.feature:196,214 | 2 | messaging_steps_test.go | ◇ |
| 54 | `^the response is an accepted reply$` | Reads captured decoupled reply from pre-armed core-NATS subscription; asserts non-error accepted shape | message-submission-validation.feature:44 | 2 | messaging_steps_test.go | ◇ |
| 55 | `^no reply is received on the response subject$` | Waits a short deadline (≤2s) and asserts zero messages arrived on the pre-armed response subject | message-submission-validation.feature:324 | 2 | messaging_steps_test.go | ◇ |

---

## Messaging — JetStream observation (Part-2) (8 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 56 | `^within ([0-9]+)s a canonical created event exists for the message in room "([^"]+)"$` | JetStream stream-peek on MESSAGES_CANONICAL_\<site\> filtered by Nats-Msg-Id; asserts event type and room ID | message-submission-validation.feature:45 | 2 | messaging_steps_test.go | ◇ |
| 57 | `^a canonical created event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$` | JetStream publish to chat.msg.canonical.\<site\>.created; used as When trigger for message-worker scenarios | message-persistence.feature:38,59 | 2 | message_persistence_steps_test.go | ◇ |
| 58 | `^a canonical created event for thread reply "([^"]+)" by "([^"]+)" to parent "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$` | JetStream publish with ThreadParentMessageID set; thread-reply variant of step 57 | message-persistence.feature:82 | 2 | message_persistence_steps_test.go | ◇ |
| 59 | `^the canonical created event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL twice$` | Publishes the identical payload twice to MESSAGES_CANONICAL to simulate JetStream redelivery | message-persistence.feature:164 | 2 | message_persistence_steps_test.go | ◇ |
| 60 | `^canonical created events for "([^"]+)" \(created ([^)]+)\) and "([^"]+)" \(created ([^)]+)\) are delivered out of order to MESSAGES_CANONICAL$` | Publishes two canonical created events with controlled timestamps in reverse order | message-persistence.feature:184 | 2 | message_persistence_steps_test.go | ◇ |
| 61 | `^a canonical updated event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$` | JetStream publish to chat.msg.canonical.\<site\>.updated to verify message-worker's FilterSubject excludes it | message-persistence.feature:123 | 2 | message_persistence_steps_test.go | ◇ |
| 62 | `^a canonical deleted event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$` | JetStream publish to chat.msg.canonical.\<site\>.deleted for the FilterSubject exclusion test | message-persistence.feature:143 | 2 | message_persistence_steps_test.go | ◇ |
| 63 | `^a canonical created event for thread reply "([^"]+)" by "([^"]+)" \(home site "([^"]+)"\) to parent "([^"]+)" in room "([^"]+)" on site "([^"]+)" is published to MESSAGES_CANONICAL$` | Cross-site full variant: JetStream publish carrying the replier's home site in the user record | message-persistence.feature:229 | 2 | message_persistence_steps_test.go | ◇ |

---

## History — request/reply (13 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 64 | `^"([^"]+)" requests history for room "([^"]+)"$` | Issues LoadHistory RPC on subject.MsgHistoryPattern with minimal LoadHistoryRequest{}; captures reply as LastResponse | history-service.feature:44,340 | 1 | history_steps_test.go | ◇ |
| 65 | `^"([^"]+)" requests next messages in room "([^"]+)"$` | Issues LoadNextMessages RPC on subject.MsgNextPattern with no cursor; captures reply as LastResponse | history-service.feature:59,319,325 | 1 | history_steps_test.go | ◇ |
| 66 | `^"([^"]+)" requests next messages in room "([^"]+)" with cursor "([^"]+)"$` | Issues LoadNextMessages RPC adding {"cursor":"\<cursor\>"} to the request; captures reply as LastResponse | history-service.feature:83 | 1 | history_steps_test.go | ◇ |
| 67 | `^"([^"]+)" requests surrounding messages with no message id in room "([^"]+)"$` | Issues LoadSurroundingMessages RPC with {"messageId":""}; captures reply as LastResponse | history-service.feature:103 | 1 | history_steps_test.go | ◇ |
| 68 | `^"([^"]+)" requests surrounding messages for unknown message in room "([^"]+)"$` | Issues LoadSurroundingMessages RPC with a generated never-persisted message ID; captures reply as LastResponse | history-service.feature:119 | 1 | history_steps_test.go | ◇ |
| 69 | `^"([^"]+)" requests message by id "" in room "([^"]+)"$` | Issues GetMessageByID RPC with {"messageId":""}; captures reply as LastResponse | history-service.feature:138,209 | 1 | history_steps_test.go | ◇ literal empty-string capture — verify godog handles `[^"]*` zero-or-more correctly |
| 70 | `^"([^"]+)" requests message by unknown id in room "([^"]+)"$` | Issues GetMessageByID RPC with a generated never-persisted message ID; captures reply as LastResponse | history-service.feature:154 | 1 | history_steps_test.go | ◇ |
| 71 | `^"([^"]+)" edits unknown message in room "([^"]+)" with empty content$` | Issues EditMessage RPC with a generated ID and newMsg:""; captures reply as LastResponse | history-service.feature:194 | 1 | history_steps_test.go | ◇ |
| 72 | `^"([^"]+)" deletes message "" in room "([^"]+)"$` | Issues DeleteMessage RPC with {"messageId":""}; captures reply as LastResponse | history-service.feature:228 | 1 | history_steps_test.go | ◇ literal empty-string capture — same godog caveat as step 69 |
| 73 | `^"([^"]+)" deletes unknown message in room "([^"]+)"$` | Issues DeleteMessage RPC with a generated never-persisted message ID; captures reply as LastResponse | history-service.feature:244 | 1 | history_steps_test.go | ◇ |
| 74 | `^"([^"]+)" requests thread parent messages in room "([^"]+)" with filter "([^"]+)"$` | Issues GetThreadParentMessages RPC with {"filter":"\<filter\>"}; captures reply as LastResponse | history-service.feature:263 | 1 | history_steps_test.go | ◇ |
| 75 | `^"([^"]+)" requests thread parent messages in room "([^"]+)"$` | Issues GetThreadParentMessages RPC with default filter (empty); captures reply as LastResponse | history-service.feature:279 | 1 | history_steps_test.go | ◇ |

> Note: history step 49 (`requests to edit message … with new content`) was merged from the near-duplicate history phrasing into step 49 above and is registered in messaging_steps_test.go, not here.

---

## Cassandra observation (Part-2) (7 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 76 | `^(?:within \d+s )?Cassandra table "([^"]+)" contains a row for message "([^"]+)" in room "([^"]+)"$` | Queries Cassandra by full primary key (room_id, bucket, created_at, message_id); step must compute bucket from created_at | message-persistence.feature:39,40,83 | 2 | message_persistence_steps_test.go | ◇ |
| 77 | `^(?:within \d+s )?Cassandra table "([^"]+)" contains a row for message "([^"]+)"$` | Simpler variant querying messages_by_id by (message_id, created_at) | message-persistence.feature:40,84 | 2 | message_persistence_steps_test.go | ◇ |
| 78 | `^(?:within \d+s )?the "([^"]+)" row for message "([^"]+)" has bucket equal to the expected time-window value for its created_at$` | Asserts bucket column equals floor(created_at_unix_ms / windowMs) * windowMs | message-persistence.feature:60 | 2 | message_persistence_steps_test.go | ◇ |
| 79 | `^(?:within \d+s )?the tcount for message "([^"]+)" in "([^"]+)" is (\d+)$` | Asserts LWT CAS tcount value in the named Cassandra table | message-persistence.feature:85,86 | 2 | message_persistence_steps_test.go | ◇ |
| 80 | `^exactly (\d+) row for message "([^"]+)" exists in Cassandra table "([^"]+)"$` | Asserts exact row count after idempotency test — must equal 1 after two identical publishes | message-persistence.feature:165,166 | 2 | message_persistence_steps_test.go | ◇ |
| 81 | `^(?:within \d+s )?the message-worker durable consumer sequence does not advance for the (updated\|deleted) event$` | Observes JetStream consumer state to assert FilterSubject excludes the given event type | message-persistence.feature:125,144 | 2 | message_persistence_steps_test.go | ◇ |
| 82 | `^(?:within \d+s )?the delivery count for message "([^"]+)" on the message-worker consumer is greater than (\d+)$` | Verifies NAK path causes JetStream redelivery; delivery count increments above N | message-persistence.feature:208 | 2 | message_persistence_steps_test.go | ◇ |

---

## MongoDB observation (Part-2) (3 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 83 | `^(?:within \d+s )?MongoDB collection "([^"]+)" contains a document with parentMessageId "([^"]+)"$` | Queries thread_rooms collection and asserts document existence | message-persistence.feature:103 | 2 | message_persistence_steps_test.go | ◇ |
| 84 | `^(?:within \d+s )?MongoDB collection "([^"]+)" contains a document for user "([^"]+)" and threadParent "([^"]+)"$` | Queries thread_subscriptions collection and asserts document for given user + thread parent | message-persistence.feature:104,105 | 2 | message_persistence_steps_test.go | ◇ |
| 85 | `^(?:within \d+s )?an OUTBOX event of type "([^"]+)" destined for site "([^"]+)" appears on the OUTBOX stream$` | JetStream stream-peek on OUTBOX_{siteID} and asserts event of given type present | message-persistence.feature:231; room-member-ops.feature:129,270 | 2 | message_persistence_steps_test.go | ◇ |

---

## Fault injection (Part-2) (2 steps)

| # | Regex | Purpose | Used in (file:line) | Part | File to add to | Notes |
|---|---|---|---|---|---|---|
| 86 | `^Cassandra is made unavailable$` | Chaos primitive: stops or network-partitions the Cassandra container via testcontainers | message-persistence.feature:207 | 2 | message_persistence_steps_test.go | ◇ |
| 87 | `^message "([^"]+)" by "([^"]+)" in room "([^"]+)" already persisted in Cassandra$` | Cassandra seed fixture: inserts a parent message row directly so thread-reply and edit/delete scenarios can reference it | message-persistence.feature:81,101,102,122,142,205 | 2 | message_persistence_steps_test.go | ◇ |

---

## Existing steps not subject to change (9 steps, listed for completeness)

| # | Regex | File |
|---|---|---|
| E1 | `^user "([^"]+)" is authenticated$` | auth_steps_test.go |
| E2 | `^user "([^"]+)" is authenticated in site "([^"]+)"$` | auth_steps_test.go |
| E3 | `^"([^"]+)" creates channel room "([^"]+)"$` | room_steps_test.go |
| E4 | `^"([^"]+)" requests role "([^"]+)" for member "([^"]+)" in room "([^"]+)"$` | role_steps_test.go |
| E5 | `^"([^"]+)" submits a message with (an empty body\|an invalid message id)$` | messaging_steps_test.go |
| E6 | `^"([^"]+)" requests thread messages with no thread message id in room "([^"]+)"$` | history_steps_test.go |
| E7 | `^"([^"]+)" requests thread messages for an unknown message in room "([^"]+)"$` | history_steps_test.go |
| E8 | `^the response is a (\w+) error$` | error_steps_test.go |
| E9 | `^the response is successful$` | error_steps_test.go |

---

## Summary

| Group | Final unique steps |
|---|---|
| Auth & users | 2 (both existing) |
| Rooms — create & info | 10 |
| Rooms — DM fixture & action | 2 |
| Room members — setup fixtures / Given | 7 |
| Rooms — MongoDB & multi-site fixtures | 2 |
| Room members — When verbs | 11 |
| Room members — Then assertions | 4 |
| Messaging — submit/edit/delete | 17 |
| Messaging — JetStream observation (Part-2) | 8 |
| History — request/reply | 12 |
| Cassandra observation (Part-2) | 7 |
| MongoDB observation (Part-2) | 3 |
| Fault injection (Part-2) | 2 |
| **TOTAL NEW UNIQUE** | **87** |

> The 2 Auth steps are existing registrations counted above for completeness. Net new steps to implement: **75** (87 − existing 9 + existing 2 shown individually, but Auth rows 1–2 are existing; the 9 existing steps in the table at the bottom are all not counted in the 87 — see breakdown below).

Corrected count (new steps only, excluding the 9 existing registrations):

| Group | New steps |
|---|---|
| Rooms — create & info (excluding existing `creates channel room`) | 9 |
| Rooms — DM fixture & action | 2 |
| Room members — setup fixtures / Given | 7 |
| Rooms — MongoDB & multi-site fixtures | 2 |
| Room members — When verbs | 11 |
| Room members — Then assertions | 4 |
| Messaging — submit/edit/delete | 17 |
| Messaging — JetStream observation (Part-2) | 8 |
| History — request/reply | 12 |
| Cassandra observation (Part-2) | 7 |
| MongoDB observation (Part-2) | 3 |
| Fault injection (Part-2) | 2 |
| **TOTAL NEW UNIQUE** | **84** |

Part-1 (implementable now, no new harness primitives needed): **36**
Part-2 (deferred — needs JetStream publish/peek, Cassandra query, MongoDB query, or fault-injection primitives): **48**

Part-1 breakdown by file:
- `room_steps_test.go` — 22 new steps (steps 4–12, 15–21, 24, 26–34)
- `messaging_steps_test.go` — 2 new steps (steps 49, 51)
- `history_steps_test.go` — 12 new steps (steps 64–75)

Part-2 breakdown by file:
- `room_steps_test.go` — 8 steps (steps 13–14, 17, 22–23, 25, 35–38)
- `messaging_steps_test.go` — 15 steps (steps 39–48, 50, 52–55)
- `message_persistence_steps_test.go` — 25 steps (steps 56–63, 76–87)

---

## What to do next

Register every step in the "Part 1" rows first: create or extend `room_steps_test.go`, `messaging_steps_test.go`, and `history_steps_test.go` with the 36 new step definitions, run `make -C tools/integration-suite local` to confirm the suite turns green on all non-blindspot scenarios, then open a follow-up task to implement the 48 Part-2 steps once the JetStream-publish, Cassandra-query, MongoDB-query, and fault-injection harness primitives exist in `tools/integration-suite/internal/harness/`.
