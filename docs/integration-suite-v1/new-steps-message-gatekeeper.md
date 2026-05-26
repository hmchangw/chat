# New Steps — message-gatekeeper validation feature

Steps used in
`tools/integration-suite/features/pipeline/message-submission-validation.feature`
that are **not** in the existing registered vocabulary
(verified against `make -C tools/integration-suite steps`).

For each step below: the regex pattern an implementer would register in a new
`*_steps_test.go` file, the scenario(s) that use it, and the transport /
primitive it requires.

---

## Messaging / submission steps

### 1. Submit a valid message

```
^"([^"]+)" submits a valid message to room "([^"]+)"$
```

**Used by:** CAT-1 happy-path scenario  
**Transport:** JetStream publish on `chat.user.<account>.room.<roomID>.<site>.msg.send`
(Part-2 primitive)  
**Notes:** Publishes a well-formed `SendMessageRequest` (20-char base62 ID, non-empty
content, no thread fields). Then pre-arms a core-NATS subscription on
`chat.user.<account>.response.<requestId>` to capture the decoupled reply.

---

### 2. Submit a message with oversized content

```
^"([^"]+)" submits a message with content larger than 20 KB to room "([^"]+)"$
```

**Used by:** CAT-4 oversized-body scenario  
**Transport:** JetStream publish (Part-2 primitive)  
**Notes:** Constructs a `content` field of `20*1024 + 1` bytes before publishing.

---

### 3. Accepted reply assertion

```
^the response is an accepted reply$
```

**Used by:** CAT-1 happy-path scenario  
**Transport:** reads the captured decoupled reply from the pre-armed core-NATS
subscription set up by step 1  
**Notes:** Asserts the reply payload unmarshals to a non-error `model.Message`
(or the accepted acknowledgement shape documented in the gatekeeper spec).

---

### 4. Canonical event existence assertion

```
^within ([0-9]+)s a canonical created event exists for the message in room "([^"]+)"$
```

**Used by:** CAT-1 happy-path scenario  
**Transport:** JetStream stream-peek on `MESSAGES_CANONICAL_<site>` filtered by
`Nats-Msg-Id == <messageID>` (Part-2 primitive)  
**Notes:** Asserts `MessageEvent.Event == "created"` and
`MessageEvent.Message.RoomID == <roomID>`.

---

### 5. Thread reply missing threadParentMessageCreatedAt

```
^"([^"]+)" submits a thread reply to room "([^"]+)" with threadParentMessageId set but no threadParentMessageCreatedAt$
```

**Used by:** CAT-5 missing-createdAt scenario AND CAT-8 large-room thread-reply-exempt scenario  
**Transport:** JetStream publish (Part-2 primitive)  
**Notes:** Publishes a `SendMessageRequest` where `threadParentMessageId` is a
valid 20-char base62 ID and `threadParentMessageCreatedAt` is absent/null.

---

### 6. Thread reply with malformed threadParentMessageId

```
^"([^"]+)" submits a thread reply to room "([^"]+)" with a malformed threadParentMessageId$
```

**Used by:** CAT-5 invalid-thread-parent-id scenario  
**Transport:** JetStream publish (Part-2 primitive)  
**Notes:** Publishes a `SendMessageRequest` where `threadParentMessageId` is not
a valid 20-char base62 string (e.g. a short UUID like `"bad-id"`).

---

### 7. Message quoting a different-thread parent

```
^"([^"]+)" submits a message to room "([^"]+)" quoting a message from a different thread$
```

**Used by:** CAT-5 cross-thread-quote scenario  
**Transport:** JetStream publish (Part-2 primitive) + Cassandra seeding  
**Notes:** Requires two seeded messages: one in thread A (parent) and one in
thread B. The new message is submitted inside thread A but quotes the thread-B
parent, triggering the `snap.ThreadParentID != newMessageThreadID` mismatch.

---

### 8. Main-room message quoting a thread reply

```
^"([^"]+)" submits a main-room message to room "([^"]+)" quoting a thread reply$
```

**Used by:** CAT-7 main-room-quoting-thread-reply scenario  
**Transport:** JetStream publish (Part-2 primitive) + Cassandra seeding  
**Notes:** The quoted message's `ThreadParentID` is non-empty; the new message
has no `threadParentMessageId`, so the check `snap.ThreadParentID != ""` fires.

---

### 9. Message quoting a non-existent ID

```
^"([^"]+)" submits a message to room "([^"]+)" quoting a non-existent message id$
```

**Used by:** CAT-7 quote-parent-not-found scenario  
**Transport:** JetStream publish (Part-2 primitive)  
**Notes:** Uses a well-formed but never-persisted 20-char base62 ID as
`quotedParentMessageId`. The history-service RPC returns NotFound; gatekeeper
rejects the send.

---

### 10. Message on behalf of a non-member

```
^"([^"]+)" submits a message to room "([^"]+)" on behalf of a non-member$
```

**Used by:** CAT-8 not-subscribed scenario  
**Transport:** JetStream publish (Part-2 primitive)  
**Notes:** Publishes on the actor's subject for a room the actor has no
subscription in. Gatekeeper's `GetSubscription` returns `errNotSubscribed`.

---

### 11. Large room existence setup

```
^a channel room "([^"]+)" exists with more than ([0-9]+) members$
```

