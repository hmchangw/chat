# Blindspots Register

Every `@blindspot:<slug>` used in any feature file under
`tools/integration-suite/features/` must have a matching level-2
heading (`## <slug>`) in this file.

Run `make integration-suite-lint` to verify consistency.

<!-- Entries follow this template:

## <slug>

**Found in:** features/<scope>/<feature>.feature
**Question:** What is the documented expected behavior for ...?
**Candidates:**
  - …
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future>.md
**Status:** open
**Added:** YYYY-MM-DD

-->

<!-- service: room-service -->

## room-add-member-to-dm

**Found in:** features/service/room-service.feature
**Question:** The scenario requires a pre-existing DM room created via
`handleCreateRoom`. The reply to create-room is `{"status":"accepted","roomId":"...","roomType":"dm"}`.
The suite currently has no step that stores the returned room ID from a create-room
reply and uses it in a subsequent step. Can the DM room ID be captured and used
as the subject parameter in the add-member request?
**Candidates:**
  - A new step `"([^"]+)" has created a DM room with "([^"]+)"` that calls
    `handleCreateRoom` and stores the returned `roomId` as a fixture.
  - Part 2 Mongo observation step that confirms the DM subscription and
    retrieves the room ID.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-dm-fixture-step>.md
**Status:** open
**Added:** 2026-05-21

## room-create-dm-already-exists

**Found in:** features/service/room-service.feature
**Question:** `natsCreateRoom` (room-service/handler.go::replyDMExists) returns a
non-standard success-shaped body `{"error":"dm already exists","roomId":"<id>"}`
rather than a `{"status":"accepted"}` reply. The suite classifier (`ClassifyNATS`)
uses `ParseNATSReplyError` which reads the `"error"` field — so it would classify
this as a `HandlerError`. However, the design intent is that the body carries the
existing room ID (idempotent open-or-create). What class should the suite assert:
`HandlerError` (error field present) or a domain-specific success?
**Candidates:**
  - Accept `HandlerError` and assert the `roomId` field separately (requires
    a new step that inspects the JSON body for a field value).
  - Treat `dm already exists` as a documented `HandlerError` assertion.
  - Add a dedicated `ClassDMExists` response class to the classifier.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-dm-exists-contract>.md
**Status:** open
**Added:** 2026-05-21

## room-create-missing-request-id

**Found in:** features/service/room-service.feature
**Question:** The integration harness (`natsRequest` in `nats_request_test.go`)
sets a `traceparent` header but does NOT set `X-Request-ID`. The handler's
`wrappedCtx` → `ContextWithRequestIDFromHeaders` returns `""` → `handleCreateRoom`
immediately returns `errMissingRequestID`. Can the test harness issue a NATS
message with that header absent and confirm the sentinel error is returned
without any room or user pre-seeding?
**Candidates:**
  - A new step variant of `natsRequest` that suppresses the `X-Request-ID` header
    explicitly (currently unused because the harness never sets it to begin with).
  - Verify `ContextWithRequestIDFromHeaders` behaviour: if it sources the ID from
    `traceparent`, the ID is always present and this guard is unreachable via
    the existing harness.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-05-12-integration-test-suite-part1.md (NATS harness extension)
**Status:** open
**Added:** 2026-05-21

## room-read-receipt-non-sender

**Found in:** features/service/room-service.feature
**Question:** The sender guard (`msgSender != requesterAccount → errNotMessageSender`)
requires the handler to look up the message in Cassandra via `GetMessageRoomAndCreatedAt`.
Exercising this path end-to-end requires a real message to have been written by a
known sender (requires the full message-worker pipeline, i.e. Part 2). Can this
guard be observed without a real message in Cassandra?
**Candidates:**
  - A stubbed message ID that is guaranteed to exist in Cassandra with a known
    sender — requires Part 2 Cassandra seeding primitive.
  - A Part 2 pipeline scenario: submit a message as "alice", wait for persistence,
    then query receipts as "bob" and assert the non-sender error.
  - Accept that this guard is only exercisable in the pipeline/ scope.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-cassandra-primitive>.md
