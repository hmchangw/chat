# Scenario-Generation Prompt

This is the **lower-bound checklist** the integration suite holds every
service to, plus an **open-for-discovery** appendix where new failure
modes go as they are found. Use this when:

- asking an AI to author scenarios for a new (or changed) service
- reviewing an AI-authored scenario file before promotion
- deciding whether a service's coverage is "enough" before approval

> **Rule of thumb:** the lower bound is a starting set, not a stopping
> point. If you read the code and find a failure mode this prompt
> does not name, ADD A CATEGORY to the discovery appendix.

---

## How to use this as an AI prompt

Drop the block below into Claude / Cursor / Copilot. Fill in every
`<placeholder>`. The rest of the block is literal instruction.

```
You are authoring integration-test scenarios for <service>.

Read in order:
  1. <service>/handler.go and <service>/store.go
  2. <service>/main.go (for stream/subject config and consumer filter subjects)
  3. Every cited design spec under docs/superpowers/specs/ that names this service
  4. tools/integration-suite/AUTHORING.md (the rules)
  5. This file (the categories below)

Cover the LOWER BOUND below for every NATS handler or HTTP endpoint.
For each category that does not apply to a handler, write a comment:
  # N/A: <category name> — <one sentence why>

Tag each scenario with @blindspot:<slug> if no design source documents
the expected behavior, and add the slug to docs/integration-suite-v1/blindspots.md.
Tag each scenario with @covers:<slug> if a matching entry exists in
docs/integration-suite-v1/coverage.md.

EVERY scenario starts as DRAFT (no @status:approved).
EVERY scenario has a # Source: comment citing the design document + section.
EVERY scenario has a # Category: annotation naming the CAT-N below.

When you finish covering the lower bound, read the OPEN-FOR-DISCOVERY
appendix at the bottom of this file. If your reading of the code found
a failure mode the lower bound does not name, PROPOSE A NEW CATEGORY
at the end of this file with one example scenario stub and a note on
whether it should be promoted to the lower bound.
```

---

## The Lower Bound — categories every service must consider

### CAT-1 Happy path

**Definition:** At least one scenario where a valid, fully-authorized
request succeeds and the synchronous reply confirms acceptance. For
async services with no reply path, the happy path is a Part-2 blindspot
(state observation via Cassandra/Mongo/JetStream primitive).

**Examples:**
- `features/service/room-service.feature:108` — "Room member adds another user
  to an unrestricted channel" — replies successful.
- `features/pipeline/room-member-ops.feature:36` — "Owner adds a new member to
  a channel and room-service accepts the request" — reply is successful.
- `features/service/room-service.feature:255` — "A subscribed member can list
  the members of their room" — reply is successful.

**When N/A:** Only when the handler is purely fire-and-forget with no
synchronous reply path (e.g. message-worker). In that case, mark the
happy path as a Part-2 blindspot and document it under CAT-12.

**Common pitfalls:**
- Do not use hard-coded room or user IDs; generate them via `Given` fixture steps.
- Make sure the caller has the right subscription and role before issuing the
  request — otherwise the auth gate (CAT-7) fires instead of the happy path.

---

### CAT-2 Subject / URL parse error

**Definition:** The service parses a structured NATS subject or URL path
to extract parameters (roomID, siteID, account, messageID). A malformed
or missing token causes an immediate rejection before any business logic
runs.

**Examples:**
- `features/service/history-service.feature:94` — "Requesting surrounding
  messages with no message id" — subject parse produces missing field (though
  auth gate fires first in Part-1; see gate-order note below).
- `features/pipeline/room-member-ops.feature:53` — "Add-member event missing
  X-Request-ID is rejected permanently by room-worker" — header parse failure.

**When N/A:** If the service's subject has no variable tokens (e.g. a
fixed subject like `chat.server.info`), this category does not apply.

**Common pitfalls:**
- In NATS services, missing X-Request-ID is a subject/header parse error,
  not a body validation error. It is typically caught before any DB call.
