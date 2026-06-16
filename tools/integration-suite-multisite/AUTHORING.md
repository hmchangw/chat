# Authoring scenarios — multi-site

This is the hand-edit guide for `tools/integration-suite-multisite/`
scenarios. The multi-site scenario shape is fundamentally different
from the single-site shape — read this doc before editing any YAML.
For the YAML grammar field-by-field reference see
[SCENARIO-REFERENCE.md](SCENARIO-REFERENCE.md).

---

## Five hard rules

1. **Expected behavior comes from a cited design doc — not from
   training data.** Every scenario has a `source:` line.

2. **If the design is silent, STOP.** Do not invent expectations.

3. **Catalog vocabulary is closed.** Verbs, readers, and seed-effect
   flags must already exist in `catalogs/`. If a needed primitive is
   absent, surface the gap rather than working around it.

4. **One scenario = one fire + one expected list.** There is no
   `cases:` array and no `base_input:`. Each scenario fires exactly
   once and asserts one set of outcomes. If you need two independent
   experiments, write two scenario files.

5. **Scenarios always land in `drafts/`.** Promotion to `approved/`
   is a separate, human-reviewed PR.

---

## Pass/fail semantics and open findings

> **Read this before you decide whether a failing scenario is a
> regression or a finding.** The suite uses two independent axes:

**Axis 1 — polarity (what the author expects):**
- **+ve** — we expect something to **happen** (a result, behavior:
  canonical event lands, row persists, success reply arrives).
- **−ve** — we expect something **prevented** (an error, warning, or
  rejection — a guard against a bad case).

**Axis 2 — outcome (whether the expectation held):**

| Polarity (what we expect) | Outcome | Means |
|---|---|---|
| +ve (a result should happen) | true — it happened | **PASS** — no issue |
| +ve | false — didn't happen, or something else did | **FAIL** — bug |
| −ve (an error/warning/rejection should fire) | true — it fired | **PASS** — no issue (correctly guarded) |
| −ve | false — the expected error/warning did not happen | **FAIL** — bug (bad case slipped through) |

**PASS = true = no issue. FAIL = false = bug → findings doc.** Same
verdict logic for both polarities; independent of whether the
expectation is asserted via a positive `match:` or `not: true`.

### Authoring a scenario for a found bug

The bug **is the failure**, not the expectation. If you observe a
bug, the scenario's expectation should describe the **correct**
behavior (whatever the system is supposed to do), so the bug shows up
as a failure:

- **System leaks deleted content into a quote** — author a −ve
  scenario expecting "deleted content NOT embedded in the quote." It
  fails because the content leaks. The fail = the bug.
- **System allows sender impersonation** — author a −ve scenario
  expecting "cross-namespace publish rejected." It fails because the
  guard doesn't fire. The fail = the bug.

**Anti-pattern:** authoring +ve "the bug happens" — e.g. "expect the
quote to embed deleted content, and it does → pass." This makes the
suite green with the bug open; pass/fail carries no signal. If you
catch yourself doing this, re-polarize: assert the correct behavior
and let the failure be the bug.

### Steady-state reads — `draft + fail = open finding`

The suite combines two orthogonal axes that authors already use:

| Status | Outcome | What it means |
|---|---|---|
| `draft` | pass | scenario passes today; not yet promoted to CI gate |
| `draft` | fail | **open finding** — a documented bug from `docs/integration-suite-multisite-findings.md`; informational, no CI gate |
| `approved` | pass | fixed + regression-guarded (CI-gating) |
| `approved` | fail | **CI regression** — fix it immediately |

In steady state, the draft report shows N red scenarios where N =
open findings count. **The reds are not regressions — they are
standing records of bugs documented in the findings doc.** The suite
goes fully green only when the chat-app team has fixed those bugs.

**Lifecycle of a finding:**
1. Author observes a bug → writes a `draft` + `−ve` (or `+ve`,
   whichever fits the correct-behavior shape) scenario that fails.
2. Author files an `F-NNN` entry in `docs/integration-suite-multisite-findings.md`
   describing the bug, citing the scenario.
