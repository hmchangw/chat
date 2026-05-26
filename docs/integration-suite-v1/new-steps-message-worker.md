# New Step Phrasings — message-worker persistence scenarios

All step phrasings below appear in
`features/pipeline/message-persistence.feature` and are NOT in the
current registered vocabulary (verified by running
`make -C tools/integration-suite steps`).

Each entry is listed with its Gherkin keyword, the regex that a step
definition file (`*_steps_test.go`) would need to register, and a brief
rationale for why it is new.

When Part-2 primitives land, these steps should be implemented in a new
`message_persistence_steps_test.go` (or similar) file at the top level
of `tools/integration-suite/`.

---

## Given steps

### 1. Canonical-event pre-seeded message in Cassandra

```
^message "([^"]+)" by "([^"]+)" in room "([^"]+)" already persisted in Cassandra$
```

**Rationale:** Needed to seed a parent message so thread-reply scenarios
can reference it. No existing step seeds Cassandra directly; this is a
Part-2 data-setup primitive.

### 2. Cassandra made unavailable (fault injection)

```
^Cassandra is made unavailable$
```

**Rationale:** Fault-injection setup step required by the Cassandra-error
/ NAK scenario (Category 8). This is a Part-2 chaos/resilience primitive.

### 3. User from a named site

```
^user "([^"]+)" from site "([^"]+)" is authenticated$
```

**Rationale:** Extends the existing `user "([^"]+)" is authenticated`
step with an explicit site parameter needed for multi-site outbox
federation scenarios.

### 4. Channel room homed on a site with a member

```
^channel room "([^"]+)" is homed on site "([^"]+)" with member "([^"]+)"$
```

**Rationale:** Setup for multi-site federation scenario; no existing step
establishes a room's home site explicitly.

---

## When steps

### 5. Canonical created event published to MESSAGES_CANONICAL

```
^a canonical created event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$
```

**Rationale:** Core trigger step. message-worker has no synchronous
request/reply; the only way to exercise it is to publish directly to
the `MESSAGES_CANONICAL_<site>` JetStream stream. This is a Part-2
JetStream-publish primitive. The step mirrors the existing
`"([^"]+)" submits a message with …` family but targets the
canonical stream directly (post-gatekeeper).

### 6. Canonical created event for a thread reply

```
^a canonical created event for thread reply "([^"]+)" by "([^"]+)" to parent "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$
```

**Rationale:** Thread-specific variant that includes `ThreadParentMessageID`.
The canonical `MessageEvent` must have `ThreadParentMessageID` set so
message-worker routes to `SaveThreadMessage` rather than `SaveMessage`.

### 7. Canonical created event published twice (idempotency)

```
^the canonical created event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL twice$
```

**Rationale:** Simulates JetStream redelivery by publishing the identical
payload twice. Required for Category 6 (idempotency). Part-2 JetStream
primitive.

### 8. Two canonical events delivered out of order

```
^canonical created events for "([^"]+)" \(created ([^)]+)\) and "([^"]+)" \(created ([^)]+)\) are delivered out of order to MESSAGES_CANONICAL$
```

**Rationale:** Out-of-order delivery simulation for Category 7 (order
independence). Requires Part-2 JetStream publish with controlled
timestamps and delivery sequencing.

### 9. Canonical updated event published to MESSAGES_CANONICAL

```
^a canonical updated event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$
```

**Rationale:** Publishes to `chat.msg.canonical.<site>.updated` to
verify that message-worker's `.created`-only FilterSubject excludes it.

### 10. Canonical deleted event published to MESSAGES_CANONICAL

```
^a canonical deleted event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$
```

**Rationale:** Publishes to `chat.msg.canonical.<site>.deleted` to
verify the same FilterSubject exclusion.

### 11. Canonical thread-reply event with cross-site replier

```
^a canonical created event for thread reply "([^"]+)" by "([^"]+)" \(home site "([^"]+)"\) to parent "([^"]+)" in room "([^"]+)" on site "([^"]+)" is published to MESSAGES_CANONICAL$
```