- The suite's `natsRequest` helper automatically injects X-Request-ID;
  to test the missing-header case you must suppress it, which may require
  a new step primitive (tag `@blindspot` until the primitive exists).

---

### CAT-3 Request ID missing or malformed

**Definition:** Every handler that reads X-Request-ID from the NATS
header (or HTTP header) must reject requests where it is absent or not
a valid 36-char hyphenated UUID.

**Examples:**
- `features/service/room-service.feature:51` — "Create room with no
  X-Request-ID header is rejected" — `@blindspot:room-create-missing-request-id`.
- `features/pipeline/room-member-ops.feature:53` — "Add-member event missing
  X-Request-ID is rejected permanently by room-worker" —
  `@blindspot:room-worker-add-missing-request-id`.

**When N/A:** Handlers that do not require a request ID (e.g. read-only
RPCs that only use the NATS reply subject). Check handler.go for
`errMissingRequestID` or equivalent before deciding.

**Common pitfalls:**
- The suite harness always injects a valid request ID. You need an explicit
  step that suppresses it to exercise this branch — tag `@blindspot` until
  that step exists.
- The 32-char no-hyphen form is NOT a valid request ID (that format is
  reserved for Mongo `_id`s). Submitting it should be treated the same as
  missing.

---

### CAT-4 Malformed request body

**Definition:** The handler calls `json.Unmarshal` (or equivalent) on
the NATS payload. An unparseable payload (bad JSON, wrong type for a
field, truncated bytes) must produce a synchronous error reply without
panicking or entering business logic.

**Examples:**
- `features/pipeline/room-member-ops.feature:68` — "Adding members to a DM
  room is rejected by room-service" (body parses fine but wrong room type —
  strictly CAT-10; the pure malformed-JSON case is typically a blindspot
  until the suite has a raw-bytes step).
- `docs/integration-suite-v1/coverage.md: room-roleupdate-invalid-request` —
  "An unparseable UpdateRoleRequest payload is rejected" — Status: todo.

**When N/A:** Fire-and-forget JetStream consumers that do not reply — the
malformed-body path still NAKs the message, which is a Part-2 observation.

**Common pitfalls:**
- Sending `{}` is valid JSON and may pass unmarshal but fail field validation
  (CAT-5). Use truly malformed bytes (e.g. `not-json`) to test this category.
- Malformed-body errors often land as `HandlerError` class in the classifier,
  not `Validation error` — check the handler's reply path before asserting.

---

### CAT-5 Field validation — missing required / invalid value / out-of-range

**Definition:** The handler validates individual fields after a successful
unmarshal: missing required fields, empty strings, invalid enum values,
out-of-range integers. Each distinct validation rule is a separate scenario.

**Examples:**
- `features/service/room-service.feature:71` — "Creating a room with no users,
  orgs, channels, or name is rejected" — `errEmptyCreateRequest`.
- `features/service/room-service.feature:325` — "Read receipt request with an
  empty message ID is rejected."
- `features/service/room-service.feature:224` — "Remove member with neither
  account nor orgId is rejected."
- `features/pipeline/message-submission-validation.feature:232` — "Non-subscriber
  cannot edit a message ... with empty newMsg" — `ErrBadRequest("newMsg must not be empty")`.

**When N/A:** If the handler has no per-field validation beyond unmarshalling
(only a few simple read RPCs fall here).

**Common pitfalls:**
- **Gate order matters.** If the handler calls a subscription/auth check BEFORE
  field validation (history-service does this for every handler except
  GetThreadMessages), then sending an invalid field to an unsubscribed caller
  exercises the auth gate, not field validation. Either set up a subscription
  fixture (Part-2) or tag `@blindspot` with a note. See CAT-7 note below.
- Use `# N/A: CAT-5 — handler has no field-level validation beyond unmarshal`
  only after reading handler.go line-by-line.

---

### CAT-6 Field boundary — at-limit / one-over / one-under

**Definition:** Numeric or string fields with documented size/range limits
require boundary tests: exactly at the limit (accepted), one over (rejected),
and where meaningful one under (accepted).