3. The scenario sits as a standing red in DRAFT reports — chat-app
   team picks it up.
4. Chat-app team ships the fix → scenario goes green.
5. Author promotes the scenario to `status: approved` so any future
   regression turns the APPROVED row red and the CI gate trips.

**Don't:** invent special folders, "expected-fail" buckets, or other
mechanisms for findings. `draft + fail` already says everything that
needs to be said; the findings doc carries the durable narrative;
the report headline already separates DRAFT (informational) from
APPROVED (CI-gating).

---

## Mental model

A multi-site scenario is one hypothesis about the assembled two-site
system. You declare the seed state you need on each site, fire one
verb from one site, and assert what you expect to observe — possibly
on both sites.

```
scenario
  sites:              per-site seed
    site-a / site-b
  input:              one fire (site required)
  expected:           one assertion list
    []expected[i]     each with site: where required
```

The Sandbox materializes users on each site by calling the per-site
auth-service. It drops and re-creates collections (per site), then
seeds rooms and Cassandra rows before firing.

---

## Required fields in order

```
scenario:   <name>
source:     <doc-or-file-citation>
tag:        positive | negative
sites:      <map>   (at least one site)
input:      <fire>
expected:   <list>
```

`status:` is optional (default `draft`). Include it only when promoting
to `approved` via a reviewed PR.

---

## Per-site seed

Declare each actor under the site where they are registered:

```yaml
sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
      rooms:
        - id: r-eng
          type: channel
          name: Engineering
      memberships:
        alice:                # user alias (must be in seed.users)
          - room: r-eng       # must reference a room in seed.rooms
            roles: [owner]    # list; defaults to [member] if omitted
  site-b:
    seed:
      users:
        bob: { verified: true }
```

- Users on site-a can only be used in `input` and `expected` entries
  whose `site:` is `site-a`. The `${alice.account}` token is global
  across the scenario, but alice's JWT was minted against site-a's
  auth-service.
- Rooms and memberships follow the same closed enums as single-site
  (channel/dm for type; owner/member for role).
- If a site needs no seed, omit it from `sites:` entirely.

---

## `cassandra_data:` at scenario top level

Cassandra is a shared cluster. Seed rows go at the top level (not
inside any `sites.<site>.seed`):

```yaml
cassandra_data:
  - table: messages_by_room
    rows:
      - room_id: r-eng
        created_at: ${now - 2m}
        bucket: ${bucket(created_at)}
        message_id: m-1
        msg: "hello"          # CQL column is `msg`, NOT body_text
```

`${now - 2m}` resolves to a Unix millisecond timestamp two minutes
before `Sandbox.StartTime`. `${bucket(created_at)}` auto-computes the
message-bucket partition key from the resolved `created_at` column.

---

## `mongo_data:` at scenario top level

For pre-conditions the first-class `seed.{users,rooms,memberships}`
shapes don't cover (e.g. thread rooms, thread subscriptions), seed raw
Mongo docs at the top level. Unlike `cassandra_data`, Mongo is
per-site, so each entry names its `site`:

```yaml
mongo_data:
  - site: site-a
    collection: thread_rooms
    docs:
      - _id: t-1
        parentRoomId: r-eng
        parentMessageId: m-1
```

`collection` must be one of the sandbox-owned collections the harness
drops between scenarios — the closed set is `users`, `rooms`,
`subscriptions`, `room_members`, `thread_rooms`, `thread_subscriptions`.
Docs are inserted verbatim (BSON), so use the on-disk `bson` field
names (`_id`, `parentRoomId`), not JSON tags. `${...}` substitution
tokens (e.g. `${alice.account}`) resolve inside doc values. See
SCENARIO-REFERENCE §4 for the full field reference.

---

## Authoring discipline — assert at every observable layer

A scenario fires one verb and observes its effects. When the verb
exercises a multi-step pipeline (request → handler → stream → worker
→ database → cross-site), the natural temptation is to assert only
the final observable — the database row, the federated event — and
treat everything in between as "if the end is right, the middle was
right." That leaves the suite blind in two important ways:

1. **Failure localisation.** When the final observable doesn't
   arrive, "which layer was broken?" requires reading container logs.
   The suite should pinpoint it.

2. **Silent correctness drift.** If a refactor changes an
   intermediate event shape or skips it entirely, the final
   observable may still arrive (via some other path) and the
   scenario stays green while a real regression sits underneath.

**Discipline:** for each fire, enumerate every layer the pipeline
can be observed at, and assert at each. The cost is a few extra
`expected[]` blocks; the benefit is that failures name the broken
layer themselves.

Observable layers, in typical order:

| Layer | Reader primitive | What it proves |
|-------|------------------|----------------|
| Synchronous reply | `reply` | The handler accepted the request and replied. |
| Local Mongo / Cassandra | `mongo_find` / `cassandra_select` | The state mutation persisted. |
| Local JetStream canonical | `jetstream_consume` | The originating service published the event. |
| Local OUTBOX (cross-site cases) | `jetstream_consume` | The worker emitted the federation event locally — before the gateway gets involved. |
| Peer-site INBOX | `jetstream_consume` (with `site:` set) | The federation Source delivered. |
| Peer-site persistence | `mongo_find` / `cassandra_select` | The peer's worker consumed and applied. |
| Service logs | `logs_tail` | Last-resort diagnostic when events skip layers. |