**Rationale:** Full cross-site variant that carries the replier's home
site in the user record so `publishThreadSubOutboxIfRemote` fires.

---

## Then steps

### 12. Cassandra row exists in a named table for a message+room

```
^(?:within \d+s )?Cassandra table "([^"]+)" contains a row for message "([^"]+)" in room "([^"]+)"$
```

**Rationale:** Core Part-2 Cassandra observation step. Not in vocabulary.
Queries by the full primary key `(room_id, bucket, created_at, message_id)`
which requires knowing the bucket; the step implementation must compute
the expected bucket from `created_at` using the configured window.

### 13. Cassandra row exists in messages_by_id for a message

```
^(?:within \d+s )?Cassandra table "([^"]+)" contains a row for message "([^"]+)"$
```

**Rationale:** Simpler variant without the room dimension, for
`messages_by_id` lookups (primary key is just `(message_id, created_at)`).

### 14. Bucket column equals expected time-window value

```
^(?:within \d+s )?the "([^"]+)" row for message "([^"]+)" has bucket equal to the expected time-window value for its created_at$
```

**Rationale:** Asserts the bucket arithmetic is correct. Requires the
step to compute `floor(created_at_unix_ms / windowMs) * windowMs` from
the known `created_at` and compare it to the stored `bucket` column.

### 15. tcount value in a specific Cassandra table

```
^(?:within \d+s )?the tcount for message "([^"]+)" in "([^"]+)" is (\d+)$
```

**Rationale:** Asserts the LWT CAS tcount increment succeeded for the
correct value. Needed for Category 3 thread-reply scenarios.

### 16. Exact row count in Cassandra table

```
^exactly (\d+) row for message "([^"]+)" exists in Cassandra table "([^"]+)"$
```

**Rationale:** Idempotency assertion — after two deliveries, row count
must be exactly 1 (not 2), demonstrating INSERT idempotency on primary
key collision.

### 17. MongoDB collection contains a document with a field value

```
^(?:within \d+s )?MongoDB collection "([^"]+)" contains a document with parentMessageId "([^"]+)"$
```

**Rationale:** Part-2 MongoDB observation step for ThreadRoom creation.

### 18. MongoDB thread_subscriptions row for a specific user+thread

```
^(?:within \d+s )?MongoDB collection "([^"]+)" contains a document for user "([^"]+)" and threadParent "([^"]+)"$
```

**Rationale:** Part-2 MongoDB observation for ThreadSubscription
creation (Category 3b).

### 19. message-worker consumer sequence did not advance for an event type

```
^(?:within \d+s )?the message-worker durable consumer sequence does not advance for the (updated|deleted) event$
```

**Rationale:** JetStream consumer-state observation — asserts the
FilterSubject excludes `.updated` / `.deleted`. Requires Part-2
JetStream consumer inspection primitive.

### 20. Delivery count on consumer greater than N

```
^(?:within \d+s )?the delivery count for message "([^"]+)" on the message-worker consumer is greater than (\d+)$
```

**Rationale:** Verifies the NAK path triggered by a Cassandra error causes
JetStream to redeliver the event, incrementing the delivery count above 1.

### 21. OUTBOX event of type X for destination site appears on stream

```
^(?:within \d+s )?an OUTBOX event of type "([^"]+)" destined for site "([^"]+)" appears on the OUTBOX stream$
```

**Rationale:** Part-2 JetStream stream-peek on OUTBOX. Verifies the
cross-site thread subscription outbox publish fired for a remote-user
replier (Category 8b).

---

## Summary counts

| Keyword | Count |
|---------|-------|
| Given   | 4     |
| When    | 7     |
| Then    | 10    |
| **Total** | **21** |

All 21 phrasings are new relative to the current registered vocabulary.
None conflict with or duplicate existing step patterns confirmed by
`make -C tools/integration-suite steps`.