**Status:** open
**Added:** 2026-05-21

## room-remove-member-org-only

**Found in:** features/service/room-service.feature
**Question:** Verifying the org-only guard (`hasOrgMembership && !hasIndividualMembership →
"org members cannot leave individually"`) requires a user whose sole membership
source in the room is an org `room_members` document. This state is produced by the
add-member-via-org flow in room-worker (async, JetStream). The Part 1 suite has no
primitive to seed `room_members` documents directly.
**Candidates:**
  - Part 2 Mongo seeding step: insert a `room_members` document with `type="org"`
    for the user's `sectId`, then issue the remove-member request.
  - An end-to-end journey: create a room with an org, wait for room-worker to
    materialize the org members, then attempt individual removal.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-mongo-seeding>.md
**Status:** open
**Added:** 2026-05-21

---

<!-- service: room-worker -->

## room-worker-add-member-outbox

**Found in:** features/pipeline/room-member-ops.feature
**Question:** When a newly added member's `SiteID` differs from the room's site,
room-worker publishes an `OutboxEvent{type:"member_added"}` to
`outbox.{room.SiteID}.to.{destSiteID}.member_added` (OUTBOX JetStream stream).
This is only observable by peeking at the OUTBOX stream or by running a second-site
inbox-worker and observing downstream state — both require Part-2 JetStream primitives.
**Candidates:**
  - Part-2 JetStream stream-peek on `OUTBOX_{siteID}` for a message with matching
    type and destination site.
  - Multi-site scenario: spin up remote site's inbox-worker, assert subscription
    created there; requires federation scope + Part-2 primitives.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-14-add-member-design.md §"Outbox for cross-site members"
**Status:** open
**Added:** 2026-05-21

## room-worker-add-member-persistence

**Found in:** features/pipeline/room-member-ops.feature
**Question:** After room-worker successfully processes an `add-member` event, a
subscription document is expected to exist in MongoDB for the new member
(`BulkCreateSubscriptions` per
`docs/superpowers/specs/2026-04-14-add-member-design.md §"Add Members (room-worker)"
step 2`). Can this be asserted without Part-2 Mongo observation primitives?
**Candidates:**
  - Part-2 MongoDB observation step that queries `subscriptions` by `(roomId, u.account)`.
  - Indirect: call the list-members NATS endpoint and assert the new member appears;
    this depends on a working list-members step which itself has its own blindspots.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-14-add-member-design.md
**Status:** open
**Added:** 2026-05-21

## room-worker-add-missing-request-id

**Found in:** features/pipeline/room-member-ops.feature
**Question:** `processAddMembers` in room-worker rejects events with a missing or
non-UUID `X-Request-ID` NATS header as a permanent error (Ack, not Nak), publishing
an `AsyncJobResult{status:"error"}` on the requester's response subject. The
integration suite's `natsRequest` helper always injects a valid UUID into the header,
so this validation path cannot be reached without a step that suppresses or corrupts
the `X-Request-ID` header.
**Candidates:**
  - Add a new step `"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"
    without a request ID` that bypasses the header injection and sends the JetStream
    publish directly (requires Part-2 JetStream publish primitive + pre-armed
    response-subject subscription).
  - Accept the gap: the unit test `TestProcessAddMembers_MissingRequestID` covers this
    branch; the integration suite may omit it.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-14-add-member-design.md
**Status:** open
**Added:** 2026-05-21

## room-worker-remove-member-persistence

**Found in:** features/pipeline/room-member-ops.feature
**Question:** After a successful self-leave, room-worker deletes the subscription
document from MongoDB (`DeleteSubscription` per
`docs/superpowers/specs/2026-04-14-remove-member-design.md §"Processing Order
(room-worker)" individual removal step 3a`). The deletion is only verifiable via a
Mongo observation primitive (Part-2).
**Candidates:**
  - Part-2 MongoDB observation step asserting no subscription doc with
    `(roomId, u.account)`.
  - Indirect: call list-members and assert the member is absent; depends on
    a working list-members step.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-14-remove-member-design.md §"Individual removal"