**Examples:**
- `features/service/room-service.feature:62` — "Creating a channel with a name
  longer than 100 runes is rejected" — 101 runes (`maxChannelNameRunes = 100`).
- `features/service/room-service.feature:266` — "Listing members with a zero
  limit is rejected" — `limit must be > 0`.
- `features/service/room-service.feature:290` — "RoomsInfoBatch with too many
  room IDs is rejected" — 1001 IDs (`MAX_BATCH_SIZE`).
- `features/pipeline/message-submission-validation.feature:62` — "Message with
  content exceeding 20 KB is rejected" — `maxContentBytes = 20 * 1024`.

**When N/A:** If no field has a numeric or length constraint documented in the
spec or source code.

**Common pitfalls:**
- "At-limit" tests (exactly 100 runes, exactly `MAX_BATCH_SIZE` IDs) are
  accepted, not rejected — write them as happy-path sub-scenarios.
- Limits sourced only from code constants (not from a spec) still require a
  `# Source:` citing the constant location in the source file.

---

### CAT-7 Authentication — caller has no valid credential or token

**Definition:** The service requires a valid NATS JWT or HTTP credential.
A request with no credential, an expired token, or an invalid nkey
signature must be rejected at the transport or auth-callout layer before
the handler executes.

**Examples:**
- Not explicitly featured in the 5 files (the harness enforces valid
  credentials for all `natsRequest` calls). Unauthenticated NATS connection
  rejection is enforced by the auth-callout service — covered in
  `features/service/auth-service.feature`.
- For HTTP (auth-service), missing `Authorization` header or invalid JWT
  is the canonical example.

