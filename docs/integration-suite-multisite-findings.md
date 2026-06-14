# Integration suite multi-site — findings

Findings from the multi-site integration suite, addressed to the
**chat-app project team**. Each finding is a report — what we
observed, where it lives in the system, what the chat-app team
needs to decide. Not a TODO for the suite team and not a changelog
of how the test tool reached its current shape.

Per-run reports under `docs/integration-suite-multisite/last-run.md`
are overwritten every run. Findings here are durable.

Format per finding:

```
F-NNN  <one-line title>
       Layer:  <chat-app code | chat-app local-dev tooling | ops/IaC>
       Status: <observed — chat-app team action pending>
```

---

## F-001 — `OUTBOX_<site>` has no production owner

**Layer:** ops/IaC (or chat-app code, if the team decides a service
should own it).

**Status:** observed — chat-app team action pending.

No chat-app service bootstraps `OUTBOX_<site>` in production. When
a producer (e.g. `room-worker`) tries to publish a cross-site
metadata event to `outbox.<site>.>` without the stream present,
NATS returns `no response from stream`. The chat-app code is
correct — the bootstrap responsibility is genuinely outside the
service's scope per `CLAUDE.md` §"Stream bootstrap ownership"
("streams are owned by ops/IaC").

The decision the chat-app team owns: **who creates
`OUTBOX_<site>` in production?**

- IaC at deploy time (matches the pattern used for other shared
  streams)
- An ops-owned bootstrap container, run once per cluster
- A designated chat-app service whose responsibility is OUTBOX
  schema ownership

Until designated, every cross-site producer fails on first publish
in any fresh environment. The integration suite works around this
via a per-scenario `pre_fire_scripts` hook that stands up OUTBOX
before the fire — operator-owned and explicit, not invented inside
the harness.

---

## F-002 — production federation topology shape

**Layer:** ops/IaC.

**Status:** observed — chat-app team action pending.

Cross-site JetStream federation requires two pieces that no chat-app
service ships and no current IaC reference declares:

1. **Transport that carries `$JS.<peer>.API.*` across sites.**
   NATS supercluster gateways do not. NATS leafnodes do. The
   integration suite uses leafnodes
   (`tools/integration-suite-multisite/internal/infra/nats.gateway.*.conf`)
   as a working reference.

2. **`SubjectTransform` on each federation `Source`** that rewrites
   `outbox.<remote>.to.<site>.>` → `chat.inbox.<site>.aggregate.>`
   on the way into the destination `INBOX_<site>`. Without the
   transform, federated messages arrive under the `outbox.*`
   namespace, get rejected by `INBOX_<site>`'s declared subjects,
   and even if they landed they'd be invisible to `inbox-worker`'s
   consumer (which binds to `chat.inbox.<site>.aggregate.>`).
   The chat-app's own `pkg/stream/stream.go` `Inbox()` docstring
   lines 64-69 already document the transform shape; the
   integration suite's `internal/infra/federation.go` `Apply` is
   an executable reference for what production `Sources` need to
   look like.

Decisions the chat-app team owns:

- Does production federate over leafnodes (or an equivalent that
  carries `$JS.<peer>.API.*` cross-cluster)?
- Is the SubjectTransform shipped at the federation IaC layer, or
  somewhere else?
- Once decided, mirror the shape in `docker-local/setup.sh` (or a
  sibling) so multi-site federation features can be verified
  locally without standing up the integration-suite-multisite
  stack.

---

## F-003 — `message-worker/README.md` describes a stream layout that no longer exists

**Layer:** chat-app code (doc only).

**Status:** observed — chat-app team action pending.

`message-worker/README.md` describes the service as consuming the
`MESSAGES` stream and publishing to a `FANOUT` stream. The actual
service (verified against `message-worker/main.go` +
`store_cassandra.go`) consumes from `MESSAGES_CANONICAL_<site>` —
the canonical stream split that landed when `message-gatekeeper`
was introduced as the validation gate ahead of message-worker — and
writes to Cassandra (`messages_by_id` + `messages_by_room` via
UnloggedBatch). No publishes to any `FANOUT` stream; that name
isn't declared anywhere in `pkg/stream/stream.go`.

The doc drift made authoring the
`message-pipeline-send-and-persist` scenario harder — an author
reading the README first would build the wrong subject/stream
graph in their head and either fire on a non-existent stream or
look for non-existent canonical events.

The chat-app team owns the doc. The fix: update
`message-worker/README.md` to describe the real consume
(`MESSAGES_CANONICAL_<site>` → `chat.msg.canonical.<site>.created`)
and write (`messages_by_id` + `messages_by_room`) shape, matching
what `message-gatekeeper/handler.go:167-330` (publishes the
canonical) and `message-worker/handler.go` (consumes + persists)
actually do.