**Status:** open
**Added:** 2026-05-21

## room-worker-role-update-outbox

**Found in:** features/pipeline/room-member-ops.feature
**Question:** When the role-updated user's `SiteID` differs from the room's site,
room-worker publishes an `OutboxEvent{type:"role_updated"}` to
`outbox.{room.SiteID}.to.{user.SiteID}.role_updated`. This requires JetStream
stream-peek on the OUTBOX stream (Part-2).
**Candidates:**
  - Part-2 JetStream stream-peek on `OUTBOX_{siteID}` filtered by type and destination.
  - Multi-site federation scenario: assert inbox-worker on remote site updates the
    subscription there.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-13-role-update-design.md §"processRoleUpdate step 6"
**Status:** open
**Added:** 2026-05-21

## room-worker-role-update-persistence

**Found in:** features/pipeline/room-member-ops.feature
**Question:** After a role-update (promote to owner), room-worker calls `AddRole` then
re-reads the subscription; the updated subscription must carry the `owner` role in its
`Roles` array. Asserting this requires a Mongo observation step or a working
get-subscription endpoint (Part-2).
**Candidates:**
  - Part-2 MongoDB observation step querying `subscriptions` by `(roomId, u.account)`
    and asserting `roles` contains `"owner"`.
  - Indirect: call a get-subscription NATS endpoint if one exists.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-13-role-update-design.md §"processRoleUpdate"
**Status:** open
**Added:** 2026-05-21

---

<!-- service: message-gatekeeper -->

## gatekeeper-canonical-publish-needs-jetstream-primitive

**Found in:** features/pipeline/message-submission-validation.feature
**Question:** The gatekeeper's primary success contract — that a valid message
is accepted and a `MessageEvent{Event: "created"}` is published to
`MESSAGES_CANONICAL_<site>` on subject `chat.msg.canonical.<site>.created`
— requires observing the downstream JetStream stream. Can the suite verify
both the accepted reply *and* the canonical publish without a JetStream
observe/peek primitive?
**Candidates:**
  - Part-2 stream-peek primitive against `MESSAGES_CANONICAL_<site>` with a
    `Nats-Msg-Id` filter (the gatekeeper publishes with
    `jetstream.WithMsgID(msg.ID)`).
  - Part-2 JetStream publish step + pre-armed core-NATS subscription on
    `chat.user.<account>.response.<requestId>`, then assert the decoupled
    accepted reply.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-jetstream-primitive>.md
**Status:** open
**Added:** 2026-05-21

## gatekeeper-delete-sender-gate-requires-seeded-message

**Found in:** features/pipeline/message-submission-validation.feature
**Question:** The "only the sender can delete" gate in history-service fires
after loading the message from Cassandra. Verifying it end-to-end requires a
seeded Cassandra row with a known `sender.account`. Part-1 has no Cassandra
seeding primitive.
**Candidates:**
  - Part-2 Cassandra seeding step to insert a `messages_by_id` row authored
    by "alice", then "bob" attempts the delete.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-cassandra-seeding>.md
**Status:** open
**Added:** 2026-05-21

## gatekeeper-edit-empty-content-requires-seeded-message

**Found in:** features/pipeline/message-submission-validation.feature
**Question:** The `newMsg must not be empty` validation in history-service
fires after the subscription check (which itself requires the caller to be
subscribed). An end-to-end test of the content-validation path therefore
requires both a live subscription *and* a seeded message. Part-1 has neither
Cassandra seeding nor a subscription setup for the edit subject.
**Candidates:**
  - Part-2: seed a subscription via the member-add flow, seed a message in
    Cassandra, then send the edit request with an empty `newMsg`.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-cassandra-seeding>.md
**Status:** open
**Added:** 2026-05-21

