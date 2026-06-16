# Flow cookbook

Patterns for the optional `flow:` scenario shape. Use this when you
need a **gate between fires** — wait for X to land before firing Y.
For the grammar reference, see `SCENARIO-REFERENCE.md` §6.2; for the
when-to-use guideline and the negative-observe semantics rule, see
`AUTHORING.md`.

This cookbook **leads with the tail parallel-observe pattern** —
that ordering is deliberate. It is both the speed-correct and the
soundness-correct default for end-state checks. Authors who skip
this pattern by default produce slower scenarios that subtly weaken
absence findings.

---

## Pattern 1 — Tail parallel-observe barrier (the cookbook default)

**Use this for cumulative end-state checks** — positive assertions
on Mongo state, JetStream canonical events, log absences, etc.
that should hold after everything else has happened.

```yaml
flow: create >> create_accepted >> rename >> rename_accepted >> [room_renamed_in_mongo, rename_canonical, no_error_logs]
```

The bracketed group at the tail is one barrier of three parallel
observations. They share one 5s window (the per-step default
`timeout`) instead of serializing three separate 5s waits. The
total tail-check time is **max(timeouts)**, not their sum.

**Why this matters:**
- **Speed.** Three serial `>> a >> b >> c` end-state checks take
  3×5s = 15s. The parallel barrier takes 5s.
- **Parity with legacy `expected[]`.** Today's flat `expected[]`
  runs each row's `Consistently().ShouldNot()` against the same
  shared accumulated buffer — the windows overlap. The tail
  parallel-observe barrier preserves that semantics. A serial
  `>> neg_a >> neg_b >> neg_c` chain does NOT — each `not: true`
  step only proves absence during its own window, not across the
  union (see AUTHORING.md §Negative-observe semantics).

If your tail includes any `not: true` observation, **it MUST sit in
the final barrier** for the absence to cover the whole scenario
window. The loader warns when `not: true` appears anywhere else.

---

## Pattern 2 — Cross-site federation gate

**Use this when site-b acts on state that site-a wrote.** The
federation lag is structurally racy without a gate; the gate makes
the read-after-write deterministic.

```yaml
input:
  - id: rename
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.r-shared.site-a.room.rename
    payload: { newName: Engineering }
    credential: ${alice.credential}

  - id: bob_lists
    site: site-b
    verb: nats_request
    subject: chat.user.${bob.account}.request.room.list
    payload: {}
    credential: ${bob.credential}

expected:
  - id: rename_accepted
    location: reply
    match: { body_json: { status: accepted } }

  - id: federation_landed                # THE GATE — site-b's INBOX
    location: jetstream_consume
    site: site-b
    args: { stream: INBOX_site-b, filter_subject: chat.inbox.site-b.> }
    match: { body_json: { type: room_renamed, roomId: r-shared, newName: Engineering } }
    timeout: 10s

  - id: bob_sees_new_name
    location: reply
    match: { body_json: { rooms: [{ id: r-shared, name: ${federation_landed.body_json.newName} }] } }

flow: rename >> rename_accepted >> federation_landed >> bob_lists >> bob_sees_new_name
```

`bob_lists` fires only after `federation_landed` matches — the
federated rename observably exists on site-b. The value comes from
the federated event (`${federation_landed.body_json.newName}`), not
a hardcoded literal — catching transform / mangling bugs a literal
match would miss.

---

## Pattern 3 — Read-after-write chain (contract pair to a race finding)

**Use this when proving the contract holds after an observable
precondition.** Mirror-image of a race finding: F-012 demonstrated
"accepted but not yet usable"; the flow shape proves "once usable,
it works correctly."

```yaml
input:
  - id: create
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.site-a.create
    payload: { name: Engineering }
    credential: ${alice.credential}

  - id: rename
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.${create.reply.body_json.roomId}.site-a.room.rename
    payload: { newName: Renamed }
    credential: ${alice.credential}

expected:
  - id: create_accepted
    location: reply
    match: { body_json: { status: accepted } }

  - id: room_persisted                  # THE GATE
    location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { _id: ${create.reply.body_json.roomId} } }
    match: { _id: ${create.reply.body_json.roomId}, name: Engineering, type: channel }
    timeout: 10s

  - id: rename_accepted
    location: reply
    match: { body_json: { status: accepted } }

  - id: room_renamed_in_mongo
    location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { _id: ${create.reply.body_json.roomId} } }
    match: { _id: ${create.reply.body_json.roomId}, name: Renamed }

flow: create >> create_accepted >> room_persisted >> rename >> rename_accepted >> room_renamed_in_mongo
```

Without the `room_persisted` gate, `rename` races room-worker and
fails the same way F-012 demonstrated. With it, rename runs against
an observably-persisted room — the contract is provable.

The pair of (race fails / post-gate succeeds) is a much stronger
finding shape than either alone.

---

## Pattern 4 — Value propagation via `${obs-id.body_json.x}`

**Use this when downstream assertions should match the observed
value, not a hardcoded literal.** Catches transform / mangling bugs.

```yaml
expected:
  - id: canonical_observed
    location: jetstream_consume
    site: site-a
    args: { stream: MESSAGES_CANONICAL_site-a, filter_subject: chat.msg.canonical.site-a.created }
    match: { body_json: { event: created, message: { id: m1, content: hi } } }

  - id: cassandra_row
    location: cassandra_select
    args:
      query: "SELECT JSON * FROM messages_by_id WHERE message_id = ?"
      params: [${canonical_observed.body_json.message.id}]    # use the OBSERVED id
    match:
      message_id: ${canonical_observed.body_json.message.id}  # assert against observed
      msg: ${canonical_observed.body_json.message.content}
```

The Cassandra query uses the message id the system actually
published, and the assertion checks the Cassandra row matches the
canonical event's content. A regression where the worker transforms
either field would fail loudly with both observed values in the
report.

---

## When NOT to use `flow:`

- **Simple validation:** one fire, several assertions, no causal
  ordering. Legacy shape is shorter and equally correct.
- **Concurrency tests:** F-011-class lost-update races NEED two
  fires to overlap in the worker. A `flow:` with no observation
  between two fires preserves the race (`fire_a >> fire_b`), so
  F-011 stays reproducible — but the legacy shape is just as good
  here and saves the ceremony.
- **Single-fire infra-sanity scenarios:** keep them legacy. The
  loader warns aren't worth navigating for a 1-input scenario.

The decision rule from AUTHORING.md: *use `flow:` when you need a
gate between fires; leave it out otherwise.*