If a layer is **skipped** from `expected[]`, the suite either trusts
it implicitly (fine for layers that are uninteresting to the
scenario's theme) or the author has chosen to be ambiguous about
where a failure originated (avoid).

For cross-site federation tests in particular, the OUTBOX-on-the-
firing-site assertion is the single most diagnostic block: it
distinguishes "the local worker published" from "the federation
Source didn't deliver." Always include it.

---

## Infra-sanity scenarios — the harness's first line of defence

Scenarios prefixed `infra-sanity-` are reserved for tests that
verify the harness itself + the multi-site stack are healthy enough
for any downstream finding to be meaningful. Conventions:

- File name: `infra-sanity-<what>-site-<a|b>.yaml`
- `status: approved` (gates CI). A failing infra-sanity scenario
  means downstream failures are not informative; fix the infra
  first.
- The fire is a minimal, well-known production code path (room
  create, message send, auth mint) — nothing exotic.
- The `expected[]` list covers every observable layer for the chosen
  fire, so a sanity failure already localises the broken layer.

Currently shipped:

| File | Proves |
|------|--------|
| `infra-sanity-rooms-pipeline-site-a.yaml` | site-a NATS req/reply, MESSAGES + ROOMS streams, per-site Mongo, room-service + room-worker chain. |
| `infra-sanity-rooms-pipeline-site-b.yaml` | Same shape on site-b — proves the symmetric site is functional. |

NATS supercluster gateway delivery is implicitly tested by the
`cross-site-room-rename-federation.yaml` scenario; we do not ship a
gateway-isolated sanity test because user-account credentials don't
have permission to publish on backend subjects, and the federation
scenario surfaces gateway-or-Source failures clearly enough.

---

## `pre_fire_scripts:` — escape hatch for state the seed grammar can't express

Some pre-conditions are operationally-scaffolded (a JetStream stream
ops/IaC normally creates, a Vault secret a sidecar normally provides).
The seed grammar declares scenario state, not infra state — see
`ARCHITECTURE.md` §0 for why.

If your scenario needs that kind of prep, declare it as a script:

```yaml
pre_fire_scripts:
  - prep-outbox.sh
```

The script sits next to the YAML file. The harness runs it after
`Sandbox.Setup` and before the fire, passing the live stack's
host-mapped URLs + creds path as `ISM_*` env vars. Non-zero exit
fails the scenario with the script's output captured into the
report. Full reference: `SCENARIO-REFERENCE.md` §4.5.

This is an escape hatch, not a default. Anything the seed grammar
can express belongs in `seed:` — declarative, author-visible, no
external dependency. Reach for `pre_fire_scripts` only when there's
genuinely no seed-grammar way to set up the precondition.

---

## When to use `flow:` (and when not to)

**Default: don't use `flow:`.** Most scenarios are 1-fire/N-assert
validations; the legacy shape (sequential fires, flat `expected[]`
evaluated after) is fine and shorter. Adding `flow:` for these is
just verbosity.

**Use `flow:` when you need a gate between fires** — wait for X to
land before firing Y. Authorable bug classes that need it:
- read-after-write (the second fire reads what the first wrote)
- contract pairing with a known race (F-012, F-013) — prove the
  contract holds **after** the precondition is observable
- cross-site federation timing (wait for an event to federate to
  site-b before firing the next user action on site-b)
- dedup-window precision (fire the second publish deterministically
  inside the first's observed dedup window)

If your scenario doesn't need a gate, skip `flow:`.

---

## Negative-observe semantics

> ⚠️ **Soundness gotcha — read this before adding `flow:` to a
> scenario with `not: true` assertions.**

The legacy shape evaluates `not: true` assertions against the
**whole accumulated buffer** at scenario tail —
`Consistently().ShouldNot()` proves absence across the entire run.

The flow shape changes this: a `not: true` observation in `flow:`
proves absence **only during its own `timeout` window**. Events
arriving outside the window are not its concern.

**The trap:** an early `not: true` step proving "no canonical for
this message" silently passes if the dropped-then-delivered message's
canonical arrives during a LATER barrier. The author got a
false-negative finding.

**The rule (loader-enforced as a warning):**
> **End-state negative checks belong in the final barrier of
> `flow:`.** The loader emits a warning when a `not: true` observe
> step sits anywhere else.

Concrete impact on existing F-006-class drop assertions
(`gatekeeper-empty-content-rejected`,
`quote-nonexistent-parent-drops-message`,
`gatekeeper-thread-reply-missing-parent-createdat-rejected`): if
ever migrated to `flow:`, their `not: true` observations MUST stay
in the final barrier. Mid-flow placement silently weakens the
finding. The legacy shape protects these assertions today — there
is no urgency to migrate them.

**Tail parallel-observe barrier is the cookbook pattern** for tail
checks. `[neg_a, neg_b, pos_c]` shares one 5s window instead of
serializing three separate 5s waits, and exactly matches today's
flat `expected[]` semantics. See `FLOW.md` for examples.

---


## Substitution token vocabulary

Available in `subject`, `payload`, `credential`, `match`, and `args`
fields.

| Token | Resolves to |
|-------|-------------|
| `${<alias>.account}` | seed user's account (== alias) |
| `${<alias>.id}` | `u-` + account |
| `${<alias>.jwt}` | minted NATS JWT |
| `${<alias>.nkey}` | nkey seed |
| `${<alias>.credential}` | user-level credential shorthand |
| `${now}` | `time.Now().UTC().UnixMilli()` (int64). Works everywhere. |
| `${now - 2m}` / `${now + 1h}` | int64 millis offset. **Resolves only inside `cassandra_data` rows and `cassandra_select` args.** Elsewhere (payloads / subjects / match / `mongo_data` / `nats_subscribe` args), the bare arithmetic form errors with `unknown path` — use `${date:now-1h}` instead. |
| `${date:now}` / `${date:now-1h}` / `${date:now+5m}` | same now-expression grammar, returns `time.Time` (UTC). **Works everywhere.** Use this for BSON Date fields in `mongo_data`, CQL timestamp params, and anywhere a real `time.Time` is required. |
| `${bucket(<col>)}` | auto-computed message-bucket value |
| `$auto` | runtime-unique random string |
| `${<id>.reply.body_json.<field>}` | a field of task `<id>`'s captured reply (multi-fire only) |
| `${<id>.reply.status}` | sugar for `${<id>.reply.body_json.status}` |
| `${<id>.body_json.<field>}` | a field of expected[].id `<id>`'s matched event body (flow scenarios only) |

---

## Multi-fire scenarios

`input:` can be a **list of tasks** (each with an `id:`) fired in
declaration order, instead of a single map. A later task reads an
earlier task's reply via `${<id>.reply.body_json.*}`, and a `reply`
assertion scopes to one task with `match: { task: <id>, … }`. Full
grammar in SCENARIO-REFERENCE.md §5–§6. Two authoring notes:

- **Reply substitution chains room-lifecycle flows by ID, not by side
  effect.** A `nats_request` reply carries data (e.g. a created room's
  `roomId`) you can thread into a later task. But Create Room / Add
  Members are async-jobs with **no inter-task wait** in v1, so a task
  that acts on the just-created room races the worker that writes it
  (member.add 403s; a msg.send is dropped). Chain reply data that is
  valid immediately (a deterministic DM `roomId`); don't assume the
  created room is queryable by the next fire.
- **Message chains chain via known ids + seeded parents, not replies.**
  `msg.send` is `jetstream_publish` — fire-and-forget, no reply to
  substitute from. To build a multi-message flow (subsequent thread
  reply, dedup, tcount), set the message `id:` yourself in each task's
  payload and/or seed the parent message, then assert on those ids.

---

## Forbidden tokens

The loader rejects these with an explicit error:

| Token | Why forbidden |
|-------|---------------|
| `${site}` | Ambiguous in a two-site scenario. Write the literal `site-a` or `site-b`. |
| `${siteA}`, `${siteB}` | Same — not supported in multi-site loader. |
| `${<alias>.site}` | Not a recognized field on seed users. |
| `${service.*.credential}` | Service credentials are not exposed in the multi-site runner. |

---

## Site-routing rules

`site:` controls which site's connections the runner uses for the fire
and for each assertion.

| Field | `site:` required? |
|-------|-------------------|
| `input` | Yes — must be `site-a` or `site-b` |
| `expected[i]` where location is `reply` | Forbidden — intrinsic to the fire site |
| `expected[i]` where location is `cassandra_select` | Forbidden — shared cluster |
| `expected[i]` where location is `mongo_find` | Required |
| `expected[i]` where location is `jetstream_consume` | Required |
| `expected[i]` where location is `nats_subscribe` | Required |
| `expected[i]` where location is `logs_tail` | Required |

Violations are caught by the loader before any container is booted.

---

## Worked example 1 — single-site happy path on multi-site infra

File: `scenarios/drafts/room-creates-federates-to-site-b.yaml`

```yaml
scenario: room-creates-federates-to-site-b
source: spec docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md §8
status: draft
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: Engineering
    users: ["${alice.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match:
      body_json:
        status: accepted
  - location: mongo_find
    site: site-a
    args:
      collection: rooms
      filter:
        name: Engineering
    match:
      name: Engineering
      createdBy: ${alice.id}
```

What this tests: room creation succeeds on site-a and lands in site-a's
Mongo. No cross-site assertion. This is the baseline — if this fails,
something is wrong with the single-site stack, not with federation.

---

## Worked example 2 — federation tail

File: `scenarios/drafts/room-create-federates-cross-site.yaml`

```yaml
scenario: room-create-federates-cross-site
source: spec docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md §8
status: draft
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
  site-b:
    seed:
      users:
        bob: { verified: true }

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: EngineeringFederated
    users: ["${alice.account}", "${bob.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match:
      body_json:
        status: accepted
  - location: mongo_find
    site: site-a
    args:
      collection: rooms
      filter:
        name: EngineeringFederated
    match:
      name: EngineeringFederated
  - location: mongo_find
    site: site-b
    args:
      collection: rooms
      filter:
        name: EngineeringFederated
    match:
      name: EngineeringFederated
    timeout: 10s
```

What this tests: a room created on site-a with a site-b member
federates to site-b's Mongo within 10 seconds. The extended timeout
accommodates OUTBOX → INBOX propagation latency. If this assertion
times out, see the "Federation may not fire on room create" open
concern in `README.md`.

---

## Tips

- **Scenarios are drafts by default.** Do not set `status: approved`
  unless the scenario is going through a reviewed PR for CI promotion.
- **One scenario = one assertion theme.** Do not bundle a happy path
  and a negative case in the same scenario — put them in separate files.
  The multi-site shape has no case loop; bundling would require awkward
  setup or unsafe state sharing between scenarios.
- **Use `$auto` for room names** that must not collide across parallel
  or repeated runs.
- **`timeout: 10s`** (or longer) is appropriate for cross-site
  assertions because federation adds OUTBOX → INBOX propagation time
  on top of normal async processing.
- **Run validation before booting infra:**
  `make -C tools/integration-suite-multisite validate`

---

## Cross-scenario cache discipline

Service containers stay up across the whole run; only databases
are reset between scenarios. The chat-app services hold in-process
caches (sub-cache keyed `(roomID, account)`, room-meta-cache keyed
`roomID`, user-cache keyed `userID`) with ~2-minute TTLs. Within
the TTL window, a second scenario referencing the same cache key
reads the FIRST scenario's projection — even though the Mongo doc
underneath has been reseeded with the new value.

**This is a soundness issue tracked as finding F-009 and plan-ahead
§2.10.** The structural fix lives in chat-app code (env-driven cache
TTL override or admin cache-flush endpoint). Until that lands,
scenario authors carry the discipline:

### The rule

**Two scenarios in the same run must not reference the same
`(actor-alias, room-id)` pair with conflicting state.** Conflicting
state today means:

- Different roles for the same `(alias, room-id)` pair
  (`alice@r-busy=[member]` in one scenario, `alice@r-busy=[owner]`
  in another)
- The same alias declared local in one scenario and via
  `remote_users:` in another
- The same `roomId` with different `user_count` values

### The discipline

Make the room ID unique per scenario when conflict is unavoidable.

**Worked example — large-room cap variants:**

```yaml
# gatekeeper-large-room-member-blocked.yaml
sites:
  site-a:
    seed:
      rooms:
        - id: r-busy-member       # NOT r-busy
          name: BusyChannelMember
          user_count: 501
      memberships:
        alice: [r-busy-member]
```

```yaml
# gatekeeper-large-room-owner-bypass.yaml
sites:
  site-a:
    seed:
      rooms:
        - id: r-busy-owner        # NOT r-busy
          name: BusyChannelOwner
          user_count: 501
      memberships:
        alice:
          - room: r-busy-owner
            roles: [owner]
```

The two scenarios both exercise the large-room cap but cache under
different `(alice, room)` keys. No contamination. Same applies to
remote-vs-local conflicts:

**Worked example — alias collision across local/remote:**

```yaml
# scenarios that need bob LOCAL on site-a:
sites:
  site-a:
    seed:
      users:
        bob: { verified: true }    # alias `bob`

# scenarios that need bob REMOTE on site-a (parent author on site-b):
sites:
  site-a:
    seed:
      remote_users:
        remotebob:                 # different alias avoids user-cache collision
          home_site: site-b
  site-b:
    seed:
      users:
        remotebob: { verified: true }
```

### When the discipline is impossible

A few scenarios genuinely need shared `(alias, room)` keys — e.g.
chained federation flows that compose against a stable
pre-condition. Until F-009 closes those need either:

- A real fix from the chat-app team (preferred).
- A `pre_fire_scripts` step that nudges the cache out of band (e.g.
  via an admin endpoint when one ships).

Document the constraint in the scenario YAML's top comment so the
reviewer understands the dependency.

### Loader-time enforcement

`make validate` checks this discipline (warn-only today):
`CrossScenarioCheck` flags both conflict shapes — `(alias, room)`
declared with different role sets, and an alias declared with
different home sites — and prints `WARNING (F-009): …` per conflict.
It does NOT fail the run yet; promotion to a hard error is gated on
the coordinated cleanup of the existing conflicts (plan-ahead §2.10).
Until then, treat any F-009 warning as a real bug: untreated, the
failure mode is silent (wrong verdict, order-dependent on whichever
scenario cached the conflicting state first).

---

## Architecture

`tools/integration-suite-multisite/ARCHITECTURE.md` explains the
26-container stack, NATS supercluster, federation Sources, Sandbox
lifecycle, and the verb/reader primitive catalog. Read it once before
authoring your first scenario.