## gatekeeper-edit-sender-gate-requires-seeded-message

**Found in:** features/pipeline/message-submission-validation.feature
**Question:** The "only the sender can edit" gate in history-service fires
*after* the message is loaded from Cassandra. Verifying it requires a seeded
Cassandra row with a known `sender.account`. The Part-1 suite has no Cassandra
seeding primitive.
**Candidates:**
  - Part-2 Cassandra seeding step to insert a `messages_by_id` row authored
    by "alice", then "bob" attempts the edit.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-cassandra-seeding>.md
**Status:** open
**Added:** 2026-05-21

## gatekeeper-large-room-cap-requires-mongo-seeding

**Found in:** features/pipeline/message-submission-validation.feature
**Question:** The large-room post restriction fires when
`room.UserCount > LARGE_ROOM_THRESHOLD` (default 500). Verifying this
requires a MongoDB `rooms` document with a `userCount` above the threshold.
The Part-1 suite has no Mongo seeding primitive. Additionally, the actual
rejection is delivered over the JetStream fire-and-forget reply path, which
also requires the Part-2 primitive.
**Candidates:**
  - Part-2 Mongo seeding step to insert a `rooms` document with
    `userCount: 501`.
  - Part-2 JetStream publish + decoupled response-subject assertion.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-mongo-seeding>.md
**Status:** open
**Added:** 2026-05-21

## gatekeeper-quote-thread-context-check-needs-jetstream-primitive

**Found in:** features/pipeline/message-submission-validation.feature
**Question:** The gatekeeper enforces the same-conversation-context quoting
rule (`snap.ThreadParentID != newMessageThreadID`) after an RPC to
history-service. The check fires during a JetStream-consumed message, so
verifying the rejection reply requires the Part-2 JetStream publish + decoupled
response-subject subscription primitive. Additionally, the history-service RPC
itself requires a seeded Cassandra message to exist.
**Candidates:**
  - Part-2 JetStream publish step + pre-armed subscription on the response
    subject, then assert the error reply text.
  - Part-2 Cassandra seeding primitive so a real parent message with a known
    thread context exists.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-jetstream-primitive>.md
**Status:** open
**Added:** 2026-05-21

## gatekeeper-siteid-mismatch-reply-unobservable

**Found in:** features/pipeline/message-submission-validation.feature
**Question:** When the gatekeeper detects a siteID mismatch it logs the error,
acks the JetStream message, and sends **no** reply to the client (because the
error is returned before `sendReply` is called with a non-validation error path
— see `handler.go:85-90`: the `infraError` branch nacks; the plain-error
branch replies; but `processMessage` returns `nil` for the siteID mismatch
case before the reply path is reached, and `sendReply` is called only on the
success path). The "no reply" contract cannot be verified without:
1. A JetStream publish primitive (Part 2) to inject a message with the wrong
   siteID into `MESSAGES_<site>`.
2. A timed absence-of-reply assertion on the response subject.
**Candidates:**
  - Part-2 JetStream publish step + timed subscription with a short deadline
    (e.g. 2s) asserting zero messages received on
    `chat.user.<account>.response.<requestId>`.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-jetstream-primitive>.md
**Status:** open
**Added:** 2026-05-21

---

<!-- service: message-worker -->

## msg-worker-bucket-partition-needs-cassandra-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** Does the `bucket` column in `messages_by_room` equal the
deterministic value `floor(created_at_unix_ms / windowMs) * windowMs`
where `windowMs = MESSAGE_BUCKET_HOURS * 3_600_000`?
**Candidates:**
  - Part-2 Cassandra observation primitive: read the `bucket` column
    from the persisted row and compare with computed expected value.
  - Separate service-level concern: verify `MESSAGE_BUCKET_HOURS`
    configuration matches across message-worker and history-service to
    prevent silent partition-mismatch data loss.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-05-05-message-partition-bucketing-design.md
**Status:** open
**Added:** 2026-05-21

