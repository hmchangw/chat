# Integration suite v2 — Part-1 corrections (catalog truthfulness + X-Request-ID)

**Status:** draft 2026-05-24 (revised same day — see §1.1)
**Owner:** integration-suite v2
**Related:**
- `2026-05-21-integration-suite-v2-design.md` (v2 platform — unchanged in architecture)
- `2026-05-24-scenario-author-command-design.md` (scenario-author command — check #5 wording clarified here)
- `tools/integration-suite-v2/ARCHITECTURE.md`

## 1. Context

Authoring the first real Part-1 scenario via `/scenario-author` surfaced
eight bugs in the v2 Part-1 implementation. The bugs span all three
classes the design anticipates:

- **Wrong facts on disk** — the catalog asserts things the code does not
  do (`mongo.rooms` "owned by" `room-service`; `room-service` declares
  it writes `mongo.rooms` on create).
- **Missing facts** — the `nats_request` executor doesn't propagate
  `X-Request-ID`, which every authoritative handler in this codebase
  requires (CLAUDE.md §3 + `room-service/handler.go:161-167`). No scenario
  using `nats_request` against a real handler can succeed today.
- **Documentation gaps** — `${site}` is used in scenarios as a runtime
  global but is not in the verb schema, AUTHORING.md, or anywhere else;
  the `/scenario-author` check #5 wording is ambiguous for synthetic
  readers (`owners: []`).

The v2 platform design (2026-05-21) is unchanged. The four-layer model,
verdict mechanism, mishap shape, and reader-catalog structure all stand.
This spec captures the corrections — one Go bug fix, two catalog data
fixes, two new catalog entries (Part-2-shaped but needed to enable
multi-step scenarios), two doc clarifications, and a re-authored sample
scenario — and the implementation order.

The mechanism worked as designed: an attempt to author a behavior-true
scenario forced the catalog to be confronted with the code. The
five-surface check is what made the gap visible at authoring time
instead of at runtime.

### 1.1 Revision log

- **2026-05-24 (initial).** Original 8-bug list and resolutions.
- **2026-05-24 (revision after audit).** A full catalog-vs-codebase
  audit identified ~15 additional findings beyond the original 8. They
  are being resolved **one at a time** via a Concern → Decision →
  Spec-update process; this spec grows as each concern lands. The first
  audit finding to land here is **P0-A** (the fixture cast is fictional
  — no `users` rows in Mongo). It is sequenced FIRST in §6 because
  without it, even P0-1's X-Request-ID fix delivers nothing visible
  (handler still 404s at `GetUser`). The remaining audit findings sit
  in the author's backlog and will be added to this spec or deferred
  to `§9 Future TODOs` as decisions are made.

## 2. Non-goals

- **No architectural changes to v2.** The four-layer model, verdict
  mechanism, mishap shape, and catalog discipline are unchanged.
- **No new verb primitives.** Only the existing `nats_request` is
  modified.
- **No Cassandra / Mongo subscriptions / outbox readers.** Those remain
  Part-2 scope. This spec adds *one* JetStream reader because it is
  needed for the immediate next scenario and because the executor it
  needs is small and self-contained.
- **No promotion automation, no chaos work, no fixture-cast expansion.**
- **No production code modifications to enable suite assertions.**
  The suite is a black-box observer of running services — it
  inspects what services ALREADY produce in the ordinary course of
  work, and adapts its catalog around that truth. Adding
  instrumentation to a service for the test's benefit defeats the
  v2 design's "Black-box only" goal (per
  `2026-05-21-integration-suite-v2-design.md:45` — "Asserting on
  implementation details… Black-box only"). See §2.1.

### 2.1 Black-box discipline (added 2026-05-24)

When authoring a scenario surfaces a gap — "the service doesn't
emit the signal I want to assert on" — three responses are
acceptable:

1. **Find another existing observable.** Production services emit
   many signals: Mongo inserts, NATS publishes, JetStream canonical
   events, error log lines, async-job-result publishes. The
   catalog catalogs SOME of these today; growing the catalog (a new
   reader entry + a Go executor that observes an ALREADY-EMITTED
   signal) is fine.

2. **Catalog a signal the suite is currently discarding.** P1-J is
   the precedent: NATS already returns headers + an error class +
   timing; we just weren't capturing them. Extending the reader to
   surface what production already produces is fine.

3. **Document the gap.** Add it to a §9.x backlog row so the next
   reviewer knows. Skip the scenario or use a weaker assertion.

What is **not** acceptable: changing a production service to emit
a NEW signal the suite can hook onto. That couples production to
test infrastructure and undermines the black-box contract — the
suite would then be "testing whether we remembered to instrument
the code we asked the suite to instrument," which is circular.

If a service genuinely lacks production-grade observability that
the operations team also needs (e.g. no success-path log at all),
that's a real-service hygiene issue worth raising separately —
through the normal CLAUDE.md §3 logging-discipline review, not
through this spec or the suite. The suite waits for that
production-driven change to land.

## 3. Issues and resolutions

Severity-ordered. Each row corresponds to one fix below.

| #  | Class       | Issue                                                                 | Evidence (file:line)                                        |
|----|-------------|-----------------------------------------------------------------------|-------------------------------------------------------------|
| **P0-A** | **Architecture / data** | **`fixture-cast.yaml` claims `has_user_record` but the seeder never writes a `users` row to Mongo; every room-service scenario 404s on `GetUser` before reaching business logic.** | `catalogs/fixture-cast.yaml:7-11` (the claim) + `internal/fixtures/seeder.go:39-101` (seeder only mints NATS JWTs) vs `room-service/handler.go:179-188` (requires user row with non-empty `EngName` / `ChineseName`) |
| P0-1 | Go bug      | `NATSRequest` executor doesn't set `X-Request-ID`; every request to room-service is rejected before business logic | `internal/verbs/nats_request.go:43-46` vs `room-service/handler.go:161-167` |
| P1-2 | Catalog fact | `mongo.rooms` `owners: [room-service]` is wrong; `room-worker` is the actual writer | `catalogs/readers/mongo.rooms.yaml:24` vs `room-worker/store_mongo.go:133` |
| P1-3 | Catalog fact | `room-service.yaml` declares `writes: mongo.rooms` on create; room-service does not write Mongo on this path | `catalogs/services/room-service.yaml:30` vs `room-service/handler.go:337-374` |
| **P1-J** | **Go gap + catalog drift** | **`reply.yaml` promises a `transport_error` field on the emitted event; `nats_reply.go:Inject` never accepts the error and the `Event` struct has no place for it. Headers + latency are similarly discarded. Negative scenarios cannot distinguish "service returned empty success reply" from "transport failed" or assert on response headers (e.g., `X-Request-ID` echo).** | `catalogs/readers/reply.yaml:14` (the promise) + `internal/readers/types.go:13-19` (no fields) + `internal/readers/nats_reply.go:39-51` (Inject ignores `out.Err`) + `internal/runtime/dispatcher.go:53-55` (dispatcher throws away `out.Err`) |
| P2-4 | Missing catalog | No `room-worker` service profile, so no multi-step scenario can target persistence | `catalogs/services/` has only `room-service.yaml` |
| P2-5 | Missing catalog + Go | No reader for the canonical-event publish that room-service produces on accept | `catalogs/readers/` has only `reply`, `mongo.rooms`, `logs.room-service` |
| P3-6 | Documentation | `${site}` is used in scenarios but undocumented | `scenarios/drafts/verified-user-creates-channel-room.yaml:6`; no mention anywhere |
| P3-7 | Documentation | `/scenario-author` check #5 wording undefined for `owners: []` readers | `.claude/commands/scenario-author.md` Step 4 row #5 vs `catalogs/readers/reply.yaml:23` |
| P4-8 | Sample drift | The sample scenario has wrong source, would be rejected by the handler, asserts a nonexistent field | `scenarios/drafts/verified-user-creates-channel-room.yaml` |

### 3.0 P0-A — Static seed JSON + `fixture-cast.yaml` as faithful annotation

**Cause.** `catalogs/fixture-cast.yaml:7-11` documents `has_user_record`
as meaning "the auth flow created a `users` row in Mongo." This is
false twice over:

- `auth-service/` has zero Mongo imports (`grep -l 'mongo' auth-service/*.go` returns nothing). The auth flow cannot create Mongo rows.
- `internal/fixtures/seeder.go:39-101` only mints NATS JWTs (POSTs to `auth-service /auth`, stores `{JWT, NkeySeed}`). It never touches Mongo.

The `users` collection in Mongo stays empty after `make up`. The only
writer anywhere is `tools/loadgen/seed.go:36-46` — a developer-invoked
load-test tool, not wired into anything else.

Consequence: every room-service scenario runs to `room-service/handler.go:179`
(`h.store.GetUser`) → `errUserNotFound` → error reply. P0-1 (X-Request-ID)
alone delivers nothing visible.

**Resolution — chosen approach (locked 2026-05-24).** Static JSON seed
files, loaded fresh before every scenario run; cast YAML annotates what
the seed contains.

```
tools/integration-suite-v2/
  seed/
    users.json              # array of model.User documents (bson tags)
    rooms.json              # array of model.Room documents
    subscriptions.json      # array of model.Subscription documents
    README.md               # "edit alongside catalogs/fixture-cast.yaml; review both together"
  catalogs/
    fixture-cast.yaml       # rewritten as faithful annotation of the seed
  internal/
    fixtures/
      seeder.go             # unchanged behaviorally — keeps JWT minting
```

_(Implementation note added 2026-05-24 during P0-A implementation: Go's `//go:embed` does not accept `../` paths, so `loader.go` ships at `tools/integration-suite-v2/seed/loader.go` (package `seed`) next to the JSON it embeds, not under `internal/fixtures/`. The §4 files table reflects the actual layout.)_

**Per-scenario reload (locked).** The loader runs at the **start of
every scenario test**, not just at suite startup. Each scenario gets a
byte-identical world; mutations a scenario produces (rooms created via
the verb, subscriptions added, etc.) do not leak into the next
scenario. Drop + `InsertMany` per collection, all three collections
each run. Latency is acceptable for Part-1 (one scenario today). If it
becomes painful at scale, revisit per §9.

**`fixture-cast.yaml` format — Shape B (separate `subscriptions:` edges):**

```yaml
users:
  - id: alice
    tags: [verified, has_user_record]
  - id: bob
    tags: [verified, has_user_record]

rooms:
  - id: r-engineering
    tags: [channel, public]
  - id: r-design
    tags: [channel, restricted]

subscriptions:
  - user: alice, room: r-engineering, roles: [owner]
  - user: alice, room: r-design,      roles: [member]
  - user: bob,   room: r-engineering, roles: [member]
```

Tag semantics are FACT-shaped: `verified`, `has_user_record`, `channel`,
`public`, `restricted` are all true of the actual seeded entity.
Relationship facts live in the `subscriptions:` block; predicate
resolvers walk it to answer queries like `{ owner_of: $r.id }`.

**Naming convention (deterministic, loadgen-shaped but readable):**

| Field | Pattern | Example |
|---|---|---|
| user `_id` | `u-<account>` | `u-alice` |
| user `account` | the cast YAML's `id` | `alice` |
| user `engName` / `chineseName` | deterministic from account; non-empty (per `handler.go:186`) | `Alice` / `爱丽丝` |
| user `siteId` | `${site}` resolved at seed time from env | `site-local` |
| room `_id` | `r-<slug>` | `r-engineering` |
| subscription `_id` | `sub-<user>-<room>` | `sub-alice-r-engineering` |

Two runs produce byte-identical world state.

**Initial richness (straw-man for Part-1, tune in implementation):** 4
users, 2 channel rooms, 3–4 subscriptions covering at minimum
"owner-of-room-X / member-of-same / member-of-different / in-no-rooms"
distribution. Small enough to author by hand; rich enough for
single-actor scenarios and the start of role-based predicates.

**Drift risk (accepted).** Two files of truth: `seed/*.json` (what's in
Mongo) and `catalogs/fixture-cast.yaml` (what scenarios pick from). If
edits to one are not mirrored in the other, scenarios may pick a user
whose declared role doesn't match Mongo, producing a mysterious
permission-denied failure. **Mitigation: review discipline.** Edits
to seed and cast land in the same commit, mentioned together in the PR
description. No code-level validator for Part-1 — accepted by user
2026-05-24.

**Seeder split.** `internal/fixtures/seeder.go` keeps its current job
(NATS JWT minting per cast user with `strategy: generate-and-auth`).
A new sibling `internal/fixtures/loader.go` owns Mongo seeding. The
two are invoked in sequence at scenario test start: loader first (so
users exist), then seeder (so JWTs exist for the existing users).

### 3.1 P0-1 — `X-Request-ID` propagation in `NATSRequest`

**Cause.** `internal/verbs/nats_request.go:43-46` only sets `traceparent`.
CLAUDE.md §3 ("Request Logging & Tracing") makes `X-Request-ID` a
contract for every entry point: the value is either inbound or
server-generated, must be a 36-char hyphenated UUID, must be validated
via `idgen.IsValidUUID`, and must be propagated via `context.Context`.
`room-service/handler.go:161-167` enforces this and rejects with
`errMissingRequestID` / `errInvalidRequestID` otherwise.

**Resolution.**
1. Generate a fresh request ID per `Execute` call using
   `pkg/idgen.GenerateRequestID()` (UUIDv7 hyphenated).
2. Set it on `msg.Header["X-Request-ID"]` alongside the existing
   `traceparent`.
3. Add `RequestID string` to `verbs.Outcome` so the dispatcher /
   reporter can correlate logs/traces back to the scenario. The
   request ID is set on `Outcome` whether the request succeeds, returns
   a transport error, or times out.
4. The verb does NOT expose `RequestID` as a scenario-author input.
   It is runtime-generated; scenarios remain agnostic. (Future spec
   may add an override for replay-attack testing — out of scope here.)

**Validator behavior.** The catalog validator's existing input-shape
check does not need changes — `RequestID` is a transport-level concern,
not a payload field. The verb YAML's `transport_effect_template` gains
one bullet describing the header.

### 3.2 P1-2 — `mongo.rooms` owner correction (+ docs cleanup from re-examination)

`tools/integration-suite-v2/catalogs/readers/mongo.rooms.yaml`: `owners: [room-service]` → `owners: [room-worker]`. Comment block updated to cite `room-worker/store_mongo.go:133` as the writer. The comment's "Owners" closing note about `dm-bootstrapper` is preserved (still accurate Part-2 work).

**Bonus docs cleanup absorbed from §9.3 audit re-examination** (one editing pass on the file picks up all four):

- **Audit-F:** the emit-shape comment lists `model.Room` fields that don't exist on `pkg/model/room.go` (`members`, `createdBy`). Rewrite to the real field set: `{id, name, type, siteId, userCount, createdAt, updatedAt}` per `pkg/model/room.go:14-31`.
- **Audit-H:** the comment opens with "Tails the `rooms` collection…" — the implementation at `tools/integration-suite-v2/internal/readers/mongo_rooms.go:33` is a 100ms polling ticker with `_id`-dedup. Change "Tails" → "Polls (100ms ticker, `_id`-dedup map)" to match the implementation. The "one event per insert" contract is unchanged.
- **Audit-I:** add a one-line caveat to the timestamp-source comment block: *"assumes writer clock ≈ suite clock; multi-host clock skew may drop writes with `createdAt < T_open` because the filter at `mongo_rooms.go:69-72` uses `$gte: start`."* Docker-local single-host is unaffected.

### 3.3 P1-3 — `room-service.yaml` write-list correction (+ docs cleanup from re-examination)

`tools/integration-suite-v2/catalogs/services/room-service.yaml` trigger on `chat.user.*.request.room.*.create` currently declares three writes: `reply`, `mongo.rooms`, `logs.room-service`. The actual room-service behavior on this path is:

| Write                                          | Catalogued today | Reality                       |
|------------------------------------------------|------------------|-------------------------------|
| `reply`                                        | yes              | correct (`room-service/handler.go:114`)    |
| `mongo.rooms`                                  | yes              | **wrong** — room-worker writes |
| `logs.room-service`                            | yes              | correct (`room-service/handler.go:110, 115`) |
| JetStream publish to `chat.room.canonical.{siteID}.create` | no   | **missing** (`room-service/handler.go:366`) |

Fix: remove `writes: mongo.rooms`; add `writes: jetstream.rooms-canonical` (new reader, see §3.5). Inline comment cites the corresponding handler line for each write.

**Bonus docs cleanup absorbed from §9.3 audit re-examination** (one editing pass picks both up):

- **Audit-E (already marked WONTFIX in §9.3):** add a one-line YAML comment above the `pattern:` field explaining that `*` in the siteID slot is deliberate — the catalog describes the subject SHAPE the service owns (deployment-agnostic), not the literal subscription string any site-bound instance issues. Prevents future maintainers from "narrowing" the pattern.
- **Audit-Q:** add a top-of-file YAML comment noting Part-1 scope: *"Part-1 scope: only the `create` trigger is catalogued today; `room-service/handler.go:63-99` RegisterCRUD subscribes to 11 subjects total. Additional triggers will be added here as their scenarios are authored, per v2 design's deliberate-growth discipline."*

### 3.4 P2-4 — Add `room-worker` service profile

`catalogs/services/room-worker.yaml` (new). Single trigger today:

- `source: jetstream-pull`
- `pattern: chat.room.canonical.{siteID}.create` (other
  canonical operations — `member.add`, `member.remove`, `member.role-update`
  — are out of scope for this spec; they will be added in their own
  fix specs when their scenarios are authored, following the same
  "look at the handler, declare exactly its writes" discipline)
- `on_trigger.writes`:
  - `mongo.rooms` (the actual insert, `room-worker/store_mongo.go:133`)
  - `logs.room-worker` (will require a `catalogs/readers/logs.room-worker.yaml`
    sibling to the existing `logs.room-service.yaml` — added in this spec)

The profile sets up the per-step service-ability check (P3-7) for any
scenario whose second step targets room-worker.

### 3.5 P2-5 — New `jetstream.rooms-canonical` reader

**YAML:** `catalogs/readers/jetstream.rooms-canonical.yaml`.

- `location: jetstream.rooms-canonical`
- `owners: [room-service]` — room-service publishes this subject (only).
- `timestamp_source`: the JetStream message metadata `Timestamp` field,
  with a fallback to the embedded payload's `timestamp` field if present
  (the canonical `CreateRoomRequest` has it per `pkg/model/member.go:152`).
- `shape: model.CreateRoomRequest` (the payload room-service marshalled
  per `handler.go:362`).
- Subject filter: `chat.room.canonical.{siteID}.>` (matches all canonical
  ops; the reader's subject-pattern field is set per scenario).
- `executor: JetStreamSubjectReader`

**Go executor:** `internal/readers/jetstream_subject.go`. Opens an
**ephemeral**, **filtered**, **deliver-new** consumer scoped to the
scenario's timeframe via `jetstream.Consume(...)`. Captured messages are
decoded into a typed event with fields `{subject, payload, header,
metadata.timestamp, metadata.sequence}` and emitted on the observer
channel. Cleanup deletes the ephemeral consumer at timeframe close. No
durable consumers; no replay; no interference with production
JetStream consumers.

The reader's emit-shape doc block calls out that `payload` is raw JSON
matching `shape:` — scenarios pair it with `matches_shape` against the
subset of `CreateRoomRequest` fields they care about
(`name`, `requesterAccount`, `roomId`).

### 3.6 P3-6 — Document `${site}` and other runtime globals

`tools/integration-suite-v2/AUTHORING.md` gains a "Runtime globals"
subsection listing:

| Global         | Provenance                                                       |
|----------------|------------------------------------------------------------------|
| `${site}`      | The `PRIMARY_SITE` env var the suite is invoked with. Resolved by the dispatcher before verb execution. |
| (future)       | `${primary_site}`, `${secondary_site}` (Part-2, multi-site)      |

The runtime resolution code already handles `${site}` (the existing
sample scenario uses it and passes validate). This is purely a doc
addition so authors can use them deliberately rather than copying from
examples.

### 3.7 P3-7 — Check #5 wording for synthetic readers

`.claude/commands/scenario-author.md` Step 4 row #5 + the matching row
in `2026-05-24-scenario-author-command-design.md` §4.3 gain one
clarifying sentence:

> Readers with `owners: []` are dispatcher-synthetic (e.g., `reply`)
> and skip the owner-equality clause; the service-profile `writes:`
> declaration is still required.

Behavior of the existing validator is unchanged; this is text only.

### 3.8 P4-8 — Re-author the sample scenario

Once §3.1–§3.7 land, regenerate `scenarios/drafts/verified-user-creates-channel-room.yaml`
via `/scenario-author`. The corrected scenario:

- Cites `room-service/handler.go:296-374 + pkg/model/event.go:245` as `source:`.
- Sends a payload that classifies as a channel AND passes
  `handleCreateRoomChannel`'s "users∪orgs∪channels must be non-empty"
  guard (i.e., includes `users: [${other.account}]` with a second
  placeholder).
- Asserts the actual `CreateRoomReply` fields: `status: "accepted"`,
  `roomType: "channel"`.
- (Optional, Part-2-shaped) adds a second sequence step targeting
  `room-worker` with `mongo.rooms` and `logs.room-worker` reads, now
  that the catalog supports it.

**Subsequent scenarios added beyond P4-8** (post first end-to-end run, 2026-05-24): the corrected `verified-user-creates-channel-room.yaml` was extended into a family of four:

| File | Type | Asserts |
|---|---|---|
| `verified-user-creates-channel-room.yaml` | + happy (single-step) | reply `body_json: {status: accepted, roomType: channel}` |
| `room-service-create-room-persists-via-room-worker.yaml` | + happy (pipeline) | step 1 reply + step 2 `mongo.rooms` with identity-level fields via placeholder substitution (`createdBy: ${requester.id}`, `name: ${input.payload.name}`, …) |
| `channel-name-too-long-rejected.yaml` | − negative | reply `body_json.error = "channel name must be at most 100 characters"` (name = 101 `x` chars; exact text from `room-service/helper.go:42`) |
| `empty-create-request-rejected.yaml` | − negative | reply `body_json.error = "request must include at least one of users, orgs, channels, or name"` (payload `{}`; exact text from `room-service/helper.go:34`) |

`make -C tools/integration-suite-v2 validate` reports `scenarios: ok — 4 drafts parsed`.

### 3.10 P1-J — Rich reply payload (capture-and-emit, matchers interpret)

**Cause.** `catalogs/readers/reply.yaml:14` (the emit-shape comment) promises a `transport_error` field on every reply event. The Go implementation drops three things on the floor:

- The error itself: `internal/runtime/dispatcher.go:53-55` calls `Inject(out.Reply, ...)` — `out.Err` is never passed.
- The NATS headers: `internal/readers/nats_reply.go:39-51` `Inject` only accepts `reply []byte`.
- The latency: nowhere captured.

The `Event` struct at `internal/readers/types.go:13-19` has no field for any of them. Result: on transport failure the matcher sees an empty `[]byte` payload and cannot tell it apart from a successful empty reply; negative-path scenarios are impossible; no header assertion is possible.

**Resolution — chosen approach (locked 2026-05-24).** Reader does not classify or interpret; it captures every observable fact about the reply and emits a structured payload. Matchers do the matching.

**The structured payload.** New type in a new file `tools/integration-suite-v2/internal/readers/reply_payload.go`:


```go
type ReplyPayload struct {
    BodyJSON  map[string]any      `json:"body_json,omitempty"`   // parsed body when JSON-decodable
    BodyRaw   string              `json:"body_raw,omitempty"`    // raw string fallback when body is not JSON
    Header    map[string][]string `json:"header,omitempty"`      // every NATS header on the reply
    Error     string              `json:"error,omitempty"`       // verb Outcome.Err.Error() verbatim; empty on success
    LatencyMs int64               `json:"latency_ms"`            // fire → reply (or fire → error)
}
```

A small helper in the same file builds `ReplyPayload` from `verbs.Outcome` + measured latency:

- Try `json.Unmarshal(out.Reply, &m)` → success → `BodyJSON: m`, `BodyRaw: ""`.
- Otherwise → `BodyJSON: nil`, `BodyRaw: string(out.Reply)`.
- `Header` copied verbatim from `out.Header` (added in `verbs.Outcome` per §3.1 P0-1 work, extended here).
- `Error` = `out.Err.Error()` verbatim (or empty if `out.Err == nil`).

**Matcher access.** `matches_shape` is the natural matcher: scenarios assert on field names. Two example reads:

```yaml
# Happy path
- location: reply
  matcher: matches_shape
  expected:
    body_json: { status: "accepted", roomType: "channel" }

# Negative — no responders for the subject (e.g., room-service down)
- location: reply
  matcher: matches_shape
  expected:
    error: "nats_request: request: nats: no responders"

# Header echo regression (proves P0-1 X-Request-ID propagation)
- location: reply
  matcher: matches_shape
  expected:
    header: { "X-Request-Id": ["${request_id}"] }
```

`matches_shape` (`internal/matchers/matches_shape.go:17-49`) accepts `map[string]any | []byte | string` today. We extend it: when `observed` is none of those, marshal it to JSON and unmarshal as `map[string]any`. Two extra cases in the type-switch.

**What changes in the verb layer.** `verbs.Outcome` (`internal/verbs/types.go:24-29`) gains a `Header nats.Header` field. `internal/verbs/nats_request.go:56` populates it from `reply.Header` on success; leaves it nil on transport error. The dispatcher (`internal/runtime/dispatcher.go:46-55`) measures latency (already has the `time.Now()` pre-Execute pin) and passes the full `Outcome + latency` to `Inject`.

**What `Inject` becomes.** New signature on `internal/readers/nats_reply.go`:

```go
func (r *NATSReplyReader) Inject(out verbs.Outcome, latency time.Duration, traceparent string, ts time.Time, ownerSvc string)
```

Builds a `ReplyPayload`, sets it on `Event.Payload`, sends.

**What does NOT change.**

- No new matchers; `catalogs/matchers.yaml` is untouched.
- No new readers; `catalogs/readers/*.yaml` count stays the same (`reply.yaml`'s comment block is updated).
- No error classification or enum mapping anywhere. The wrapped error string is preserved verbatim.
- The `Event` struct (`internal/readers/types.go:13-19`) gains nothing — only the reader's payload type is enriched.

**Side effects (positive).**

- **Reporter ergonomics (future).** When a read fails, the reporter can `json.Marshal(Event.Payload)` and surface the full reply context — headers, latency, body, error — without bespoke per-reader formatting.
- **Header assertions become free.** Regression tests for P0-1 (X-Request-ID echo) become a single-line `header:` assertion. No matcher change.
- **Forward compatible.** If room-service later sets custom reply headers (delivery-attempt, partition hint, etc.), they appear in the event automatically. No code or catalog edit.

**Side effects (constraint).** Scenarios couple to `err.Error()` text. If the NATS library underneath rewords its error messages, affected scenarios break loudly. Mitigation: the wrapping at `internal/verbs/nats_request.go:53` (`fmt.Errorf("nats_request: request: %w", err)`) is ours and stays stable; a NATS-library upgrade that shifts the inner message is a deliberate PR that touches affected scenarios.

### 3.11 P3-9 — Docs polish on `logs.room-service` reader + Reader interface (Audit-L, M, O)

Three small text-only cleanups, no behavior change. Bundled because they all touch the logs/reader subsystem.

- **Audit-L** (`tools/integration-suite-v2/catalogs/readers/logs.room-service.yaml:13-15`): the comment promises that on missing `time` field, the reader "tags such events so the classifier can be strict if desired." `tools/integration-suite-v2/internal/readers/container_logs.go:76-86` `parseLogTime` silently returns `time.Now()` — no tag, no flag. Update the comment to match reality: *"if the `time` field is missing or unparseable, the reader falls back to wall-clock receive time (no marker is set today; if scenarios need to distinguish, add a tag in a future PR)."*

- **Audit-M** (`tools/integration-suite-v2/internal/readers/container_logs.go:12-14`): the type-level godoc claims it "filters lines to those with a traceparent we set." The implementation at lines 46-68 emits **every** JSON line (traceparent populated when present, empty when not). The behavior is correct for verdict — the three-way classifier handles trace presence/absence at the verdict step. Just rewrite the godoc to match: *"Tails `docker logs -f <container>` from the start of the scenario and emits one Event per JSON-parseable line. Populates `Event.Traceparent` from the line's `traceparent` field when present, empty otherwise. The classifier handles trace presence/absence at verdict time."*

- **Audit-O** (`tools/integration-suite-v2/internal/readers/types.go:22-28`): the `Reader` interface godoc says nothing about cleanup expectations. Add a sentence: *"Watch goroutines MUST exit when ctx is cancelled; implementations using OS resources (subprocesses, sockets) rely on ctx-cancellation to release them. `ContainerLogsReader` uses `exec.CommandContext` which guarantees process kill on ctx cancel (verified per Go stdlib)."*

### 3.12 Synthetic trace stamping for DB readers (added 2026-05-24 after first end-to-end run; revised same day after second run for scope-awareness)

**Cause.** The verdict classifier (`tools/integration-suite-v2/internal/runtime/verdict.go`) assumes every observed event carries W3C trace context, and asks "does this event carry MY scenario's trace?" as its primary attribution test. NATS, JetStream, and slog all CAN propagate `traceparent` because production wires OTel into those transports (`room-service/main.go:12`, `room-worker/main.go:14` import `otel-nats/oteljetstream`). **DB writes cannot.** Mongo and Cassandra documents have no header slot, and OpenTelemetry does not encode trace context as document fields.

Net consequence at first end-to-end run (run id `a1f2`): `MongoRoomsReader` emitted `Event.Traceparent=""` for every room-worker insert. The classifier saw no trace, no profile lookup wired (runner passed `nil`) → events landed in the **anomaly** bucket. Two positive scenarios failed despite production behaving correctly end-to-end.

**First-pass resolution (commit `5e16639`).** Suite generates the scenario's traceparent ONCE in `runner.runOneCase` and hands it to both the dispatcher AND the observer; the observer forwards to each reader's `Watch(ctx, traceparent, start)`. DB readers stamp `Event.Traceparent = traceparent` on every emit. Anomalies disappeared (3/4 in run `7973`).

**Revised resolution — scope-aware stamping at the Observer (commit `<NEXT>`).** The first-pass approach was too greedy: it stamped EVERY DB event with our trace regardless of the writing service. So when a minimal scenario (e.g. `verified_user_creates_channel_room`, single-step room-service-only) ran, room-worker's downstream `mongo.rooms` write got our trace → classified as in-cascade → demanded an expected read the scenario hadn't declared → `unexpected-cascade` (FAIL).

The architectural insight: trace tells you "the production system produced this event because of a NATS request labeled with my trace." **Scope** tells you "this event came from a service THIS scenario is responsible for testing." Both are needed.

The fix:

1. **Stamping moved from the reader to the Observer's merge loop.** `MongoRoomsReader.Watch` no longer touches `Event.Traceparent`. The Observer (`tools/integration-suite-v2/internal/runtime/observer.go`) inspects every event arriving from any reader: if `Event.Traceparent == ""` AND `Event.OwnerSvc` is in this scenario's `inScopeServices` (derived from `s.Sequence`), the Observer stamps the scenario's traceparent. Otherwise the event passes through untouched.

2. **`profileLookup` wired in the runner.** Previously `Classify` received `nil`, so out-of-trace events always went to the anomaly bucket. Now the runner builds an `AbilityProfileLookup` (`buildProfileLookup`) from the loaded catalog's service profiles. Given a service name and an event, it returns true if the event's `Location` is in any of that service's declared `on_trigger.writes`. The classifier uses this to filter background events.

The combined effect:

| Scenario shape | Mongo event from room-worker arrives... | Observer stamps? | Verdict classification |
|---|---|---|---|
| Pipeline (room-worker IS in s.Sequence) | yes | yes — trace stamped | in-cascade → match against expected read for mongo.rooms ✓ |
| Minimal (room-worker NOT in s.Sequence) | yes | no — stays trace-less | out-of-trace → profileLookup: room-worker.yaml declares `writes: mongo.rooms` → background, ignored ✓ |

Per-step trace check is unchanged. The Reader interface's `traceparent` param remains (kept for uniform signature; readers MAY use it for source-side filtering in the future), but synthetic stamping is now an Observer-side concern.

**Reader interface contract (revised).** Readers populate `Event.Traceparent` IFF their underlying source carries trace natively (NATS reply header, slog JSON field, JetStream message header). Storage readers (Mongo, Cassandra) leave it empty. The Observer's scope-aware stamping decides whether to attribute the event to this scenario.

**Limitation (unchanged from first-pass).** Synthetic stamping holds only while scenarios are serial. Concurrent scenarios with overlapping observation windows would both stamp their respective trace IDs onto each other's events from in-scope services. The forward path remains fingerprint-based correlation — the scenario declares an identity predicate (`createdBy: ${requester.id} AND name: ${input.payload.name}` — already expressible via the placeholder substitution work) and the classifier matches events to scenarios by content, not by stamp.

**Files (revised resolution lands in commit `<NEXT>`):**
- `tools/integration-suite-v2/internal/readers/types.go` — Reader interface contract clarified: storage readers leave Traceparent empty; Observer does scope-aware stamping
- `tools/integration-suite-v2/internal/readers/mongo_rooms.go` — reverted to NOT stamping; Watch's traceparent param renamed to `_`
- `tools/integration-suite-v2/internal/runtime/observer.go` — `Start` signature gains `inScopeServices []string`; merge loop stamps trace IFF empty AND OwnerSvc in scope
- `tools/integration-suite-v2/internal/runtime/runner.go` — keeps the loaded `*catalog.Catalog`; builds `profileLookup` via new `buildProfileLookup`; passes inScopeServices to observer.Start; passes profileLookup to Classify

## 4. Files this design creates or changes

| Path                                                                                  | Action                                                                                     |
|---------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------|
| `tools/integration-suite-v2/seed/users.json`                                          | New — array of `model.User` documents (alice, bob, + 2 others per §3.0 straw-man)          |
| `tools/integration-suite-v2/seed/rooms.json`                                          | New — array of `model.Room` documents (r-engineering, r-design)                            |
| `tools/integration-suite-v2/seed/subscriptions.json`                                  | New — array of `model.Subscription` documents covering owner/member/non-member matrix      |
| `tools/integration-suite-v2/seed/README.md`                                           | New — 1-pager: "edit these alongside `catalogs/fixture-cast.yaml`; PR description must mention both" |
| `tools/integration-suite-v2/seed/loader.go`                                           | New — `//go:embed *.json` (must live next to embedded files), package `seed`, exposes `Decoded()` + `LoadAll(ctx, db)`; drops + InsertMany per collection |
| `tools/integration-suite-v2/seed/loader_test.go`                                      | New — unit tests for `Decoded()`: counts, user-shape guard, role-matrix, room shape, subscription shape (no infra)                                       |
| `tools/integration-suite-v2/seed/loader_integration_test.go`                          | New (`//go:build integration`) — testcontainer Mongo, covers `LoadAll` populates all collections, bson tags land correctly, idempotent re-load          |
| `tools/integration-suite-v2/internal/fixtures/seeder.go`                              | Unchanged behaviorally — keeps JWT minting; loader runs first in suite startup             |
| `tools/integration-suite-v2/catalogs/fixture-cast.yaml`                               | Rewrite — Shape B (separate `subscriptions:` edges); faithful annotation of seed JSON      |
| `tools/integration-suite-v2/internal/runtime/runner.go` (or per-test hook)            | Call loader at start of every scenario test (per §3.0 locked decision)                     |
| `tools/integration-suite-v2/internal/verbs/nats_request.go`                           | Set `X-Request-ID` header; populate `Outcome.RequestID`                                    |
| `tools/integration-suite-v2/internal/verbs/types.go`                                  | Add `RequestID string` to `Outcome` (P0-1); also add `Header nats.Header` (P1-J)            |
| `tools/integration-suite-v2/internal/readers/reply_payload.go`                        | **(P1-J)** New — `ReplyPayload` struct + helper that builds it from `verbs.Outcome` + latency (JSON-decode body if possible, capture headers, capture verbatim error string) |
| `tools/integration-suite-v2/internal/readers/nats_reply.go`                           | **(P1-J)** `Inject` signature changes to `Inject(out verbs.Outcome, latency time.Duration, traceparent, ts, ownerSvc)`; sets `Event.Payload` to `ReplyPayload`                |
| `tools/integration-suite-v2/internal/readers/nats_reply_test.go`                      | **(P1-J)** New — covers success-JSON-body / success-non-JSON-body / no-responders / timeout, asserting `Event.Payload` shape per case                                       |
| `tools/integration-suite-v2/internal/runtime/dispatcher.go`                           | **(P1-J)** Capture latency at `time.Now()` pin (lines 31-32 today already capture `startTime`); pass full `out` + latency to `Inject` (line 54)                            |
| `tools/integration-suite-v2/internal/matchers/matches_shape.go`                       | **(P1-J)** Extend type-switch (lines 24-37) — when `observed` is not map/bytes/string, marshal to JSON and unmarshal as `map[string]any` so struct payloads work transparently |
| `tools/integration-suite-v2/catalogs/readers/reply.yaml`                              | **(P1-J)** Replace the `transport_error` line in the emit-shape comment with the new fields: `body_json`, `body_raw`, `header`, `error`, `latency_ms`; cite `internal/readers/reply_payload.go` |
| `tools/integration-suite-v2/internal/verbs/nats_request_test.go`                      | New — covers ID generation, header set, echo on success/error/timeout                      |
| `tools/integration-suite-v2/catalogs/verbs/nats_request.yaml`                         | `transport_effect_template` gains "sets X-Request-ID (UUIDv7) header per CLAUDE.md §3"     |
| `tools/integration-suite-v2/catalogs/readers/mongo.rooms.yaml`                        | `owners: [room-worker]`; comment cites `room-worker/store_mongo.go:133`; **(P3-9 bundle)** also fix emit-shape field list (Audit-F), change "Tails" → "Polls" (Audit-H), add clock-skew caveat (Audit-I) |
| `tools/integration-suite-v2/catalogs/services/room-service.yaml`                      | Drop `writes: mongo.rooms`; add `writes: jetstream.rooms-canonical`; per-line comments; **(P3-9 bundle)** add the wide-siteID comment (Audit-E) + Part-1-scope comment (Audit-Q) |
| `tools/integration-suite-v2/catalogs/services/room-worker.yaml`                       | New — single trigger on `chat.room.canonical.{siteID}.create`, writes mongo.rooms + logs   |
| `tools/integration-suite-v2/catalogs/readers/jetstream.rooms-canonical.yaml`          | New — `owners: [room-service]`, subject filter, shape `model.CreateRoomRequest`            |
| `tools/integration-suite-v2/catalogs/readers/logs.room-worker.yaml`                   | New — mirrors `logs.room-service.yaml` for the new service profile                         |
| `tools/integration-suite-v2/internal/readers/jetstream_subject.go`                    | New — `JetStreamSubjectReader` executor (ephemeral consumer, timeframe-scoped)             |
| `tools/integration-suite-v2/internal/readers/jetstream_subject_test.go`               | New — exercises ephemeral-consumer lifecycle + decode + cleanup (testcontainers)           |
| `tools/integration-suite-v2/internal/readers/registry.go`                             | Register `JetStreamSubjectReader`                                                          |
| `tools/integration-suite-v2/AUTHORING.md`                                             | New "Runtime globals" subsection                                                           |
| `.claude/commands/scenario-author.md`                                                 | One-sentence clarification on check #5 for `owners: []` readers                            |
| `docs/superpowers/specs/2026-05-24-scenario-author-command-design.md`                 | Same one-sentence clarification in §4.3 row #5                                             |
| `tools/integration-suite-v2/scenarios/drafts/verified-user-creates-channel-room.yaml` | Rewrite — corrected source, payload, expected reply; optionally a second `room-worker` step |
| `tools/integration-suite-v2/catalogs/readers/logs.room-service.yaml`                  | **(P3-9)** Replace the strictness-tag promise (lines 13-15) with the honest fallback note (Audit-L)            |
| `tools/integration-suite-v2/internal/readers/container_logs.go`                       | **(P3-9)** Rewrite the type-level godoc (lines 12-14) to match actual no-filter behavior (Audit-M)             |
| `tools/integration-suite-v2/internal/readers/types.go`                                | **(P3-9)** Add cleanup-expectation sentence to `Reader` interface godoc (lines 22-28) (Audit-O)                |

## 5. Acceptance criteria

1. **P0-A unit.** `loader_test.go` parses the seed JSON files, opens a
   testcontainer Mongo, performs drop + InsertMany for all three
   collections, and verifies row counts + field shapes match the JSON.
   Re-running the loader against the same Mongo produces identical
   state (idempotent via drop-first).
2. **P0-A end-to-end.** With docker-compose stack up and the loader
   wired into scenario startup, `room-service`'s `GetUser` against any
   seeded account (e.g., `alice`) returns a `model.User` with non-empty
   `EngName` / `ChineseName` — verified by issuing a NATS request for
   `chat.user.alice.request.rooms.list` (which goes through `GetUser`)
   and getting a non-error reply.
3. **P0-1.** `make -C tools/integration-suite-v2 test` passes with
   the new `nats_request_test.go` covering: header set, ID format
   (`idgen.IsValidUUID` returns true), `RequestID` populated on
   success, transport-error, and timeout outcomes.
4. **P0-1 end-to-end.** With docker-compose stack up + P0-A loader
   in place, the suite can request
   `chat.user.alice.request.room.local.create` and observe a
   non-error reply (i.e., the request now passes BOTH the
   X-Request-ID header guard AND the user-row guard). Either via a
   unit-level integration test against a real room-service, or via
   the re-authored sample scenario actually running.
5. **P1-J unit.** `nats_reply_test.go` covers the four reply outcomes
   (success-JSON-body / success-non-JSON-body / no-responders /
   timeout). For each, asserts that `Event.Payload` is a `ReplyPayload`
   with the expected field set (e.g., transport-error case has
   `Error != ""` and `BodyJSON == nil`; success case has `BodyJSON`
   populated and `Error == ""`). Also asserts headers are captured
   verbatim on success.
6. **P1-J matcher integration.** `matches_shape_test.go` covers a
   scenario-shaped assertion against a struct payload — e.g., observed
   is a `ReplyPayload` value, expected is
   `map[string]any{"body_json": ..., "header": ...}` — confirming the
   marshal-and-unmarshal fall-through path works without the scenario
   author having to pre-flatten.
7. **P1-2 + P1-3 + P2-4.** `make -C tools/integration-suite-v2 validate`
   passes with all catalog edits in place. The catalog validator (if
   it cross-checks owners against code) reports no drift.
8. **P2-5.** `JetStreamSubjectReader`'s test exercises the ephemeral
   consumer's create/consume/cleanup lifecycle against a real NATS
   testcontainer and decodes a published `CreateRoomRequest` payload
   back to a typed event.
9. **P3-6 + P3-7.** `AUTHORING.md` and `.claude/commands/scenario-author.md`
   contain the updated text; the existing slash command flow still
   passes the existing sample scenario through the five-surface check
   (regression check for backward compatibility).
10. **P4-8.** The re-authored sample scenario loads via
    `make validate`, and (with stack up) classifies as PASS — i.e., the
    reply read matches `{body_json: {status: "accepted", roomType: "channel"}}`.

## 6. Implementation order

The order minimises rework and lets each step be independently
verifiable:

1. **P0-A** (static seed JSON + loader + cast YAML rewrite). MUST land
   first — without `users` rows in Mongo, no room-service scenario
   produces a non-error reply even after P0-1 lands.
2. **P0-1** (nats_request header + Outcome field + tests). Self-contained
   Go change; unblocks every future end-to-end test once P0-A is in.
3. **P1-J** (rich reply payload — `ReplyPayload`, `Outcome.Header`,
   `Inject` signature, `matches_shape` struct handling, reply.yaml
   comment, tests). Lands on top of P0-1 because both extend `Outcome`;
   coherent to do in sequence so reviewers see the full reply-pipeline
   picture in one diff.
4. **P1-2, P1-3** (catalog data fixes). Trivial YAML edits.
5. **P2-4, P2-5** (room-worker profile + JetStream reader). Bigger; can
   be split into two commits if reviewer prefers. Note P1-3's new
   `writes: jetstream.rooms-canonical` line depends on P2-5 landing
   first.
6. **P3-6, P3-7, P3-9** (docs). Text-only. P3-9 is the cleanup
   bundle (Audit-L/M/O) on the logs reader and `Reader` interface
   godoc. P1-2 and P1-3 already absorbed the other text-only audit
   fixes (F/H/I/E/Q) as part of editing those files.
7. **P4-8** (re-author scenario). Final pass via `/scenario-author`,
   verifies all the above end-to-end. The corrected sample scenario
   uses `body_json:` per P1-J's new emit shape.

Each step lands as its own commit on
`claude/integration-test-automation-LXQHP`. No new PRs are opened by
this spec; PR creation remains a separate, user-initiated action.

## 7. Risks

| Risk                                                                          | Mitigation                                                                                   |
|-------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------|
| **Drift between `seed/*.json` and `catalogs/fixture-cast.yaml`.** Cast says `alice has role owner of r-engineering`; subscription JSON says `alice is member`. Scenario picks alice as "owner," verb fails with permission denied, failure looks mysterious. | **Accepted by user 2026-05-24.** Managed via review discipline: edits to seed and cast land in the same commit and are called out in the PR description. No code-level validator for Part-1. If drift starts to bite (recurring "mysterious permission" failures), revisit per §9. |
| **Per-scenario drop+reload latency.** Mongo drop + InsertMany of users + rooms + subscriptions runs before every scenario test. Acceptable today (one scenario); could be slow at scale. | **Accepted by user 2026-05-24** as the Part-1 cost of having a clean baseline per scenario. Future optimization captured in §9. |
| Adding the `X-Request-ID` header silently changes scenario semantics for users who relied on its absence (none today, but worth flagging) | The header was always required by the system under test. No scenario could pass without it. |
| `JetStreamSubjectReader`'s ephemeral consumer leaks if cleanup races with timeframe close | Cleanup deletes the consumer via `iter.Stop()` + `js.DeleteConsumer`; tests cover the leak path. |
| Future canonical operations (`member.add`, etc.) require similar room-worker profile expansion and may surface their own P1-class bugs | Each future scenario authoring pass will hit the five-surface check against an incomplete profile and surface the gap — this is the intended Part-1 → Part-2 expansion cycle. |
| The re-authored sample scenario adds a second placeholder (`other`); the cast has three users, sufficient today, but adding more two-actor scenarios may exhaust cast diversity | Cast growth is a separate, deliberate PR (per v2 design §3.2 "Cast growth"). Not changed here. |

## 8. Out of scope (deferred Part-2)

- Cassandra readers (`messages_by_room`, `thread_messages_by_room`).
- Other Mongo readers (`subscriptions`, `room_members`, `users`).
- Outbox / Inbox JetStream readers for cross-site scenarios.
- Other canonical operations (`member.add`, `member.remove`,
  `member.role-update`) on the room-worker profile.
- Other service profiles (`message-worker`, `broadcast-worker`,
  `notification-worker`, `message-gatekeeper`, `inbox-worker`,
  `search-sync-worker`, `auth-service`).
- A `Validator` upgrade that semantically cross-checks reader
  `owners:` against the actual Go write call sites (the bugs in this
  spec would have been caught by such a validator at startup).

## 9. Future TODOs (deferred — captured here so they don't get lost)

Items the author and reviewer agreed are worth doing eventually but
that are not blocking the immediate room-service scenario goal.
Anything moved into the main spec body (§3) should be removed from
this list.

### 9.1 Cast-driven seeder (replacement for static JSON)

**Today:** seed JSON is the source of truth; `fixture-cast.yaml`
annotates it. Drift is possible (mitigated by review discipline per §7).

**Future shape:** `fixture-cast.yaml` becomes the SOURCE of truth.
Tags carry world-state intent (`has_user_record`, `owner_of:r-X`,
`member_of:r-Y`). A Go seeder reads the cast and materializes Mongo
state from it via a documented **tag → behavior** mapping
(`has_user_record → InsertOne(users) with deterministic shape`;
`owner_of:r-X → InsertOne(subscriptions) with roles:[owner]`; …). Every
tag the cast uses must have a registered handler — loud fail at suite
startup otherwise. This is the original design (per v2 platform spec
§3.2) that we deferred for time-to-first-scenario.

**Trigger to revisit:** static seed grows past ~50 entities; OR
scenarios start needing runtime-parameterized world (different sites,
different sizes, generated data); OR drift-related failures become
recurring.

**Why it's not now:** the static JSON approach is ~30 LOC and ships in
hours; the cast-driven seeder is ~300 LOC plus the tag-handler
registry, and we don't need its full power for the handful of Part-1
scenarios. We pay the drift-risk premium for the velocity.

### 9.2 Per-run instead of per-scenario seed reload

**Today:** every scenario test starts with drop + InsertMany. Latency
is tolerable for the single Part-1 scenario.

**Future shape:** load once per `make local` run; namespace each
scenario's mutations under an `it-<runID>-<scenarioID>-` prefix that
gets cleaned up at scenario end. World stays consistent across the
run; tear-down is cheap.

**Trigger to revisit:** scenario count crosses ~10; OR per-scenario
reload latency exceeds ~1s.

### 9.3 Remaining audit findings not yet decided

The 2026-05-24 catalog audit surfaced ~15 findings beyond the original
8. P0-A is resolved in §3.0; the rest sit in the author's backlog and
will surface as Concerns #2..#N. Each will land in this spec's §3 or
be added here as a deferred item once decided. Quick index of
not-yet-decided audit findings:

| Audit ID | Short description | Status |
|----------|-------------------|--------|
| Audit-J | `reply.yaml` promises `transport_error` field; `nats_reply.go:39-51` never emits it. Negative scenarios cannot assert on transport failures. | **RESOLVED** as P1-J in §3.10 (2026-05-24). Rich `ReplyPayload` captures body + headers + raw error string + latency; matchers interpret. |
| Audit-B | Dispatcher hardcodes `nats_request → NATSRequestExecutor` (`tools/integration-suite-v2/internal/runtime/dispatcher.go:60-65`); YAML `executor:` field never read by runtime. | **DEFERRED to §9.4** (2026-05-24). One verb today → no impact. Bundle with C/D/K — single validator-rebuild item. |
| Audit-C | Reader registry uses `location` name (`tools/integration-suite-v2/internal/runtime/runner.go:75-79`); YAML `executor:` decorative for all three readers. | **DEFERRED to §9.4** (2026-05-24). Same bundle. |
| Audit-D | Validator (`tools/integration-suite-v2/cmd/validator/main.go:32-37`) is a hand-maintained 4-entry `knownExecutors` map; no reflection vs Go registry. | **DEFERRED to §9.4** (2026-05-24). Same bundle. |
| Audit-E | `room-service.yaml` pattern uses `*` for siteID; real subscription has concrete siteID. | **WONTFIX — by design** (2026-05-24). The catalog describes the subject SHAPE the service owns (deployment-agnostic); wide `*` lets the classifier tag cross-site room-service activity as background. Folded into §3.3 P1-3 as a one-line explanatory YAML comment. |
| Audit-F | `tools/integration-suite-v2/catalogs/readers/mongo.rooms.yaml:9-11` lists `model.Room` fields including `members` and `createdBy` which don't exist on `pkg/model/room.go:14-31`. | **RESOLVED in §3.2 P1-2 bonus cleanup** (2026-05-24). Same editing pass that fixes the owners field rewrites the field list. |
| Audit-G | `tools/integration-suite-v2/catalogs/readers/reply.yaml:10` cites `model.Room` for room.create; real reply is `model.CreateRoomReply`. | **RESOLVED by §3.10 P1-J** (2026-05-24). P1-J rewrites the whole reply.yaml comment block to describe the new rich emit shape. |
| Audit-H | `tools/integration-suite-v2/catalogs/readers/mongo.rooms.yaml:5` opens with "Tails…"; `tools/integration-suite-v2/internal/readers/mongo_rooms.go:33` is actually a 100ms polling ticker. Contract "one event per insert" holds. | **RESOLVED in §3.2 P1-2 bonus cleanup** (2026-05-24). Comment changed to "Polls (100ms ticker, `_id`-dedup map)" to match impl. False positive on bug; docs-only fix. |
| Audit-I | `tools/integration-suite-v2/internal/readers/mongo_rooms.go:69-72` filter `createdAt >= start` drops late writes with skewed clocks; catalog promised out-of-order classification. | **RESOLVED in §3.2 P1-2 bonus cleanup** (2026-05-24). One-line "assumes writer clock ≈ suite clock" caveat added to the mongo.rooms.yaml timestamp-source comment. Docker-local single-host is unaffected. |
| Audit-K | `tools/integration-suite-v2/catalogs/readers/logs.room-service.yaml:28` uses bracketed-parameter syntax `ContainerLogsReader[room-service]` that no code parses. | **DEFERRED to §9.4** (2026-05-24). Field is decorative (per C), so the fictional syntax has no runtime impact. When the validator rebuild lands, either implement `[param]` syntax or rename to plain `ContainerLogsReader` with constructor args. |
| Audit-L | `tools/integration-suite-v2/catalogs/readers/logs.room-service.yaml:13-15` promises strictness-tag on missing time; `tools/integration-suite-v2/internal/readers/container_logs.go:76-86` silently uses `time.Now()`. | **RESOLVED in §3.11 P3-9** (2026-05-24). Comment rewritten to honest fallback note. |
| Audit-M | `tools/integration-suite-v2/internal/readers/container_logs.go:12-14` godoc claims traceparent filtering; impl at lines 46-68 emits every JSON line. | **RESOLVED in §3.11 P3-9** (2026-05-24). Godoc rewritten to match actual no-filter behavior. Verdict-side classification handles trace presence/absence correctly. |
| Audit-N | `tools/integration-suite-v2/catalogs/readers/mongo.rooms.yaml:5` "pre-existing rooms NOT replayed" is vacuously true today (`tools/integration-suite-v2/catalogs/fixture-cast.yaml:40` is `rooms: []`). | **RESOLVED by §3.0 P0-A** (2026-05-24). Once seed JSON lands with rooms like `r-engineering`, the `^it-<runID>-` filter at `mongo_rooms.go:69-72` continues to exclude them non-vacuously. Verify after P0-A implementation. |
| Audit-O | `Reader` interface (`tools/integration-suite-v2/internal/readers/types.go:22-28`) has no lifecycle docs. Audit also alleged `docker logs -f` subprocess leak. | **RESOLVED in §3.11 P3-9** (2026-05-24). Docs sentence added to the interface godoc. The leak claim was a false positive: `exec.CommandContext` at `container_logs.go:30` guarantees process kill on ctx cancel per Go stdlib. |
| Audit-P | `tools/integration-suite-v2/catalogs/readers/reply.yaml:23` `owners: []` works because validator loops empty list — no explicit synthetic-reader branch. | **User-facing RESOLVED by §3.7 P3-7** (2026-05-24). Structural fix (explicit synthetic-reader branch in validator) is part of the §9.4 validator-rebuild bundle. |
| Audit-Q | `tools/integration-suite-v2/catalogs/services/room-service.yaml` declares 1 trigger; `room-service/handler.go:63-99` subscribes to 11 subjects. | **WONTFIX — Part-1 scope** (2026-05-24). Folded into §3.3 P1-3 as a top-of-file YAML comment noting the scope. Additional triggers added as their scenarios are authored. |

This list is the authoritative backlog. When a row is decided, move it
into §3 with full detail; when it's deferred, give it its own §9.x
section.

### 9.4 Validator-rebuild bundle (replaces deferred Audit-B / C / D / K)

Four audit rows describe the same underlying gap: the catalog's
`executor:` field, intended by v2 design as the binding from YAML name
to Go symbol, is **decorative** today — the dispatcher hardcodes the
verb-to-executor mapping (Audit-B), the reader registry uses location
names instead (Audit-C), the validator hand-maintains a 4-entry map
(Audit-D), and the bracketed-parameter syntax for parameterised
readers (Audit-K) is fictional. None of this bites today because
there is one verb, three readers, and a hand-list big enough to cover
them.

**The full fix** is a single coherent change touching:

| File                                                              | Change                                                                                       |
|-------------------------------------------------------------------|----------------------------------------------------------------------------------------------|
| `tools/integration-suite-v2/internal/runtime/dispatcher.go:60-65` | Replace `verbExecutorName` hardcoding with a registry lookup by YAML executor name           |
| `tools/integration-suite-v2/internal/runtime/runner.go:75-79`     | Register readers by YAML executor name as well as location, OR resolve executor → location at load |
| `tools/integration-suite-v2/internal/catalog/validator.go`        | Replace the hardcoded `knownExecutors` map with reflection over the verb + reader registries; add explicit synthetic-reader branch (closes Audit-P structural half) |
| `tools/integration-suite-v2/cmd/validator/main.go:32-37`          | Delete the hand-list; import the runtime registries instead                                  |
| `tools/integration-suite-v2/catalogs/readers/logs.room-service.yaml:28` | Pick a story for parameterised executors: either implement `Executor[param]` parsing, or rename to plain `ContainerLogsReader` with constructor args declared elsewhere |

**Trigger to revisit.** Any of:
- A second verb is added (currently we have `nats_request` only; if Part-2 adds `nats_publish`, `http_request`, `jetstream_publish`, etc., the dispatcher's hardcoded switch starts hurting).
- A fourth reader executor type is added (we have three: `NATSReplyReader`, `MongoRoomsReader`, `ContainerLogsReader`. The `JetStreamSubjectReader` from §3.5 makes it four — if the registration ergonomics start to drag, this trigger has fired).
- A new structural class of catalog-vs-code drift surfaces that the hand-list validator silently allowed.

**Estimated size:** ~150-250 LOC across the four files plus tests, plus a small extension to validator unit tests to cover the registry-driven path. Doable as a single PR.

**Why not now.** P0-A through P4-8 are the path to the first runnable room-service scenario. The validator rebuild is a structural improvement that pays dividends as the catalog grows but doesn't change what the suite can do today. We accept the technical debt for velocity.

### 9.5 Room-worker scenario enablement (bucket list, added 2026-05-24)

Authoring the first room-worker scenario (a multi-step pipeline that
drives room-worker by firing a user-level create against room-service)
surfaced concrete gaps the suite needs to close before room-worker
behavior can be asserted on. The list is roughly minimal-blast-radius
first; each row cites the evidence so the implementer doesn't have to
re-derive it. All items respect §2.1 black-box discipline — none of
them propose changes to production code.

#### Reader bugs

| ID | Gap | Evidence | Why it matters |
|----|---|---|---|
| **RW-1** | **RESOLVED 2026-05-24.** `mongo.rooms` reader's `_id ~ ^it-<runID>-` filter excluded every scenario-created room (IDs are server-generated by `pkg/idgen.GenerateID()`, no prefix; room-worker wrote that verbatim). Fix landed: dropped the `_id` regex; filter is now `createdAt >= start` alone. Seeded rooms still excluded by timestamp (`2025-01-01T00:00:00Z`). Also corrected `OwnerSvc` on emitted events from `"room-service"` to `"room-worker"` (matches the catalog's owners list per P1-2). Catalog comment in `mongo.rooms.yaml` updated. |
| **RW-2** | No way to filter `mongo.rooms` events to "the room THIS scenario just created." Currently emits every insert in the window. | `mongo_rooms.go:54-60` (Event constructed from raw doc; no scenario-aware filtering). | Strict per-scenario identity assertions. Workable today via shape-matching on placeholder-derived `name`; revisit if scenarios start needing tighter identity. |

#### Production observability gaps that DO NOT motivate suite changes

| ID | What we noticed | Why we are not acting |
|----|---|---|
| ~~**RW-3 (dropped)**~~ | `room-worker/handler.go` has no `slog.Info`/`slog.Debug` on the happy-path create — every `slog.*` is `Warn` or `Error`. `logs.room-worker` is therefore useless for happy-path assertion via `contains`/`matches_shape`. | Per §2.1, **the suite does not motivate production code changes.** If room-worker's silence on success is a real operational hygiene gap, raise it through CLAUDE.md §3 logging-discipline channels — not here. The suite's response is to assert on a different existing signal (Mongo write via RW-1, JetStream canonical via P2-5's `jetstream.rooms-canonical`, async-job-result via RW-9 below). |

#### Missing readers (observability that exists in production but the suite doesn't capture yet)

| ID | What | Evidence (production already emits) | When to add |
|----|---|---|---|
| **RW-4** | `mongo.subscriptions` reader. room-worker writes one subscription row per member when it creates a channel — that's the actual "alice is now in r-foo" record. | `room-worker/store_mongo.go:28` initialises `subscriptions: db.Collection("subscriptions")`; multiple Find/Insert/Delete call sites at lines 40, 170, 181, 194, 298, 306, 333, 420, 444. | When the first role-based scenario (owner-of-room, member-but-not-owner) needs it. |
| **RW-5** | `mongo.room_members` reader. Distinct from `subscriptions`; used for org-based membership lookups. | `room-worker/store_mongo.go:29` (`roomMembers: db.Collection("room_members")`); lookups at lines 210, 266. | When the first org-membership scenario needs it. |
| **RW-6** | NATS subscriber reader for `chat.user.<account>.subscription.update`. room-worker publishes after every membership change so user-facing clients get notified. | `room-worker/handler.go:248, 411, 916` publish to `subject.SubscriptionUpdate(...)`. | When asserting "user got the membership-change notification" matters. Pairs with RW-12 (new verb / subscribe-style reader). |
| **RW-7** | NATS subscriber reader for `chat.room.<roomID>.event`. The broadcast-worker's input subject. | `room-worker/handler.go:428, 617, 940`. | Cross-checking the broadcast pipeline. Probably Part-2 (broadcast-worker scenarios). |
| **RW-8** | Cross-site outbox reader. room-worker emits outbox events for members on a different site. | `room-worker/handler.go:264-274` publishes to `subject.Outbox(h.siteID, user.SiteID, ...)`. | Federation scenarios. Already on §8 Part-2 deferred list. |
| **RW-9** | Reader for the async-job-result publish. room-worker emits a success/failure event to the requester's reply subject as the final step of every canonical op. | `room-worker/handler.go:97, 115` (`publishAsyncJobResult` → `subject.UserResponse(...)`). | Clearest "the async op succeeded end-to-end from the requester's perspective" signal. Worth catalogueing once a scenario needs it. Pairs with RW-12. |

#### Service-profile coverage

| ID | What | Evidence |
|----|---|---|
| **RW-10** | `catalogs/services/room-worker.yaml` declares only the `create` trigger. The handler dispatches on subject suffix `.create`, `.member.add`, `.member.remove`, `.member.role-update`. | `room-worker/handler.go:178` (the switch). | Each non-create operation needs its own trigger declaration + writes set as its scenarios are authored. Same Part-1-scope pattern as Audit-Q for room-service. |

#### Verb-catalog expansion

| ID | What | Why |
|----|---|---|
| **RW-11** | `jetstream_publish` verb. The only way to drive room-worker today is via room-service (multi-step pipeline). A direct-publish verb would enable isolated room-worker tests. | Part-2 verb-catalog expansion. Not blocking the immediate goal. |
| **RW-12** | `nats_subscribe` verb (or equivalent reader-style listener). For async assertions ("publish canonical, wait for subscription_update on requester subject"). | Pairs with RW-6 / RW-9. |

#### Resolver / fixture richness

| ID | What | Evidence |
|----|---|---|
| **RW-13** | Scenario resolver only walks `users[].tags`. The seed YAML has `rooms:` and `subscriptions:` sections (per §3.0) but predicates like `owner_of:r-engineering`, `member_of:r-engineering` don't resolve — no resolver code walks the edges. | Look at `internal/fixtures/seeder_test.go:9-29` — `Cast.FindByPredicate` is tag-on-user only. | Role-based scenario families (demote / kick / "only owners can X"). |
| **RW-14** | Cast has 4 users; concurrency mishap requires ≥3 distinct fixtures per predicate. Narrow predicates (`owner_of:r-engineering`) currently match exactly 1 fixture (alice). | `tools/integration-suite-v2/catalogs/fixture-cast.yaml` + `seed/*.json`. | Grows incrementally with scenarios. |

#### Infrastructure verification (read-only checks)

| ID | What | Why |
|----|---|---|
| **RW-15** | **RESOLVED 2026-05-24 (no action needed).** Valkey is fully wired: container in `docker-local/compose.deps.yaml:149-156` (image `valkey:8-alpine`, healthcheck via `valkey-cli ping`); `VALKEY_ADDR=valkey:6379` exported to both services in `room-service/deploy/docker-compose.yml:16` and `room-worker/deploy/docker-compose.yml:16`; both `room-service/main.go:33` and `room-worker/main.go:40` declare `VALKEY_ADDR` as `env:"VALKEY_ADDR,required"` so the services refuse to start without it. |
| **RW-16** | Exercise `JetStreamSubjectReader` (P2-5) against a live NATS + JetStream stream. The runner wires it opt-in (`Config.NATSCredsFile`), but no integration test has run it yet. | Validates what P2-5 shipped before scenarios depend on it. |

#### Minimum to unblock the first room-worker scenario

**RW-1** alone is sufficient: with the `mongo.rooms` filter fixed, the
scenario asserts step 1 = `room-service` reply (status=accepted) and
step 2 = `room-worker` mongo.rooms (room appears with the expected
shape). `logs.room-worker` is not used because production doesn't log
on happy create (and §2.1 says don't change that). RW-15 (Valkey
verification) is a pre-flight check, not a code change.

Everything else in this list is scenario-driven — pick up the next
item when the next scenario asks for it.

### 9.6 Bugs surfaced by the first end-to-end runs (all RESOLVED)

Discovered when scenarios actually fired against the live stack —
not via the pre-flight five-surface check or unit tests. Captured
here so the pattern is visible: each iteration moved the failing
fingerprint one layer deeper through the stack.

| ID | Bug | Symptom | Fix |
|----|-----|---------|-----|
| **R-1** | **Payload substitution didn't recurse into slices.** `dispatcher.buildPayload` only handled top-level string values; `users: ["${other.account}"]` shipped to the wire as the literal `"${other.account}"`. Room-service couldn't find a user with that account → `errEmptyCreateRequest`. | First run, every positive scenario rejected at room-service. | Folded into commit `aa2181e` (placeholder value plumbing). The new `Substitute()` walks `map[string]any`, `[]any`, and scalars recursively. Unit test: `TestSubstitute_SliceRecurses` in `internal/runtime/substitute_test.go`. |
| **R-2** | **Resolver picked the same fixture for every same-predicate placeholder.** `requester` and `other` both with `{verified: true}` both got alice → `users: [alice]` stripped against requester → empty → errEmptyCreateRequest. Plus Go map iteration is non-deterministic so picks varied across runs. | Scenarios with two same-predicate placeholders silently broken. | Commit `2547230`. `internal/scenario/resolver.go` sorts placeholder names then tracks picked accounts in `used`; subsequent placeholders skip already-picked fixtures. Tests: `TestResolve_DistinctPicksAcrossPlaceholders`, `TestResolve_DeterministicOrder`, `TestResolve_ExhaustedMatches_Fails`. |
| **R-3** | **`matches_shape` did whole-map equality on nested values.** The top-level loop iterated `want` keys (subset semantics, correct) but `deepEq` for two maps fell through to `fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)` — full key-set equality. `{body_json: {status, roomType}}` vs `{body_json: {status, roomId, roomType}}` → not equal → missing-positive. Negatives passed by coincidence (error reply has 1 field, expected has 1 field, sizes match). | Run b8db: all positive scenarios got missing-positive at room-service.reply despite production producing the correct reply. | Commit `646cc52`. New `matchMaps()` in `internal/matchers/matches_shape.go` recurses with subset semantics at every level; nested-path error messages name the failing field. Regression test: `TestMatchesShape_NestedMapSubset`. |
| **R-4** | **`$auto` collision: two resolutions same calendar month → same name.** `autoValue()` used `shortHash(time.Now().String())` returning `s[:8]` of `"2026-05-24 …"` → always `"2026-05-"`. Both `$auto`s in the same run produced the same room name; second create rejected as duplicate. | Run b8db: only one room landed in Mongo despite two positive scenarios. | Commit `646cc52`. Replaced timestamp-based suffix with `atomic.Uint64` counter; format is `it-<runID>-room-auto-<n>`. Deterministic, collision-free within one process run. Regression test: `TestSubstitute_Auto_UniquePerCall`. |
| **R-5** | **Mongo events classified as anomaly because they lacked trace.** Verdict's three-way classifier assumes every event carries `traceparent`; Mongo documents have no header slot → `Event.Traceparent=""` → no profile lookup wired → anomaly bucket. Pipeline scenario's step-2 expected `mongo.rooms` read never matched. | Run a1f2: positive scenarios scored 0 at the Mongo reader level despite production correctly persisting the room. | Commit `5e16639`. Synthetic trace stamping per §3.12. Suite generates traceparent once, hands to observer + dispatcher; non-trace-propagating readers (Mongo, future Cassandra) stamp it on emitted events. |

**Lesson.** The five-surface check at authoring time catches **catalog-vs-code drift** (does the verb exist, does the reader exist, does the service profile declare this write). It does not catch **runtime semantics gaps** — payload-substitution recursion, matcher subset semantics, trace-stamping fidelity. Those only surface when the suite is fired against a live stack and the verdict report classifies the result. Future spec consideration: a coverage register for **runtime semantics** alongside the five-surface presence check.

### 9.7 Strict cascade coverage — enabling a reader can expose incomplete scenarios

After the 4/4 milestone (run `b8a9`), the service-credential work (commit `0b01454`) activated the `jetstream.rooms-canonical` reader (which had been disabled because `NATS_CREDS_FILE` was unset). The next run (`6221`) regressed two previously-green happy scenarios to fail with `unexpected-cascade at jetstream.rooms-canonical (owner=room-service)`.

This is the strict v2 model working as designed: per the v2 platform spec `2026-05-21-…:578-583`, every in-trace event from an in-scope service must be covered by an expected read, or it's `unexpected-cascade`. When `jetstream.rooms-canonical` wasn't being observed, the canonical publish was invisible and the scenario passed despite asserting only on `reply`. Turning the reader on made the publish visible — which the scenarios hadn't declared.

The fix per scenario: add a read for every entry in the in-scope service's `on_trigger.writes` for the matching trigger pattern. For the create scenarios, this means adding:

```yaml
- location: jetstream.rooms-canonical
  matcher: matches_shape
  expected:
    body_json:
      name: ${input.payload.name}
      requesterAccount: ${requester.account}
      requesterId: ${requester.id}
```

alongside the existing `reply` read in the room-service step. Identity-level via the placeholder substitution work (spec `2026-05-24-placeholder-value-plumbing-design.md`).

**Proposed slash-command guardrail — check #6.** Add a sixth pre-flight check to `/scenario-author` that walks each in-scope service's profile, lists every write under the trigger pattern matching `input.subject`, and warns if any of those writes aren't covered by the scenario's reads (or explicitly tolerated when the tolerate mechanism exists — Option Y from §3.12's "three real choices" table). Wording for the slash command + scenario-author design spec:

> **Check #6 — Cascade coverage.** For each step in `sequence[]`, look up the step's service profile. Filter triggers by `pattern` matching the resolved `input.subject` (step 1) or the upstream-step's canonical publish subject (step 2+). For each trigger's `on_trigger.writes`, verify the scenario declares a read at that location in this step. Unmatched writes → warning (not hard-stop) at authoring time with text like "service X declares write Y on trigger Z but no expected read covers it — scenario will fail with unexpected-cascade if Y fires."

Cost: ~40 LOC in the slash command's check logic plus a one-line clarification in the scenario-author design spec. Doable when the next scenario family is authored.

**Forward path — "tolerated cascades."** Some scenarios will legitimately want to ignore an upstream write (focus testing). Options Y and Z from §3.12 still apply. Pick one when an actual scenario family demands it; don't speculate the schema extension before there's a real driver.

### 9.8 ContainerLogsReader vs docker-compose container naming (RESOLVED)

`tools/integration-suite-v2/internal/readers/container_logs.go:30` invokes `docker logs -f <container-name>` where `<container-name>` is the literal string passed at construction time (`"room-service"`, `"room-worker"`). The per-service compose files at `<svc>/deploy/docker-compose.yml` do NOT set `container_name:` explicitly, so docker compose's default naming applies — typically `<project>-<service>-1` based on the parent compose's `name:` directive.

Concretely: `make up` runs `docker compose -f docker-local/compose.services.yaml up`. `compose.services.yaml` has `name: chat-local-services` at the top and `include:` for every per-service compose. The resulting container for room-worker is likely `chat-local-services-room-worker-1` (or some compose-include-resolution variant) — NOT the literal `"room-worker"` our `ContainerLogsReader` looks for.

If true, `docker logs -f room-worker` fails silently (subprocess starts, exits with "No such container" on stderr which we don't capture, scanner sees nothing). No events emitted. Any scenario asserting on `logs.<service>` → missing-positive.

Evidence point: no prior scenario asserted on `logs.*` — the pure-negative room-worker scenario (`room-worker-rejects-missing-valkey-key.yaml`) was the first. It failed in run `6221` with missing-positive at `logs.room-worker`. Consistent with this hypothesis. NOT conclusive — could also be slog text mismatch, ordering, or other.

**Verify (read-only check during/after a run):**

```
docker ps --format '{{.Names}}' | grep room-worker
```

If output is something other than `room-worker`, this is the cause.

**Fix options if confirmed:**

| Option | Where | Cost |
|---|---|---|
| **A — Set `container_name:` in each per-service compose** | `<svc>/deploy/docker-compose.yml` | One line per service. Borderline §2.1 — these are deploy configs, not service code; production deploys with k8s, not docker-compose, so `container_name` is irrelevant there. |
| **B — Suite resolves container by compose label** | `internal/readers/container_logs.go` — run `docker ps --filter "label=com.docker.compose.service=<service>" -q` to get the actual ID, then `docker logs -f <id>` | ~15 LOC. Stays inside the suite. Robust to any compose project naming. |
| **C — Make container name configurable per reader** | Reader gets a `Container string` field set at registration time; runner sets to whatever the deployment uses | ~5 LOC, but requires every deployment story to know the suite's naming convention — fragile |

My lean: **B** — it's the smallest blast radius that lives inside the suite. The discovery query is one extra subprocess at Watch startup; the rest of the reader stays identical.

**Resolved 2026-05-24.** User confirmed `docker ps --format '{{.Names}}'` prints `chat-local-services-room-worker-1` (not literal `room-worker`), confirming the hypothesis. Option B landed: `tools/integration-suite-v2/internal/readers/container_logs.go` gains `resolveContainerRef` that queries `docker ps --filter label=com.docker.compose.service=<svc> --format {{.ID}}` and uses the first match; falls back to the literal `r.Container` name on empty/error so non-compose deployments still work. ~25 LOC.
