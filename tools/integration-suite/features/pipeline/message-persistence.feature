Feature: Messaging pipeline — message-worker persistence
  # Source: message-worker/handler.go § processMessage, SaveMessage, SaveThreadMessage
  # Source: message-worker/store_cassandra.go § CassandraStore (SaveMessage, SaveThreadMessage,
  #         incrementParentTcount, UpdateParentMessageThreadRoomID)
  # Source: docs/cassandra_message_model.md § Table / messages_by_room, messages_by_id,
  #         thread_messages_by_room
  # Source: docs/superpowers/specs/2026-05-05-message-partition-bucketing-design.md § Bucket scheme
  # Source: docs/superpowers/specs/2026-04-22-edit-message-design.md § Architecture (D4)
  # Source: docs/superpowers/specs/2026-04-22-delete-message-design.md § Architecture
  # Source: docs/superpowers/specs/2026-04-28-message-worker-thread-subscription-outbox-design.md § Design
  # Source: docs/superpowers/specs/2026-05-14-message-edit-delete-canonical-events-design.md § D4
  # Source: CLAUDE.md § "Bucketed message tables"
  #
  # message-worker has NO synchronous reply path observable in Part 1.
  # Its entire observable output is state-mutation in Cassandra (and MongoDB
  # for thread rooms/subscriptions) plus JetStream publishes to OUTBOX.
  #
  # All scenarios below require Part-2 primitives (Cassandra query observation,
  # JetStream stream-peek, or MongoDB observation) and are therefore tagged
  # @blindspot. They are drafted now to auto-resolve when Part 2 lands.

  # ---------------------------------------------------------------------------
  # Category 1: Persistence post-condition (canonical create → Cassandra rows)
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-persist-canonical-needs-cassandra-primitive
  Scenario: A canonical create event yields rows in messages_by_room and messages_by_id
    # Source: message-worker/store_cassandra.go § SaveMessage (UnloggedBatch inserting into
    #         both messages_by_room and messages_by_id with room_id, bucket, created_at,
    #         message_id, sender, msg, site_id, updated_at, mentions, type, sys_msg_data,
    #         tshow, quoted_parent_message).
    # Category: Persistence post-condition
    #
    # Trigger: a valid MessageEvent is published to chat.msg.canonical.<site>.created.
    # Post-condition (Part-2 Cassandra observe): the message row exists in
    #   messages_by_room and messages_by_id with the expected columns populated.
    Given user "alice" is authenticated
    When a canonical created event for message "msg-01" by "alice" in room "r1" is published to MESSAGES_CANONICAL
    Then within 5s Cassandra table "messages_by_room" contains a row for message "msg-01" in room "r1"
    And within 5s Cassandra table "messages_by_id" contains a row for message "msg-01"

  # ---------------------------------------------------------------------------
  # Category 2: Bucketing — partition key (room_id, bucket)
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-bucket-partition-needs-cassandra-primitive
  Scenario: The messages_by_room row lands in the correct time bucket derived from created_at
    # Source: docs/superpowers/specs/2026-05-05-message-partition-bucketing-design.md
    #         § Bucket scheme: bucket = (created_at_unix_ms / windowMs) * windowMs.
    # Source: docs/cassandra_message_model.md § messages_by_room PRIMARY KEY((room_id, bucket),
    #         created_at, message_id).
    # Source: CLAUDE.md § "Bucketed message tables": MESSAGE_BUCKET_HOURS must match
    #         across all services; mismatch silently loses data.
    # Category: Bucketing
    #
    # Post-condition: the bucket column in the persisted row equals
    #   floor(created_at_unix_ms / (MESSAGE_BUCKET_HOURS * 3_600_000)) * (MESSAGE_BUCKET_HOURS * 3_600_000).
    Given user "alice" is authenticated
    When a canonical created event for message "msg-02" by "alice" in room "r2" is published to MESSAGES_CANONICAL
    Then within 5s the "messages_by_room" row for message "msg-02" has bucket equal to the expected time-window value for its created_at

  # ---------------------------------------------------------------------------
  # Category 3: Thread row writes (thread replies → thread_messages_by_room)
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-thread-persist-needs-cassandra-primitive
  Scenario: A thread reply yields a row in thread_messages_by_room and increments the parent tcount
    # Source: message-worker/store_cassandra.go § SaveThreadMessage: inserts into
    #         messages_by_id and thread_messages_by_room, then calls incrementParentTcount
    #         (LWT CAS on both messages_by_id and messages_by_room of the parent row).
    # Source: message-worker/handler.go § processMessage: if ThreadParentMessageID != ""
    #         → handleThreadRoomAndSubscriptions → SaveThreadMessage.
    # Source: docs/cassandra_message_model.md § thread_messages_by_room PRIMARY KEY
    #         ((room_id, bucket), thread_room_id, created_at, message_id).
    # Category: Thread row writes
    #
    # Post-conditions: row in thread_messages_by_room; parent's tcount incremented by 1
    #   in both messages_by_id and messages_by_room.
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And message "parent-msg" by "alice" in room "r3" already persisted in Cassandra
    When a canonical created event for thread reply "reply-01" by "bob" to parent "parent-msg" in room "r3" is published to MESSAGES_CANONICAL
    Then within 5s Cassandra table "thread_messages_by_room" contains a row for message "reply-01"
    And within 5s the tcount for message "parent-msg" in "messages_by_id" is 1
    And within 5s the tcount for message "parent-msg" in "messages_by_room" is 1

  # ---------------------------------------------------------------------------
  # Category 3b: ThreadRoom and ThreadSubscription written to MongoDB
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-thread-room-mongodb-needs-mongo-primitive
  Scenario: The first thread reply creates a ThreadRoom document and ThreadSubscription for both parent author and replier
    # Source: message-worker/handler.go § handleFirstThreadReply: calls
    #         h.threadStore.CreateThreadRoom and h.threadStore.InsertThreadSubscription
    #         for the parent author and the replier (if distinct).
    # Source: docs/superpowers/specs/2026-04-28-message-worker-thread-subscription-outbox-design.md
    #         § Design / "handleFirstThreadReply".
    # Category: Thread row writes (MongoDB side)
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And message "parent-msg-02" by "alice" in room "r4" already persisted in Cassandra
    When a canonical created event for thread reply "reply-02" by "bob" to parent "parent-msg-02" in room "r4" is published to MESSAGES_CANONICAL
    Then within 5s MongoDB collection "thread_rooms" contains a document with parentMessageId "parent-msg-02"
    And within 5s MongoDB collection "thread_subscriptions" contains a document for user "alice" and threadParent "parent-msg-02"
    And within 5s MongoDB collection "thread_subscriptions" contains a document for user "bob" and threadParent "parent-msg-02"

  # ---------------------------------------------------------------------------
  # Category 4: Edit propagation (canonical .updated → NOT handled by message-worker)
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-no-edit-handler-needs-canonical-filter-primitive
  Scenario: message-worker ignores canonical updated events and does not write duplicate rows
    # Source: docs/superpowers/specs/2026-05-14-message-edit-delete-canonical-events-design.md
    #         § D4: message-worker's consumer FilterSubject is set to
    #         chat.msg.canonical.<siteID>.created — it does NOT consume .updated.
    # Source: message-worker/main.go § buildConsumerConfig: FilterSubject = subject.MsgCanonicalCreated(siteID).
    # Category: Edit propagation (non-consumption)
    #
    # Post-condition (JetStream-peek): a canonical .updated event published to
    #   MESSAGES_CANONICAL is not consumed by message-worker's durable consumer.
    #   No new row appears in Cassandra for the edit event alone.
    Given user "alice" is authenticated
    And message "msg-03" by "alice" in room "r5" already persisted in Cassandra
    When a canonical updated event for message "msg-03" by "alice" in room "r5" is published to MESSAGES_CANONICAL
    Then within 5s the message-worker durable consumer sequence does not advance for the updated event

  # ---------------------------------------------------------------------------
  # Category 5: Delete propagation (canonical .deleted → NOT handled by message-worker)
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-no-delete-handler-needs-canonical-filter-primitive
  Scenario: message-worker ignores canonical deleted events and does not process soft-deletes
    # Source: docs/superpowers/specs/2026-05-14-message-edit-delete-canonical-events-design.md
    #         § D4 and § 3.4: message-worker stays on .created only; history-service owns
    #         the synchronous Cassandra soft-delete write.
    # Source: message-worker/main.go § buildConsumerConfig: FilterSubject = subject.MsgCanonicalCreated(siteID).
    # Category: Delete propagation (non-consumption)
    #
    # Post-condition (JetStream-peek): a canonical .deleted event published to
    #   MESSAGES_CANONICAL is not delivered to message-worker's durable consumer.
    Given user "alice" is authenticated
    And message "msg-04" by "alice" in room "r6" already persisted in Cassandra
    When a canonical deleted event for message "msg-04" by "alice" in room "r6" is published to MESSAGES_CANONICAL
    Then within 5s the message-worker durable consumer sequence does not advance for the deleted event

  # ---------------------------------------------------------------------------
  # Category 6: Idempotency under JetStream redelivery
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-idempotency-needs-cassandra-primitive
  Scenario: JetStream redelivery of the same canonical create event does not produce duplicate Cassandra rows
    # Source: message-worker/store_cassandra.go § SaveMessage (UnloggedBatch): each INSERT
    #         is idempotent on its primary key — the PRIMARY KEY of messages_by_room is
    #         ((room_id, bucket), created_at, message_id) and messages_by_id is
    #         (message_id, created_at), so a duplicate publish writes the same row.
    # Source: docs/superpowers/specs/2026-04-09-message-worker-mongodb-setup-design.md
    #         § Error Handling: "Cassandra insert failure → NAK; idempotent retry acceptable
    #         since message_id is part of primary key."
    # Category: Idempotency under redelivery
    #
    # Post-condition: after two deliveries of the same event, exactly one row exists
    #   (Cassandra upsert semantics on primary key collision).
    Given user "alice" is authenticated
    When the canonical created event for message "msg-05" by "alice" in room "r7" is published to MESSAGES_CANONICAL twice
    Then within 5s exactly 1 row for message "msg-05" exists in Cassandra table "messages_by_room"
    And exactly 1 row for message "msg-05" exists in Cassandra table "messages_by_id"

  # ---------------------------------------------------------------------------
  # Category 7: Order independence — distinct messages across buckets
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-order-independence-needs-cassandra-primitive
  Scenario: Out-of-order delivery of distinct canonical create events does not corrupt partition layout
    # Source: docs/superpowers/specs/2026-05-05-message-partition-bucketing-design.md
    #         § Bucket scheme: bucket is derived deterministically from created_at, not from
    #         delivery order. Each message lands in its own bucket regardless of JetStream
    #         delivery sequence.
    # Source: CLAUDE.md § "Bucketed message tables": all services that read or write
    #         messages_by_room must use the same MESSAGE_BUCKET_HOURS.
    # Category: Order independence
    #
    # Post-condition: each message lands in its own correct bucket partition independently.
    Given user "alice" is authenticated
    When canonical created events for "msg-06" (created 48h ago) and "msg-07" (created now) are delivered out of order to MESSAGES_CANONICAL
    Then within 5s "msg-06" exists in Cassandra in the bucket for 48 hours ago
    And "msg-07" exists in Cassandra in the bucket for the current window

  # ---------------------------------------------------------------------------
  # Category 8: Handler error reporting — Cassandra unavailable → NAK
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-cassandra-error-naks-needs-jetstream-primitive
  Scenario: When Cassandra is unreachable message-worker NAKs the canonical event for redelivery
    # Source: message-worker/handler.go § HandleJetStreamMsg: if processMessage returns
    #         an error, msg.Nak() is called so JetStream redelivers the message.
    #         The code logs slog.Error("process message failed") and slog.Error("failed to nack
    #         message") on Nak failure.
    # Source: message-worker/store_cassandra.go § SaveMessage: returns
    #         fmt.Errorf("save message %s: %w", msg.ID, err) on ExecuteBatch failure.
    # Category: Handler error reporting
    #
    # Post-condition (JetStream-peek): the canonical event is NOT acked; its delivery count
    #   increases (JetStream redelivery semantics). Verifying the NAK is a Part-2 primitive
    #   (stream consumer state observation).
    Given user "alice" is authenticated
    And Cassandra is made unavailable
    When a canonical created event for message "msg-08" by "alice" in room "r8" is published to MESSAGES_CANONICAL
    Then within 5s the delivery count for message "msg-08" on the message-worker consumer is greater than 1

  # ---------------------------------------------------------------------------
  # Category 8b: Cross-site thread subscription outbox published for remote replier
  # ---------------------------------------------------------------------------

  @blindspot:msg-worker-thread-sub-outbox-needs-jetstream-primitive
  Scenario: A thread reply by a user whose home site differs from the room site triggers an OUTBOX event
    # Source: message-worker/handler.go § publishThreadSubOutboxIfRemote: publishes to
    #         subject.Outbox(h.siteID, ownerSiteID, "thread_subscription_upserted") when
    #         ownerSiteID != h.siteID. Dedup-ID format: "thread-sub-outbox:{threadRoomID}:{userID}:{msgID}:{destSiteID}".
    # Source: docs/superpowers/specs/2026-04-28-message-worker-thread-subscription-outbox-design.md
    #         § Outbox routing: one outbox event per (reply, affected user) when the user's
    #         home site differs from the room's home site.
    # Category: Handler error reporting / cross-site federation observable side-effect
    #
    # Post-condition (JetStream-peek on OUTBOX): an outbox event of type
    #   "thread_subscription_upserted" appears on outbox.{site-a}.to.{site-b}.thread_subscription_upserted.
    Given user "alice" from site "site-a" is authenticated
    And user "bob" from site "site-b" is authenticated
    And channel room "r9" is homed on site "site-a" with member "alice"
    And message "parent-msg-03" by "alice" in room "r9" already persisted in Cassandra
    When a canonical created event for thread reply "reply-03" by "bob" (home site "site-b") to parent "parent-msg-03" in room "r9" on site "site-a" is published to MESSAGES_CANONICAL
    Then within 5s an OUTBOX event of type "thread_subscription_upserted" destined for site "site-b" appears on the OUTBOX stream