## msg-worker-cassandra-error-naks-needs-jetstream-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** When Cassandra is unreachable (or returns an error),
does message-worker call `msg.Nak()` so that JetStream redelivers the
event? This is the documented failure path in `HandleJetStreamMsg`.
The design is silent on NAK-vs-ack-and-skip semantics for store errors
beyond the code comment.
**Candidates:**
  - Part-2 JetStream consumer-state observation: inject a Cassandra
    fault, publish a canonical event, observe that the consumer's
    delivered-message count exceeds 1 for the same sequence number.
  - Chaos injection (Part-2 resilience scope): temporarily make
    Cassandra unavailable and observe the consumer's delivery count.
**Owner:** TBD
**Target spec:** message-worker/handler.go § HandleJetStreamMsg (NAK branch)
**Status:** open
**Added:** 2026-05-21

## msg-worker-idempotency-needs-cassandra-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** When the same canonical created event is delivered twice
(JetStream redelivery simulation), does message-worker produce exactly
one Cassandra row per table (not two), relying on Cassandra's
INSERT-idempotency on the message primary key?
**Candidates:**
  - Part-2 Cassandra observation + JetStream redelivery injection:
    publish the same event twice (or force a NAK to trigger redelivery),
    then count rows in `messages_by_room` and `messages_by_id` by
    primary key.
**Owner:** TBD
**Target spec:** message-worker/store_cassandra.go § SaveMessage (UnloggedBatch comment)
**Status:** open
**Added:** 2026-05-21

## msg-worker-no-delete-handler-needs-canonical-filter-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** Is message-worker's consumer correctly ignoring canonical
`.deleted` events, given that history-service owns the synchronous
Cassandra soft-delete write and message-worker must not re-process
those events?
**Candidates:**
  - Part-2 JetStream consumer-state observation: same approach as the
    `.updated` case above.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-05-14-message-edit-delete-canonical-events-design.md § 3.4
**Status:** open
**Added:** 2026-05-21

## msg-worker-no-edit-handler-needs-canonical-filter-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** Is message-worker's durable JetStream consumer configured
with `FilterSubject: chat.msg.canonical.<siteID>.created` so that
canonical `.updated` events are NOT delivered to it? The design
explicitly requires this (D4 in the edit/delete canonical events spec)
to prevent accidental re-processing of edits as new message creates.
**Candidates:**
  - Part-2 JetStream consumer-state observation: inspect the consumer's
    `FilterSubject` configuration, or publish a `.updated` event and
    assert the consumer's delivered-sequence count does not advance.
  - Unit-test coverage (already in codebase): `consumer_config_test.go`
    verifies the filter; the integration question is whether the live
    consumer in a running stack is configured identically.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-05-14-message-edit-delete-canonical-events-design.md § D4
**Status:** open
**Added:** 2026-05-21

## msg-worker-order-independence-needs-cassandra-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** When two canonical create events with different `created_at`
timestamps (and therefore different bucket values) are delivered out of
order, do both messages land in their correct `(room_id, bucket)`
partitions independently, with no data corruption?
**Candidates:**
  - Part-2 Cassandra observation: publish two events with deliberately
    different timestamps (spanning bucket boundaries), deliver them in
    reversed order, then assert each row's `bucket` column equals
    `floor(created_at_unix_ms / windowMs) * windowMs`.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-05-05-message-partition-bucketing-design.md § Bucket scheme
**Status:** open
**Added:** 2026-05-21

## msg-worker-persist-canonical-needs-cassandra-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** After a valid `MessageEvent` is published to
`chat.msg.canonical.<site>.created`, does message-worker persist the
expected rows to both `messages_by_room` and `messages_by_id` in
Cassandra, with the correct column values (room_id, bucket, created_at,
message_id, sender UDT, msg, site_id, updated_at, mentions,
tshow, quoted_parent_message)?
**Candidates:**
  - Part-2 Cassandra observation primitive: query by primary key after
    a synthetic canonical publish, assert the returned row shape.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-09-message-worker-mongodb-setup-design.md