**Used by:** CAT-8 large-room-blocked and large-room-thread-reply-exempt scenarios  
**Transport:** Mongo seeding (Part-2 primitive)  
**Notes:** Inserts a `rooms` document with `userCount` above the given threshold
directly into MongoDB, bypassing the member-add flow for speed.

---

### 12. Regular member assertion

```
^"([^"]+)" is a regular member of room "([^"]+)"$
```

**Used by:** CAT-8 large-room scenarios  
**Transport:** Mongo seeding (Part-2 primitive) or member-add flow  
**Notes:** Inserts a `subscriptions` document with roles `["member"]` so the
actor passes the subscription gate but not `canBypassLargeRoomCap`.

---

### 13. Top-level message submit (explicit, for large-room tests)

```
^"([^"]+)" submits a top-level message to room "([^"]+)"$
```

**Used by:** CAT-8 large-room-blocked scenario  
**Transport:** JetStream publish (Part-2 primitive)  
**Notes:** Same as step 1 but named distinctly to make large-room scenarios
self-documenting. No `threadParentMessageId` in the payload.

---

### 14. Edit message request

```
^"([^"]+)" requests to edit message "([^"]+)" in room "([^"]+)" with new content "([^"]+)"$
```

**Used by:** CAT-9 edit non-subscriber and edit non-sender scenarios  
**Transport:** NATS request/reply on
`chat.user.<account>.request.room.<roomID>.<site>.msg.edit` (Part-1 compatible)  
**Notes:** `messageId` is the literal fixture-generated ID passed as the
argument. `newMsg` is the fourth capture group.

---

### 15. Edit message with empty content

```
^"([^"]+)" requests to edit message "([^"]+)" in room "([^"]+)" with empty content$
```

**Used by:** CAT-9 edit-empty-content scenario  
**Transport:** NATS request/reply (Part-1 compatible)  
**Notes:** Sends `{"messageId": "<id>", "newMsg": ""}`. The subscription check
fires first, so for this to reach content validation the actor must be
subscribed — requires seeding.

---

### 16. Delete message request

```
^"([^"]+)" requests to delete message "([^"]+)" in room "([^"]+)"$
```

**Used by:** CAT-10 delete non-subscriber and delete non-sender scenarios  
**Transport:** NATS request/reply on
`chat.user.<account>.request.room.<roomID>.<site>.msg.delete` (Part-1 compatible)  
**Notes:** `messageId` is the literal fixture-generated ID. The subscription
check is the first gate; for non-subscriber tests this is all that is needed.

---

### 17. Wrong-site publish (siteID mismatch)

```
^"([^"]+)" publishes a message on a subject for site "([^"]+)" to room "([^"]+)"$
```

**Used by:** siteID-mismatch silent-drop scenario  
**Transport:** JetStream publish (Part-2 primitive)  
**Notes:** Publishes a well-formed message on a subject whose embedded siteID
token differs from the gatekeeper's configured `SITE_ID`, e.g.
`chat.user.<account>.room.<roomID>.wrong-site.msg.send`.

---

### 18. No-reply absence assertion

```
^no reply is received on the response subject$
```

**Used by:** siteID-mismatch silent-drop scenario  
**Transport:** timed absence-of-message check on the pre-armed core-NATS
subscription to `chat.user.<account>.response.<requestId>`  
**Notes:** Waits a short deadline (≤ 2 s) and asserts zero messages were
received. Requires the harness to distinguish "no message within deadline"
from "step not attempted".

---

## Summary

| # | Step regex (abbreviated) | Part | Transport |
|---|--------------------------|------|-----------|
| 1 | `submits a valid message to room` | Part-2 | JetStream publish |
| 2 | `submits a message with content larger than 20 KB` | Part-2 | JetStream publish |
| 3 | `the response is an accepted reply` | Part-2 | decoupled core-NATS sub |
| 4 | `within Ns a canonical created event exists` | Part-2 | JetStream stream-peek |
| 5 | `submits a thread reply … no threadParentMessageCreatedAt` | Part-2 | JetStream publish |
| 6 | `submits a thread reply … malformed threadParentMessageId` | Part-2 | JetStream publish |
| 7 | `quoting a message from a different thread` | Part-2 | JetStream + Cassandra seed |
| 8 | `main-room message … quoting a thread reply` | Part-2 | JetStream + Cassandra seed |
| 9 | `quoting a non-existent message id` | Part-2 | JetStream publish |
| 10 | `submits a message to room … on behalf of a non-member` | Part-2 | JetStream publish |
| 11 | `exists with more than N members` | Part-2 | Mongo seed |
| 12 | `is a regular member of room` | Part-2 | Mongo seed |
| 13 | `submits a top-level message to room` | Part-2 | JetStream publish |
| 14 | `requests to edit message … with new content` | Part-1 | NATS request/reply |
| 15 | `requests to edit message … with empty content` | Part-2 | NATS + subscription seed |
| 16 | `requests to delete message … in room` | Part-1 | NATS request/reply |
| 17 | `publishes a message on a subject for site` | Part-2 | JetStream publish |
| 18 | `no reply is received on the response subject` | Part-2 | timed absence check |

Steps 14 and 16 are Part-1 compatible (pure NATS request/reply). All others
require Part-2 primitives (JetStream publish, stream-peek, Mongo or Cassandra
seeding).