---

## F-004 — gatekeeper accepts thread replies to non-existent parents (orphaned threads)

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

`message-gatekeeper`'s `processMessage` validates a thread reply's
`threadParentMessageId` for FORMAT only (`idgen.IsValidMessageID`,
handler.go:191-193) and that `threadParentMessageCreatedAt` is paired
with it (handler.go:209-211). It never verifies the parent message
actually exists before publishing the canonical event. A client can
send a thread reply whose `threadParentMessageId` is any syntactically
valid 20-char base62 string pointing at nothing.

`message-worker` then builds an **orphaned thread**: `CreateThreadRoom`
succeeds, the reply is persisted, but `handleFirstThreadReply`'s
`GetMessageSender(parent)` returns `errMessageNotFound` and
early-returns (handler.go:155-162) — skipping BOTH the parent-author
and the replier `thread_subscriptions`. The reply is Ack'd; nothing
errors.

Demonstrated by
`scenarios/drafts/thread-reply-to-nonexistent-parent-creates-orphan.yaml`
(green): canonical published, orphan `thread_rooms` doc created, reply
persisted with `thread_parent_id` set, ZERO `thread_subscriptions`.

Decision the team owns: should a thread reply whose parent does not
exist be rejected (gatekeeper verifies existence, or worker refuses to
create a room for a missing parent), or is silently building the orphan
acceptable? Consequence today: a client can manufacture unbounded
orphaned `thread_rooms` (pollution/abuse), replies unreachable via a
non-existent parent, and the orphan thread has no subscribers.

---

## F-005 — malformed (present-but-non-UUID) requestId is silently dropped

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

When a `msg.send` payload carries a `requestId` that is present but not
a valid hyphenated UUID, `processMessage` rejects it with
`errcode.BadRequest` (handler.go:178-180) — but `sendReply` then
**no-ops**, because its guard requires `req.RequestID` to pass
`idgen.IsValidUUID` (handler.go:143-145), the very predicate that just
failed. The reply subject `chat.user.{account}.response.{requestId}`
would be unroutable, so nothing is published. The client receives
NOTHING — no success, no error — and the send is dropped.

Contrast: an EMPTY-content rejection with a *valid* requestId IS
delivered (`gatekeeper-empty-content-rejected.yaml`). The differentiator
is solely requestId routability, not the rejection class.

Demonstrated by
`scenarios/drafts/gatekeeper-malformed-requestid-silent-drop.yaml`
(green): no reply reaches the client, gatekeeper logged the bad_request
rejection, no canonical event published.

Decision the team owns: is a silent drop on a malformed requestId
acceptable (client must time out), or should the client be told?

---

## F-006 — a missing quoted-parent drops the entire message, not just the quote

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

When a `msg.send` quotes a parent (`quotedParentMessageId`) that does
not exist, `resolveQuoteSnapshot` propagates history-service's typed
`NotFound` verbatim (handler.go:312-326), and `processMessage` returns
it before publishing — the WHOLE message is dropped (client gets a
`not_found` reply, no canonical event / Cassandra row).

This **contradicts the `ParentMessageFetcher` interface doc**
(store.go:33-34): "the handler soft-fails on every error and ships the
message without the quote." The implementation hard-fails the entire
send on NotFound. Code and stated contract diverge — one side is wrong.

Demonstrated by
`scenarios/drafts/gatekeeper-quote-nonexistent-parent-drops-message.yaml`
(green): reply `not_found`, no canonical, no `messages_by_id` row.
(Positive counterpart `gatekeeper-quote-happy-path-embeds-snapshot.yaml`
confirms the success path embeds + persists the snapshot.)