**When N/A:** Internal server-to-server subjects (see Architectural Truth #5)
that use service credentials rather than user JWTs — the "no credential"
case is a different primitive. Mark as `@blindspot` until the suite has
a service-credential step.

**Common pitfalls:**
- Do not confuse authentication (no/invalid credential) with authorization
  (valid credential but wrong permission — that is CAT-8).
- The suite's `natsRequest` always uses a valid user JWT. Testing the
  unauthenticated path requires opening a raw NATS connection without a
  credential — a harness gap to be noted as `@blindspot`.

---

### CAT-8 Authorization — caller has credential but lacks permission

**Definition:** The caller is authenticated but does not have the required
role or membership for the specific operation. Common forms: non-member
tries to act on a room, non-owner tries an owner-only operation, caller
acts on a resource owned by another account.

**Examples:**
- `features/service/room-service.feature:121` — "A non-owner member cannot add
  to a restricted channel."
- `features/service/room-service.feature:209` — "A non-owner cannot remove
  another member from a channel."
- `features/pipeline/room-member-ops.feature:81` — "Non-owner cannot add members
  to a restricted channel" — reply is HandlerError.
- `features/pipeline/room-member-ops.feature:151` — "Non-owner cannot remove
  another member from a channel."
- `features/service/room-service.feature:312` — "A non-member cannot query
  read receipts for a message."

**When N/A:** Handlers that have no role-based access control (e.g. public
read endpoints, server-to-server RPCs with blanket service-credential gate).

**Common pitfalls:**
- History-service gates on subscription (a form of authorization) BEFORE
  field validation, so every scenario without a subscription fixture produces
  an Auth error regardless of what field values are sent. This is expected —
  document it with `@blindspot:history-forbidden-class-unverifiable` and a
  note that Part-2 subscription fixtures will unlock the field-validation branch.
- Sanitized error text (Architectural Truth #1) may prevent the classifier
  from distinguishing `Auth error` from `HandlerError`. When this is the case,
  tag the scenario with a `class-unverifiable` blindspot slug.

---

### CAT-9 Existence — referenced resource is missing

**Definition:** The request references a room, user, message, or subscription
that does not exist. The handler must return a clear error (not a panic or a
wrong-type error) and must not create the resource as a side effect.

**Examples:**
- `features/pipeline/room-member-ops.feature:96` — "Adding a member to a
  non-existent room is rejected" — HandlerError (subscription check fails
  because no subscription to a non-existent room exists).
- `features/service/room-service.feature:297` — "RoomsInfoBatch for an unknown
  room returns Found=false for that entry" — partial-success semantics: the
  reply is successful but the entry carries `found: false`.
- `features/pipeline/message-submission-validation.feature:149` — "Quoting a
  non-existent parent message fails the send."

**When N/A:** Handlers that have no existence checks (e.g. create operations
where the resource must not already exist — those belong in CAT-10 instead).

**Common pitfalls:**
- Some handlers return partial-success for missing resources (batch RPCs,
  quote resolution) rather than a top-level error. Check the spec's "Response
  semantics" section before assuming a missing resource is a top-level error.
- Missing subscription and missing room often look identical to the handler
  (both produce `ErrSubscriptionNotFound`) — write separate scenarios to
  document both shapes if they are distinguished in the spec.

---

### CAT-10 State preconditions — operation invalid given current state

**Definition:** The resource exists, the caller is authorized, but the
current state of the system makes the operation invalid. Examples: last
owner cannot leave or demote themselves, DM already exists, bot cannot
join a channel, self-DM is forbidden, org-only member cannot leave
individually.

**Examples:**
- `features/service/room-service.feature:36` — "Creating a DM with yourself
  is rejected" — `errSelfDM`.
- `features/service/room-service.feature:82` — "Adding a bot account to a
  channel create request is rejected" — `errBotInChannel`.
- `features/service/room-service.feature:196` — "The last owner cannot remove
  themselves from a channel" — last-owner guard.
- `features/pipeline/room-member-ops.feature:163` — "The last owner cannot be
  removed from a channel."
- `features/pipeline/room-member-ops.feature:234` — "The last owner cannot
  self-demote."

**When N/A:** Purely stateless handlers (e.g. a simple read RPC with no
state machine) or handlers whose state machine is not documented in any spec.

**Common pitfalls:**
- State preconditions often require fixture setup (creating a room, adding
  members, setting restricted flag) — count the `And` steps in the `Given`
  block; more than 3-4 is a signal the scenario may depend on Part-2 Mongo
  seeding and should be tagged `@blindspot`.
- Self-DM and DM-already-exists are distinct state preconditions — write
  separate scenarios (see CAT-11 for idempotency).

---

### CAT-11 Idempotency — repeating the same operation

**Definition:** Some operations are explicitly specified as idempotent:
repeating them must yield the same outcome as the first call without
producing a duplicate resource or an error on the repeat.

**Examples:**
- `features/service/room-service.feature:92` — "Creating a DM that already
  exists returns the existing room ID" — `dmExistsError` special-cases to
  return `{"error":"dm already exists","roomId":"<existing>"}`.
- `features/pipeline/message-persistence.feature:151` — "JetStream redelivery
  of the same canonical create event does not produce duplicate Cassandra rows"
  — Cassandra primary key upsert semantics.

**When N/A:** Operations whose specs explicitly say a repeat is an error
(e.g. promoting a user who is already an owner is rejected — that is CAT-10,
not CAT-11).

**Common pitfalls:**
- Idempotency of JetStream consumers (redelivery) is always a Part-2
  blindspot (requires Cassandra observation). Tag accordingly.
- The DM-already-exists special case returns a non-standard response shape
  (error string + room ID in the same body) — the assertion step must check
  for that specific shape, not a generic success or Validation error.

---

### CAT-12 Post-condition / state observation (Part-2)

**Definition:** The write the handler performed actually landed in the
persistent store (Mongo collection, Cassandra table). These scenarios
require the Part-2 Mongo/Cassandra observation step primitives and are
tagged `@blindspot` until those primitives exist.

**Examples:**
- `features/pipeline/room-member-ops.feature:108` — "After add-member succeeds
  a subscription document is written for the new member" —
  `@blindspot:room-worker-add-member-persistence`.
- `features/pipeline/room-member-ops.feature:178` — "After a successful
  self-leave the subscription document is deleted" —
  `@blindspot:room-worker-remove-member-persistence`.
- `features/pipeline/message-persistence.feature:27` — "A canonical create
  event yields rows in messages_by_room and messages_by_id" —
  `@blindspot:msg-worker-persist-canonical-needs-cassandra-primitive`.
- `features/pipeline/room-member-ops.feature:247` — "After a successful role
  promotion the subscription carries the owner role" —
  `@blindspot:room-worker-role-update-persistence`.

**When N/A:** Fire-and-forget services where the synchronous reply is the only
observable output (these have no state to observe in the current reply); or
pure read handlers that do not mutate state.

**Common pitfalls:**
- Draft these scenarios now even though they are Part-2 blindspots — they
  serve as a living contract that auto-resolves when the primitives land.
- Always include both the synchronous reply assertion (`Then the response is
  successful`) and the async state assertion (`And within 5s ...`) in the
  same scenario. The timing budget for async assertions is 5s by convention.

---

### CAT-13 Downstream propagation — canonical publish or outbox event (Part-2)

**Definition:** After the handler processes a request, it publishes a
downstream event (canonical MESSAGES_CANONICAL publish, ROOMS stream event,
OUTBOX cross-site event). These are observable only via JetStream primitives
(Part-2) and are tagged `@blindspot` until those primitives exist.

**Examples:**
- `features/pipeline/room-member-ops.feature:123` — "Adding a cross-site member
  produces an outbox event" — `@blindspot:room-worker-add-member-outbox`.
- `features/pipeline/room-member-ops.feature:261` — "A role update for a
  cross-site member produces an outbox event" —
  `@blindspot:room-worker-role-update-outbox`.
- `features/pipeline/message-persistence.feature:214` — "A thread reply by a
  user whose home site differs triggers an OUTBOX event" —
  `@blindspot:msg-worker-thread-sub-outbox-needs-jetstream-primitive`.
- `features/pipeline/message-submission-validation.feature:38` — "Valid message
  submission is accepted and canonical event is published" —
  `@blindspot:gatekeeper-canonical-publish-needs-jetstream-primitive`.

**When N/A:** Handlers that are pure reads (no downstream event), or services
that consume events but do not republish (these have no propagation to verify).

**Common pitfalls:**
- Distinguish between the synchronous acceptance reply (CAT-1, observable in
  Part-1) and the downstream event (CAT-13, Part-2 only). Do not collapse them
  into one scenario unless both are observable in the same Part.
- Cross-site outbox events require multi-site fixture setup (a second site's
  NATS URL); tag them appropriately and check the scope decision tree —
  cross-site propagation scenarios belong in `features/federation/`.

---

## Architectural truths to know before authoring

These are non-obvious facts that the five initial agents surfaced. Do not
re-discover them each time.

**1. Sanitized error text breaks class assertions.**
Services call `sanitizeError(err)` before `natsutil.ReplyError`. The NATS
classifier pattern-matches on keywords ("forbidden", "not found",
"validation") that may not survive sanitization. When the error text is
sanitized, the reply lands as a generic `HandlerError` class instead of
`Auth error` or `Validation error`. When this matters, register a
`<svc>-<op>-class-unverifiable` blindspot. Do NOT assert on specific error
strings — assert on the response class, and if even the class is unreliable,
assert `@blindspot`.

**2. Gate order matters — subscription before field validation.**
History-service handlers call `getAccessSince` (subscription check) BEFORE
any field validation. A "test missing field X" scenario aimed at an
unsubscribed caller exercises the auth gate (CAT-7), not field validation
(CAT-5). The test still has value — it documents the observable behavior —
but mark it `# Category: CAT-5 (CAT-7 observed in Part-1)` and tag
`@blindspot:history-forbidden-class-unverifiable`. Part-2 subscription
fixtures will unlock the field-validation branch. Room-service is the
counterexample: it validates some fields (empty request, bot accounts, self-DM)
before the subscription check.

**3. Fire-and-forget services have no Part-1 observable output.**
message-worker has NO synchronous reply path. Its entire output is
Cassandra rows, MongoDB documents, and JetStream OUTBOX publishes. Every
contract is a Part-2 blindspot (CAT-12 or CAT-13). message-gatekeeper's
canonical publish to MESSAGES_CANONICAL is similarly Part-2-only — the
gatekeeper does reply synchronously to the client, but the canonical event
is a downstream side-effect observable only via JetStream.

**4. FilterSubject scoping means some workers ignore certain event types.**
message-worker's durable consumer is configured with
`FilterSubject = subject.MsgCanonicalCreated(siteID)` — it explicitly
does NOT consume `.updated` or `.deleted` events. Document that the worker
does not react to excluded events with a non-consumption scenario tagged
`@blindspot:<svc>-filter-non-consumption-needs-jetstream-primitive`.
Always read `main.go`'s consumer config before assuming a worker handles
all event types on a stream.

**5. Server-to-server subjects use service credentials, not user JWTs.**
Subjects like `chat.server.request.room.{siteID}.info.batch` are reached
with service credentials, not user nkey JWTs. The suite's `natsRequest`
helper opens a user connection and cannot exercise these subjects in Part-1.
Scenarios requiring service credentials are `@blindspot` until the harness
gains a service-credential primitive.

**6. siteID-mismatch causes a silent ack-and-drop, not an error reply.**
message-gatekeeper drops messages where the published siteID does not match
its own config — it acks the JetStream message and logs an error but sends
NO reply to the client (not even an error reply). This is distinct from a
validation rejection. The "no reply" contract is observable only via
JetStream stream-peek (Part-2). Tag such scenarios
`@blindspot:gatekeeper-siteid-mismatch-reply-unobservable`.

**7. Batch RPCs use partial-success semantics, not all-or-nothing.**
RoomsInfoBatch returns a `Found=false` entry for each unknown room ID rather
than a top-level error. The reply class is `successful` even when some entries
are not found. Always check the spec's "Response semantics" section for batch
endpoints before writing CAT-9 assertions.

---

## Open for discovery (the living appendix)

When authoring scenarios for a new service or feature, you will sometimes
find a failure mode that does not fit any category above. When that happens,
**ADD A NEW SECTION HERE.** Do not squeeze it into an existing category —
surface it as a first-class new thing.

Format:

```
### DISC-<YYYY-MM-DD>-<slug>  <Name>

**Discovered in:** <service> while authoring <feature file>.
**Pattern:** <1-2 sentences describing the failure shape>.
**Example:** <one scenario citation or stub>.
**Should this become a CAT in the lower bound?** <YES/NO + rationale>.
```

---

### DISC-2026-05-21-sanitized-error-class  Sanitized error text makes class assertions unreliable

**Discovered in:** history-service while authoring
`features/service/history-service.feature`.

**Pattern:** Services call `sanitizeError(err)` before `natsutil.ReplyError`.
The NATS response classifier pattern-matches on substrings ("forbidden",
"mongo", "not found") that may not survive sanitization, causing a genuine
`ErrForbidden` to arrive at the classifier as a bare `HandlerError`. Every
history-service "not subscribed" scenario is affected — the suite observes
`Auth error` class because the handler wraps `ErrForbidden`, but if sanitization
strips the keyword the assertion would fail even though the system did the
right thing. Registered blindspot: `history-forbidden-class-unverifiable`.

**Example:** `features/service/history-service.feature:33` — every scenario
tagged `@blindspot:history-forbidden-class-unverifiable`.

**Should this become a CAT?** NO. It is a meta-pattern affecting CAT-7, CAT-8,
and CAT-9 in many services. Documented as Architectural Truth #1 instead of
a standalone lower-bound category.

---

### DISC-2026-05-21-fire-and-forget-no-part1  Fire-and-forget service with no synchronous reply

**Discovered in:** message-worker while authoring
`features/pipeline/message-persistence.feature`.

**Pattern:** message-worker subscribes to a JetStream stream and either acks
(on success) or naks (on error). There is no NATS request/reply subject and
no synchronous client-facing response. Every observable output — Cassandra
rows, MongoDB documents, OUTBOX events — requires Part-2 observation
primitives. A Part-1 scenario for this service can only describe the absence
of a reply (itself unverifiable without a raw connection). All 10 scenarios
in the file are tagged `@blindspot`.

**Example:** `features/pipeline/message-persistence.feature:26` — every
scenario uses `@blindspot:msg-worker-*-needs-*-primitive`.

**Should this become a CAT?** NO. It is a service architecture pattern, not
a failure mode. Documented as Architectural Truth #3. When authoring for a
fully async service, skip CAT-1 through CAT-11 for the synchronous-reply
dimension and write only CAT-12 and CAT-13 stubs (all tagged `@blindspot`).

---

### DISC-2026-05-21-siteid-mismatch-nil-reply  siteID mismatch produces nil reply, not an error reply

**Discovered in:** message-gatekeeper while authoring
`features/pipeline/message-submission-validation.feature`.

**Pattern:** message-gatekeeper checks that the siteID embedded in the NATS
subject matches its own configured siteID. On mismatch it logs an error and
acks the JetStream message but sends NO reply to the decoupled response subject.
The client therefore receives nothing — not an error, not a success. This is
distinct from a validation rejection (which does produce an error reply) and
from a silent-drop without ack. The "no reply" contract is unverifiable in
Part-1 without a JetStream primitive to confirm the message was acked.

**Example:** `features/pipeline/message-submission-validation.feature:320` —
"Message published on a mismatched siteID subject is silently acked with no
reply" — `@blindspot:gatekeeper-siteid-mismatch-reply-unobservable`.

**Should this become a CAT?** YES — consider adding a lower-bound category
for "silent-drop / nil-reply paths" in services that mix synchronous and
async reply patterns. The distinguishing question is: *does this code path
send any reply at all?* If not, it is a different shape from a validation
rejection and needs a distinct step assertion (`Then no reply is received on
the response subject`).

---

### DISC-2026-05-21-consumer-filter-non-consumption  Worker explicitly ignores certain event subtypes

**Discovered in:** message-worker while authoring
`features/pipeline/message-persistence.feature`.

**Pattern:** A JetStream worker's consumer is configured with a `FilterSubject`
that covers only a subset of events published to a stream. message-worker
listens only to `chat.msg.canonical.<siteID>.created` — it deliberately
ignores `.updated` and `.deleted`. The non-consumption contract is as important
as the consumption contract: authoring a scenario that confirms the worker does
NOT advance its consumer sequence on an ignored event type catches regressions
where FilterSubject is accidentally widened.

**Example:** `features/pipeline/message-persistence.feature:111` — "message-worker
ignores canonical updated events and does not write duplicate rows" —
`@blindspot:msg-worker-no-edit-handler-needs-canonical-filter-primitive`.

**Should this become a CAT?** YES — consider adding to the lower bound as
"CAT-14 FilterSubject non-consumption" for any service that uses a
`FilterSubject` narrower than the full stream. The lower-bound check: for
each event subtype the service does NOT subscribe to, write one scenario
asserting the worker does not process it.

---

### DISC-2026-05-21-partial-success-batch  Batch RPC returns partial success rather than top-level error

**Discovered in:** room-service while authoring
`features/service/room-service.feature` (RoomsInfoBatch handler).

**Pattern:** RoomsInfoBatch returns `{"rooms":[{"roomId":"X","found":false}]}`
when a room ID is unknown — the response is HTTP-200 / NATS-success at the
transport level but contains per-entry failure flags. This is a different
response shape from CAT-9 (existence check that returns a top-level error).
Asserting `Then the response is a Validation error` would incorrectly fail;
the correct assertion is `Then the response is successful And the response
contains a room info entry with "found" set to "false"`.

**Example:** `features/service/room-service.feature:297` — "RoomsInfoBatch for
an unknown room returns Found=false for that entry."

**Should this become a CAT?** YES — consider adding "CAT-14 Partial-success
response" for any batch or multi-resource RPC that can succeed at the top
level while reporting per-item errors. The CAT-9 "existence" category assumes
a top-level error; partial-success is a distinct shape that needs its own
assertion pattern.