**Status:** open
**Added:** 2026-05-21

## msg-worker-thread-persist-needs-cassandra-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** When a canonical event carries a non-empty
`ThreadParentMessageID`, does message-worker:
  (a) insert a row into `thread_messages_by_room` with the correct
      composite partition key `(room_id, bucket)` and clustering
      columns `(thread_room_id, created_at, message_id)`?
  (b) atomically increment `tcount` on the parent row in both
      `messages_by_id` and `messages_by_room` using the Cassandra
      LWT CAS pattern (`IF tcount = ?`)?
**Candidates:**
  - Part-2 Cassandra observation primitive: seed a parent row, publish
    a thread-reply canonical event, then read both `tcount` and the
    `thread_messages_by_room` row.
**Owner:** TBD
**Target spec:** message-worker/store_cassandra.go § SaveThreadMessage + incrementParentTcount
**Status:** open
**Added:** 2026-05-21

## msg-worker-thread-room-mongodb-needs-mongo-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** Does the first thread reply cause message-worker to
create a `ThreadRoom` document in MongoDB and insert
`ThreadSubscription` documents for both the parent author and the
replier? On subsequent replies does it upsert rather than re-insert,
and does it update `LastMsgID` / `LastMsgAt` on the `ThreadRoom`?
**Candidates:**
  - Part-2 MongoDB observation primitive: assert document existence and
    field values in the `thread_rooms` and `thread_subscriptions`
    collections after a canonical thread-reply event is processed.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-28-message-worker-thread-subscription-outbox-design.md § Design
**Status:** open
**Added:** 2026-05-21

## msg-worker-thread-sub-outbox-needs-jetstream-primitive

**Found in:** features/pipeline/message-persistence.feature
**Question:** When a thread reply is processed and the replier's home
site differs from the room's home site, does message-worker publish an
outbox event of type `thread_subscription_upserted` to
`outbox.<site-a>.to.<site-b>.thread_subscription_upserted`? The dedup-ID
format is `thread-sub-outbox:{threadRoomID}:{userID}:{msgID}:{destSiteID}`;
is it stable across redeliveries?
**Candidates:**
  - Part-2 JetStream stream-peek on the OUTBOX stream: publish a
    canonical event with a remote-user replier (different siteID),
    peek the OUTBOX stream and assert the expected event type and
    payload.
  - Multi-site federation scope (Part-2 federation/): verify the event
    arrives at the destination inbox-worker and lands in
    `thread_subscriptions`.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-28-message-worker-thread-subscription-outbox-design.md § Outbox routing
**Status:** open
**Added:** 2026-05-21

---

<!-- service: history-service -->

## history-empty-thread-requires-subscription-fixture

**Found in:** features/service/history-service.feature
**Question:** When a subscribed caller requests thread messages for a thread-parent
message whose `thread_room_id` is empty (no replies have been created yet),
`history-service` returns `{messages: [], hasNext: false}` rather than an error.
Can the suite confirm this empty-result contract without a Cassandra + subscription
fixture?
**Design quote:**
> `history-service/internal/service/threads.go:44-53` — "if msg.ThreadRoomID == "" {
>   return &GetThreadMessagesResponse{Messages: []models.Message{}, HasNext: false}, nil }"
**Candidates:**
  - Part-2 Cassandra observation step: seed a parent message row in Cassandra with
    no `thread_room_id` column set, establish a subscription fixture, then assert the
    response body contains `messages: []` and `hasNext: false`.
  - Or: integration test at the `cassrepo` level (already covered by unit tests with
    mocked repos; Cassandra fixture would add an end-to-end assertion path).
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md
  § "Thread message query error / Success (thread with no replies)"
**Status:** open
**Added:** 2026-05-21

## history-forbidden-class-unverifiable

**Found in:** features/service/history-service.feature
  features/service/history-thread.feature