**Severity escalation — it also drops LEGITIMATE quotes (timing race).**
"Missing" includes "exists but not yet persisted." `message-worker`
persists to Cassandra asynchronously, behind the gatekeeper publish, so a
message quoted shortly after it was sent is not yet readable by
history-service's `GetMessageByID` → the quote fetch NotFounds → the
quoting message is dropped, even though the parent is a real, valid
message. Demonstrated by
`scenarios/drafts/quote/gatekeeper-quote-just-sent-message.yaml`
(multi-input, run 144c): task1 sends M, task2 immediately quotes M; only
M's canonical appears — the quoting message produced no canonical at all
(dropped). Unlike F-011 this race is consistently ordered (quote-fetch
always precedes the worker's write), so it reproduces reliably. So F-006
is not just bad-input handling — a normal "reply-with-quote" issued
quickly after the original silently loses the user's message.

Decision the team owns: should a bad quote target drop the whole message
(current behavior) or soft-fail as the doc describes (ship without the
quote)? This is a code-vs-contract mismatch, and the timing facet makes
it a real message-loss path, not just a malformed-input edge.

---

## F-007 — whitespace-only message content is accepted (no trim)

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

`message-gatekeeper`'s non-empty content gate is an exact empty-string
check — `if req.Content == ""` (handler.go:196-198) — with no trimming.
A whitespace-only body ("   ", "\n", a tab) passes validation, is
published, and persisted verbatim. The only other content gate is the
20KB size cap. A user can post blank-looking messages at will.

Demonstrated by
`scenarios/drafts/gatekeeper-validation/gatekeeper-whitespace-only-content-accepted.yaml`
(green): a "   " send produces a canonical event and a persisted row
with msg="   ".

**Corroborated by the edit path — the asymmetry is the smoking gun.**
`msg.edit` (history-service) rejects the SAME whitespace-only content:
`if strings.TrimSpace(req.NewMsg) == ""` → `bad_request`
(messages.go:306-308). So the chat-app's own edit path treats
whitespace-only as empty, while send (gatekeeper) does not. The same
logical operation — set a message's content to "   " — is accepted by
send but rejected by edit. This proves the two write paths disagree on
the empty-content rule, so the no-trim send check is an inconsistency,
not an intentional allowance. Demonstrated by
`scenarios/drafts/lifecycle/message-edit-to-whitespace-rejected.yaml`
(green, run f11e): editing a seeded message to "   " replies
`code: bad_request`.

Decision the team owns: align the two paths. Either trim before the
non-empty check in the gatekeeper (reject whitespace-only on send too —
matches edit), or relax edit to allow it. Today they contradict.
---

## F-008 — `publishThreadSubOutboxIfRemote` has three observationally-indistinguishable exit paths

**Layer:** chat-app code (observability).

**Status:** observed — chat-app team action pending.

In `message-worker/handler.go`, `publishThreadSubOutboxIfRemote`
has three exit paths that all look identical from an operator's
log:

- `ownerSiteID == ""` → `slog.WarnContext("owner siteID empty, skipping outbox publish")`, return nil
- `ownerSiteID == h.siteID` → silent return nil (same-site skip)
- successful cross-site publish → silent return nil

When a cross-site federation scenario fails to deliver an event
to `OUTBOX_<site>`, the operator has no log trace to distinguish
"the publish silently succeeded but didn't land on the stream"
from "the publish was correctly skipped because the remote-user
data looked local."

Surfaced during authoring of
`thread-first-reply-remote-parent-federates-subscription` (Run
sequence ending in the 18/19 cycle). Surfaces 2 and 3 of the
scenario prove the handler reached `InsertThreadSubscription` for
both the parent author and the replier (Mongo rows present).
Surface 4 (`jetstream_consume` on `OUTBOX_site-a` filtered by
`outbox.site-a.to.site-b.thread_subscription_upserted`) times out
with zero events. The full message-worker log across the entire
run contains zero log lines mentioning the test's message IDs at
all — no "owner user not found" warn, no "owner siteID empty"
warn, no publish-error error. The publish, if it happened, left no
trace.

**Recommended fix (one line):**
Add a `slog.InfoContext` log immediately before the `h.publish(...)`
call in `publishThreadSubOutboxIfRemote`:

```go
slog.InfoContext(ctx, "publishing thread subscription outbox",
    "ownerSiteID", ownerSiteID,
    "threadRoomID", sub.ThreadRoomID,
    "user_id", sub.UserID,
    "msgID", msgID,
    "subject", subj,
    "request_id", natsutil.RequestIDFromContext(ctx))
```

After this lands, re-run the failing scenario:
- Log fires with `ownerSiteID="site-b"` and the cross-site subject
  → publish was attempted; the gap is downstream (subject not
  captured by the stream, JetStream dedup window swallowing it,
  etc.). Cheap to localize from there with a stream inspect at the
  right moment.
- Log doesn't fire → the handler isn't actually reaching the publish
  branch for this scenario despite Surfaces 2+3 proving it ran past
  the upsert. Most likely cause to look at: the subsequent-reply
  branch being taken instead of first-reply due to state leakage
  from a prior scenario in the same run (see F-009).

**Adjacent (broader audit):**

- Same observability gap applies to the replier publish a few lines
  below in `handleFirstThreadReply` —
  `publishThreadSubOutboxIfRemote(ctx, replierSub, replier.SiteID,
  msg.ID)`. One log line covers both call sites.
- The same silent-success pattern likely exists in other
  `publish*OutboxIfRemote` helpers across `room-worker` and
  `room-service`. Worth a sweep with the same instrumentation
  discipline. Closes the parallel of `plan-ahead §2.9` at the
  production-code layer.

---

## F-009 — Service in-process caches violate per-scenario isolation

**Layer:** chat-app code (cache lifecycle / test-environment configurability).

**Status:** observed — chat-app team action pending. High severity (soundness).

The integration suite's `Sandbox.Setup` drops Mongo collections and
truncates Cassandra tables between scenarios, guaranteeing
byte-identical store state at scenario start. But the service
containers (gatekeeper, room-service, others) stay up for the whole
run and keep their **in-process caches** — sub-cache keyed
`(roomID, account)`, room-meta-cache keyed `roomID`, user-cache,
each with ~2-minute TTLs.

When scenario N populates a cache key, scenario N+1 — even with
clean Mongo state — can see the stale cached projection if it
references the same key within the TTL window.

**Concrete failure** (Run 649f → 1982 in the latest cycle):
- `gatekeeper-large-room-member-blocked` ran first, caching
  `(alice@r-busy, roles=[member])`.
- `gatekeeper-large-room-owner-bypass` ran second, expected
  `(alice@r-busy, roles=[owner])`. The cached `[member]` projection
  won → `canBypassLargeRoomCap` saw no owner role → capped → wrong
  verdict.
- Run 1982 fixed it by giving the second scenario a unique room ID.
  Only difference. Same code, same env.

**Why this is the worst class of bug:** silent, order-dependent
false verdicts — not a loud setup error. A scenario reordering
could falsely-green a negative scenario without anyone noticing.
The suite's "byte-identical state per scenario" guarantee turns
out to be DB-level only.

**Cache inventory (scope for whichever fix you choose).** A suite-side
audit found **13 in-process caches across 5 services**. Both
mitigations below need to touch each one, so the inventory is the
work surface either way:

| Service | Caches | Implementation |
|---|---|---|
| `message-gatekeeper` | sub-cache `(roomID, account)`, room-meta-cache `roomID`, user-cache | all `hashicorp/golang-lru` (expirable) |
| `broadcast-worker` | room-meta-cache, user-cache, room-cipher cache | 2× golang-lru; `pkg/roomcrypto.Encoder` is a raw `map + sync.RWMutex` |
| `message-worker` | user-cache, DEK cache | golang-lru; `pkg/atrest` 2Q LRU |
| `history-service` | emoji, subscription, room-times, min-last-seen | 4× golang-lru (`pkg/emoji`, `internal/readcache`) |
| `notification-worker` | room-meta-cache | golang-lru (`pkg/roommetacache`) |

**12 of the 13 are `hashicorp/golang-lru`** — each already has a
built-in `.Purge()`, so a flush hook is a one-liner per cache. The
lone outlier is `pkg/roomcrypto.Encoder` (raw map), which needs a
~10-line hand-written `Purge()`. Most caches are owned by shared
`pkg/` packages (`userstore`, `roommetacache`, `atrest`, `emoji`,
`roomcrypto`), so a flush is implemented once per package and reused.
The Valkey-backed caches (`pkg/roomsubcache`, search restricted-rooms)
are out of scope — the sandbox already resets Valkey.

**Mitigation options for the chat-app team:**

1. **Env-driven cache TTL override.** Services accept e.g.
   `*_CACHE_TTL` env vars; the test stack sets them to `0`
   (disabling the cache in the test environment). Smallest
   architectural shape; preserves production caching behavior
   unchanged.
2. **Admin cache-flush endpoint.** Each cache-holding service
   exposes a NATS or HTTP admin verb to invalidate its caches.
   The test sandbox calls it between scenarios. More plumbing;
   useful operationally too (cache flush on demand without restart).

The test tool can mitigate at the author-discipline layer (use
unique `(account, roomID)` per scenario — see plan-ahead §2.10) but
the discipline is a footgun, not a fix. The structural fix is
in chat-app code.

---

## F-010 — `upload-service` Dockerfile pins `golang:1.25.10` — toolchain mismatch breaks the image build

**Layer:** chat-app local-dev tooling (Dockerfile).

**Status:** observed — chat-app team action pending. Low severity (one-line fix), but it blocks fresh-image suite runs.

`upload-service/deploy/Dockerfile` uses `FROM golang:1.25.10-alpine`.
It is the **only** service that does — the other 13 service
Dockerfiles all use `golang:1.25.11-alpine`, matching the root
`go.mod` directive `go 1.25.11` and the CLAUDE.md Docker rule
(builder must be `golang:1.25.11-alpine`).

With the default `GOTOOLCHAIN=auto`, a `1.25.10` toolchain building
a module that declares `go 1.25.11` attempts to **download** the
`1.25.11` toolchain. In the sealed Docker builder that download
fails, so `RUN go mod download` exits non-zero — the observed
"`go mod download` exit 1".

**Blast radius (why the suite team is reporting it):** `make
build-test-images` (root Makefile) runs `docker compose build` over
`docker-local/compose.services.yaml`, which `include:`s all 13
services. `docker compose build` is all-or-nothing across its set,
so this one Dockerfile aborts the whole target before the 9 images
the suite actually needs are (re)built. The suite then runs against
**stale pre-merge images** — a green run validates suite plumbing,
not current chat-app behavior.

**Fix:** one line — bump `upload-service/deploy/Dockerfile` to
`FROM golang:1.25.11-alpine`, matching every other service and the
root `go.mod`. (Pinning is correct; only the version is stale.)

**Suite-side note (no chat-app dependency):** the suite team can
add a `tools/integration-suite-multisite/` make target that builds
only the 9 `defaultServices` images, bypassing `upload-service` /
`user-presence-service` entirely — those two are not in the suite's
service set. That unblocks fresh-image runs independently of this
fix; it does not replace it (the Dockerfile is wrong regardless).

---

## F-011 — `tcount` lost update under concurrent thread replies (non-atomic count-and-set)

**Layer:** chat-app code (message-worker concurrency).

**Status:** observed — chat-app team action pending.

PR #245 (`ab1cafdc`) replaced the parent reply-count (`tcount`) update
in `message-worker/store_cassandra.go` with a **non-atomic
count-and-set**: `SaveThreadMessage` → `countAndSetParentTcount`
(store_cassandra.go:327-344) does `countThreadReplies()` (SELECT/COUNT
the thread partition, :287-303) then `setParentTcount(n)` (blind
`UPDATE ... SET tcount = ?`, **no `IF`/CAS**, :305-325). The prior
implementation (`incrementParentTcount`) used a Cassandra LWT
(`IF tcount = ?`) retry loop and was lost-update-safe.

`message-worker` consumes MESSAGES_CANONICAL with a concurrent
worker pool (MAX_WORKERS goroutines), so two replies to the same
thread are processed in parallel. The count-and-set then has a
classic lost-update window:

```
reply1: INSERT its thread row            (1 row)
reply1: countThreadReplies → 1           (reply2's row not yet visible)
reply2: INSERT its thread row            (2 rows)
reply2: countThreadReplies → 2
reply2: setParentTcount(2)               tcount = 2
reply1: setParentTcount(1)  ← runs LAST  tcount = 1   (overwrites)
```

Result: `tcount` is left at 1 though two replies exist. It is NOT
self-correcting until the *next* reply (or a redelivery) triggers a
recount — a thread that goes quiet keeps a permanently-wrong
reply-count badge. The code comment ("idempotent on redelivery,
avoiding the 2PC window of the old CAS increment") is accurate for
*sequential* redelivery but does not address *concurrent distinct
replies*.

Reproduced by
`tools/integration-suite-multisite/scenarios/drafts/message-worker-subsequent-thread-reply-multi-input.yaml`
(two msg.send fires to one pre-seeded parent). **Confirmed lost update,
run 8088:** the scenario asserts both reply rows persisted
(`m1subseqreply…` + `m2subseqreply…` in `messages_by_id`) *before* the
tcount check; the run PASSED both row assertions, then failed `tcount: 2`
with `got 1`, stable for the full 10s poll. So two reply rows exist
(true count = 2) yet the parent's stored `tcount` is 1 — a lost update,
not a dropped reply. (First seen run 2465; the race is timing-dependent,
back-to-back firing maximizes it.)

The decision the chat-app team owns: is a concurrency-induced
`tcount` undercount acceptable (it self-heals on the next reply, and
the badge is best-effort), or should the count be made
concurrency-safe again — e.g. restore the LWT/CAS increment, derive
the badge from a COUNT at read time instead of a stored column, or
serialize per-thread tcount writes? This is a regression in
concurrency-safety from the pre-#245 CAS path.

## F-012 — `room.create` replies "accepted" before the room is usable (creator's first send races the async subscription write)

**Layer:** chat-app code (room-service async-job contract ↔ gatekeeper gate).

**Status:** observed — chat-app team action pending.

`room.create` is an async job: room-service publishes ROOMS_CANONICAL.create
and replies `{status: accepted, roomId}` (room-service/handler.go:287-321),
while `room-worker` writes the creator's subscription afterward
(BulkCreateSubscriptions). So the `accepted` reply — which carries the
roomId, implying the room exists — precedes the room being *usable*. A
client that treats `accepted` as ready and immediately sends to the
returned roomId hits the gatekeeper's subscription gate
(message-gatekeeper/handler.go:213-222) before the creator's subscription
exists → `forbidden`/`not_subscribed` → the first message is dropped.

Demonstrated by
`scenarios/drafts/gatekeeper-validation/gatekeeper-create-room-then-send-races-subscription.yaml`
(multi-input, run 22bb, green): task1 creates a channel (reply accepted +
roomId), task2 sends to `${create.reply.body_json.roomId}` immediately →
the send's reply is `not_subscribed` and no canonical event is produced.
The race is consistently ordered (the gatekeeper check precedes
room-worker's subscription write), so it reproduces reliably under the
back-to-back fire; for a human typing it is timing-dependent (the write
lands in ~100ms), but a fast client/bot or an auto-first-message flow can
lose the message.

The decision the chat-app team owns: should `create`'s `accepted` reply
guarantee usability (e.g. sync-write the creator's own subscription before
replying, or have the gatekeeper tolerate a just-created room the sender
owns), or is the client expected to wait for a readiness signal
(subscription.update) before sending? Today "accepted" over-promises.

## F-013 — editing a just-sent message fails `not_found` (edit RPC races the async Cassandra persist)

**Layer:** chat-app code (history-service edit RPC ↔ message-worker async persist).

**Status:** observed — chat-app team action pending.

A `msg.send` publishes through the gatekeeper to MESSAGES_CANONICAL
synchronously, but `message-worker` persists the row to Cassandra
**async**, behind the canonical publish. The `msg.edit` RPC
(`chat.user.{account}.request.room.{roomID}.{siteID}.msg.edit`) resolves
the target via `EditMessage → findMessage`
(history-service/internal/service/messages.go:283-320), which returns a
typed `NotFound` when the row isn't present yet. So a client that sends a
message and immediately edits it gets `not_found` for a message it just
successfully sent (and whose canonical event *did* publish).

Demonstrated by
`scenarios/drafts/lifecycle/message-edit-just-sent-races-persistence.yaml`
(multi-input, run 1a61, green): task1 sends M, task2 immediately edits M
→ the edit reply is `code: not_found`, **and** M's canonical event is
present on MESSAGES_CANONICAL (the send itself succeeded). The
edit-fetch is consistently ordered before the worker's write, so it
reproduces reliably under back-to-back fire.

**This is the third confirmed facet of one root cause — the
worker-persistence-lag blindspot.** An operation on a just-async-written
entity races the worker write and fails, across three independent code
paths:
- **F-006 (fresh-quote facet)** — quote a just-sent message → the
  quoting message is *dropped* (gatekeeper quote-resolution NotFound).
- **F-012** — send to a just-created room → the send is *dropped*
  (`not_subscribed`; room-worker hasn't written the subscription).
- **F-013 (this)** — edit a just-sent message → `not_found` (history
  edit RPC; worker hasn't persisted the row).

The shared decision the chat-app team owns: which write-then-read APIs
should guarantee read-your-write on the success reply (sync the critical
row before replying, or have readers tolerate the in-flight entity), vs.
which legitimately require the client to await a readiness signal before
the follow-up operation. Today the `accepted`/canonical-published replies
imply a usability they don't yet provide.

## F-014 — client-supplied message-id dedup is sender-agnostic (a colliding second send is acked-but-discarded)

**Layer:** chat-app code (gatekeeper canonical-publish dedup key ↔ worker blind-upsert persist).

**Status:** observed — low severity / likely intentional idempotency with a
sharp edge; chat-app team ruling requested.

The gatekeeper publishes the canonical message with
`jetstream.WithMsgID(natsutil.CanonicalDedupID(&evt))`
(message-gatekeeper/handler.go:301), and for a `created` event
`CanonicalDedupID` is **`evt.Message.ID` alone** — no sender, content, or
requestId (pkg/natsutil/canonical_dedup.go, default case). The
MESSAGES_CANONICAL stream therefore suppresses any second publish carrying
a message id it has seen within the dedup window (JetStream default ~2m),
**regardless of who sent it or what it contains.** The worker's persist is
a blind `INSERT` (Cassandra upsert, no `IF NOT EXISTS`,
message-worker/store_cassandra.go SaveMessage).

This is correct, desirable idempotency for one client retrying its **own**
send. The sharp edge: message ids are **client-supplied**, so:
- **Success ack ≠ persisted.** A second sender (or a buggy client) that
  reuses an existing id gets a successful *duplicate* PubAck — the
  gatekeeper sees no error and acks the send — but nothing new is
  persisted. The distinct message is silently lost.
- **Cross-sender, sender-agnostic.** The dedup ignores the sender, so the
  collision is resolved purely on id. Demonstrated by
  `scenarios/drafts/gatekeeper-validation/gatekeeper-duplicate-message-id-cross-sender-deduped.yaml`
  (run d195, green): alice sends id=COLLIDE first (wins); bob sends the
  same id with different content back-to-back → bob's content never
  reaches the canonical stream, and the persisted `messages_by_id` row is
  alice's. bob's send was nonetheless accepted.
- **Theoretical outside-window overwrite.** Past the dedup window (~2m),
  a second send with a colliding id is NOT suppressed → the worker's blind
  upsert overwrites `messages_by_id[id]` with the second sender's
  content/sender. `GetMessageByID(id)` (quote-resolution, edit, delete
  lookups) would then resolve to the second writer's version. Not
  reproduced here (the window exceeds a practical test), noted as the
  integrity ceiling of the current design.

For honest clients with random 20-char base62 ids, collision is
negligible — practical impact is near-zero. The ruling the team owns:
is global (sender-agnostic) id dedup acceptable, or should the dedup key /
persist be namespaced by sender (and/or the worker guard the first write
with `IF NOT EXISTS`) so a colliding id can't shadow or overwrite another
sender's message?

## F-015 — soft-deleted message content is recoverable (delete leaves plaintext `msg`; reads/quotes ignore `deleted`)

**Layer:** chat-app code (history delete CQL + GetMessageByID + gatekeeper quote-resolver).

**Status:** observed — privacy/correctness; chat-app team action requested.
**Reachability:** any room member, **no special timing** (deterministic logic
bug — not a fast-path/bot edge). Message ids are broadcast on the canonical
event and rendered client-side, so a member always knows the id to read or
quote.

Soft-delete does not actually remove the message content, and two read
paths return it. **Two independent defects compose:**

1. **Soft-delete retains the plaintext `msg` column.** The delete CQL is
   `UPDATE messages_by_id SET deleted = true, enc_payload = null,
   enc_meta = null, updated_at = ? … IF deleted != true`
   (history-service/internal/cassrepo/write.go:38, and the parallel
   `messages_by_room` / `thread_messages_by_thread` / pinned variants). It
   nulls only the **encrypted** columns; the plaintext `msg` column is left
   intact. In any **unencrypted** deployment (the suite stack runs no
   roomcrypto/cipher container, so this is the live path) the deleted
   content persists verbatim in Cassandra.

2. **Reads ignore `deleted`.** `GetMessageByID`
   (history-service/internal/service/messages.go:258-279) has no `deleted`
   filter — `findMessage` (utils.go:76-91) returns the row regardless of
   `deleted`, and the handler only redacts the message's *own*
   quoted-parent for the access window, never the message itself. So
   `msg.get` on a deleted id returns the retained content directly. The
   gatekeeper's quote-resolver compounds it: `FetchQuotedParent`
   (message-gatekeeper/fetcher_history.go:71-81) projects `parent.Msg` into
   the quote snapshot with no `deleted` check, so quoting a deleted message
   **embeds its content into a new message** that is persisted and
   broadcast to the room. Defect 2 is config-independent: even with
   encryption, a deleted message should not be silently quotable.

Demonstrated by
`scenarios/drafts/delete/gatekeeper-quote-soft-deleted-message-resurrects-content.yaml`
(multi-input, run 3c00, green): alice sends+owns M and deletes it
(succeeds); the row is then `deleted: true` **with `msg` still equal to the
original content**; bob (a different member) quotes M and the canonical
event for bob's message carries `quotedParentMessage.msg` = alice's deleted
content. The delete (synchronous RPC) completes before the quote fires, so
the resurrection is deterministic, not a race.

The decisions the team owns: (1) soft-delete should null `msg` (and any
other plaintext content columns) alongside the encrypted ones, or
hard-delete the content; (2) `GetMessageByID` / the quote-resolver should
treat `deleted` rows as not-found or surface a redacted "message
unavailable" snapshot (the latter already exists for out-of-window quotes,
`UnavailableQuoteMsg`), rather than returning live content.

## F-016 — mentioning a non-member in a thread reply auto-subscribes them to the thread

**Layer:** chat-app code (worker mention resolution + thread-mention handling).

**Status:** observed — access-control; chat-app team action requested.
**Reachability:** any room member, **no special timing** — `@mention` of any
valid account in a thread reply.

`mention.Resolve` (message-worker/handler.go:63-67) resolves `@account`
mentions via `FindUsersByAccounts` — a **global** user lookup with **no
room-membership scoping** (pkg/mention/mention.go `ResolveFromParsed`
resolves any account the lookup returns). For thread replies, every reply
runs `markThreadMentions` (handler.go:89-98, 359-392), which for **every**
resolved mentionee calls `MarkThreadSubscriptionMention` —
an upsert (`SetUpsert(true)` + `$setOnInsert`, store_mongo.go) that
**creates** a `thread_subscription` if absent — and adds them to
`thread_rooms.replyAccounts`. There is no check that the mentionee belongs
to the room.

Net: `@bob`, where bob is a real user who is **not a member** of the room,
gives bob a durable thread subscription and makes him a thread follower
(notification fan-out + the "following threads" feed) for a thread inside a
room he cannot otherwise access. This is broader than the already-known
main-room phantom-mention (the `message-worker-mentions-persisted` scenario
shows a non-member merely landing in the persisted `mentions` set, tagged
positive); here the non-member gains a **subscription** — an access /
notification relationship to room-scoped content.

Demonstrated by
`scenarios/drafts/mentions/message-worker-thread-mention-nonmember-auto-subscribes.yaml`
(run e683, green): alice (a member) replies in a thread mentioning `@bob`
(seeded as a profile only, no membership); afterward `thread_subscriptions`
holds a row for bob (parentMessageId = the thread parent, userAccount=bob)
and `thread_rooms.replyAccounts` contains bob.

The decision the team owns: mention resolution (and at minimum the thread
subscription/replyAccounts side-effects) should be scoped to room members —
filter `mention.Resolve` results by membership, or guard
`markThreadMentions` so a non-member mention is recorded as content but
does not create a subscription / follower relationship.

**Cross-site escalation (the blast radius is multi-site).** When the
mentioned non-member is a **remote** user, the subscription federates to
their home site. `markThreadMentions` calls `publishThreadSubOutboxIfRemote`
for every mentionee whose home site differs from the local site
(handler.go:381), emitting `outbox.{local}.to.{ownerSite}.thread_subscription_upserted`;
the federation Source delivers it to the remote site's INBOX, where
`inbox-worker` applies it via `handleThreadSubscriptionUpserted →
UpsertThreadSubscription` (inbox-worker/handler.go:96-97,274-279). So a
remote user who is a member of nothing — not on the acting site, not in the
room on their own site — ends up with a durable `thread_subscription` **on
their home site** purely by being `@`-mentioned from another site.

Demonstrated end-to-end by
`scenarios/drafts/federation/thread-mention-remote-nonmember-federates-subscription.yaml`
(runs 6c59 + 4c72, green twice): alice (site-a) thread-replies mentioning
`@remotebob` (a site-b user, member of nothing). The chain lands on every
surface — local thread_subscription on site-a, `OUTBOX_site-a →
thread_subscription_upserted (destSiteId site-b)`, `INBOX_site-b` aggregate
event, and finally a `thread_subscription` for remotebob applied on
**site-b**. The membership-scoping fix above must therefore apply before the
outbox publish, or the leak crosses the federation boundary.

## F-017 — a thread reply can attach to a parent in another room and pollute its tcount

**Layer:** chat-app code (gatekeeper thread-parent non-validation + worker tcount write).

**Status:** observed — integrity / missing validation; chat-app team action requested.
**Reachability:** any member of a room they control, who knows a target
message's `(id, createdAt)` — both are broadcast on canonical events and
present in history/search reads. No special timing.

The gatekeeper validates a thread reply's `threadParentMessageId` only for
**format** (`IsValidMessageID`) and **pairing**
(`threadParentMessageCreatedAt` must be present) — handler.go:191-210. Unlike
quotes (which fetch the parent via `resolveQuoteSnapshot`), it **never
fetches the parent**, so it cannot tell the parent lives in a different room
(or doesn't exist — the old, untracked F-004 orphan-thread root). The worker
then builds the thread room with `RoomID: msg.RoomID` — the **reply's** room
— and a client-supplied `ParentMessageID`, with no parent-room check
(handler.go:89-92, `handleThreadRoomAndSubscriptions`). The fatal detail is
in `setParentTcount` (store_cassandra.go): the `messages_by_id` tcount write
is `UPDATE … SET tcount = ? WHERE message_id = ? AND created_at = ?` —
**room-agnostic** — so it lands on the parent wherever it lives; the parallel
`messages_by_room` write uses `msg.RoomID` (the reply's room), upserting a
**phantom** parent row in the wrong room's partition.

Net: a member of room Y can send a thread reply naming a parent M that lives
in room X (a room they are not in), and M's `tcount` in X is bumped — a
reply count injected onto a foreign room's message by a non-member of that
room. The thread room created for M is also mislabeled with `RoomID = Y`, so
X's members see M's tcount increment but the thread itself is associated with
a room they cannot see.

Demonstrated by
`scenarios/drafts/threads/message-worker-cross-room-thread-reply-pollutes-foreign-parent.yaml`
(run 987c, green): alice (member of `r-mine` only) threads off `M` seeded in
`r-other`; afterward `messages_by_id[M]` shows `room_id: r-other, tcount: 1`,
and `thread_rooms[parentMessageId=M]` has `roomId: r-mine`.

The decision the team owns: the gatekeeper (or worker) should verify the
thread parent exists **and belongs to the reply's room** before creating the
thread room / writing tcount — reject the reply otherwise. This is the same
non-validation root the old F-004 flagged; the cross-room facet turns it from
a harmless orphan thread into cross-room integrity pollution.