**Question:** When a caller requests thread messages for a room they are
not subscribed to, history-service replies `ErrForbidden("not subscribed
to room")`. The design (2026-04-21-history-get-thread-messages-design.md
§ Error Matrix) classifies this as `forbidden`. The suite's NATS
classifier (`classifier.go::ClassifyNATS`) is substring-based and the
sanitized error text contains no `forbidden`/`unauthorized`/`permission`
token, so it is bucketed `HandlerError`, not `Auth`. Can the documented
authorization-class rejection be confirmed by the suite?
**Candidates:**
  - Classifier should map the natsrouter `code:"forbidden"` field (it
    exists in the reply but `ClassifyNATS` currently ignores it) -> Auth.
  - Or: history-service error text should carry an auth keyword.
  - Or: accept HandlerError and treat the design's class as advisory.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-classifier-code-aware>.md
**Status:** open
**Added:** 2026-05-17

## history-ordering-requires-cassandra-fixture

**Found in:** features/service/history-service.feature
**Question:** The `LoadHistory` endpoint documents that messages are returned newest-first
(descending `created_at`). Can the suite assert the ordering of a multi-message response
without a Cassandra seed fixture?
**Design quote:**
> `docs/superpowers/specs/2026-03-25-refactor-history-service-design.md` § LoadHistory:
>   "GetMessagesBefore returns messages in a room before `before` and after `since`,
>   ordered newest-first."
**Candidates:**
  - Part-2 Cassandra seed: insert two or more messages with distinct `created_at`;
    assert response array has `messages[0].createdAt > messages[1].createdAt`.
  - Or: cassrepo integration test (already covers sort order for `GetMessagesBefore`).
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-03-25-refactor-history-service-design.md § LoadHistory
**Status:** open
**Added:** 2026-05-21

## history-pagination-requires-cassandra-fixture

**Found in:** features/service/history-service.feature
**Question:** The `LoadNextMessages` handler returns `HasNext=true` and a base64-encoded
`NextCursor` (Cassandra `PageState`) when more data exists beyond the current page.
Can the suite confirm multi-page cursor pagination — i.e., that a second request with
the returned cursor returns a non-overlapping subsequent page?
**Design quote:**
> `history-service/internal/service/messages.go` — LoadNextMessages:
>   "return &LoadNextMessagesResponse{Messages: page.Data, NextCursor: page.NextCursor, HasNext: page.HasNext}"
> `docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md` §
>   "Pagination | Limit below total → hasNext=true and a non-empty cursor; second call
>   with that cursor returns the remainder, no overlap, no gaps"
**Candidates:**
  - Part-2 Cassandra seed: insert N > page-size messages into a test room; obtain cursor
    from first response; confirm second response contains non-overlapping tail with no gaps.
  - Or: integration test in `cassrepo` (already exists for GetThreadMessages pagination;
    pattern can be extended to LoadNextMessages).
**Owner:** TBD
**Target spec:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md
  § Cassandra integration test, pagination case
**Status:** open
**Added:** 2026-05-21

---

<!-- service: other -->

## message-submit-needs-jetstream-primitive

**Found in:** features/pipeline/message-submission.feature
  features/pipeline/message-submission-validation.feature
**Question:** message-gatekeeper's documented validation contract
(2026-03-27-message-gatekeeper-design.md § Error Handling: invalid
message ID and empty content are rejected) is reached via a
fire-and-forget JetStream publish on `MESSAGES_<site>` with a *decoupled*
reply on `chat.user.<account>.response.<requestId>`. Part-1 has only HTTP
and NATS request/reply primitives (README § Status & maturity defers
JetStream consumer/peek to Part 2). How is the submission-validation
contract verified before the Part-2 primitive lands?
**Candidates:**
  - Part-2 JetStream publish step + pre-armed core-NATS subscription on
    the response subject, then assert the decoupled reply.
  - Part-2 stream-peek on MESSAGES_CANONICAL to assert accept/reject.
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future-jetstream-primitive>.md
**Status:** open
**Added:** 2026-05-17
