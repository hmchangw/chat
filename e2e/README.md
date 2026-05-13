# e2e suite

End-to-end tests for the chat backend. Drives the full compose stack
(two sites, NATS supercluster gateway, MongoDB + Cassandra + ES per site,
Keycloak per site, auth-service + 8 backend workers per site) and
asserts behaviour at the user-API boundary.

## Quick start

```bash
make e2e            # testcontainers-owned lifecycle (CI parity)
make e2e-up         # bring up compose stack in the background
make e2e-only       # run go test against an already-up stack (sets E2E_REUSE_STACK=1)
E2E_REUSE_STACK=1 go test -tags e2e ./e2e/...   # same, manual invocation
make e2e-down       # stop the stack
make e2e-logs       # tail container logs
```

Suite size: **45 PASS / 1 SKIP / 0 FAIL** in ~15s on a warm REUSE stack.

## Stack ownership: two modes

| Mode | Trigger | Lifecycle | Catchup test |
|------|---------|-----------|--------------|
| Owned | `make e2e` | testcontainers brings up + tears down per `go test` invocation | runs |
| Reused | `E2E_REUSE_STACK=1` | tests attach to an already-running compose stack (via `make e2e-up`) | skipped |

REUSE mode is for fast local iteration: one `make e2e-up` (~30s), then
`go test ./e2e/...` runs in 15s instead of paying the bringup tax
every time. The cost is one skipped test (catchup -- see below).

## Container-level surfaces

The compose file (`docker-local/e2e/compose.e2e.yaml`) provisions:

- **Per site**: NATS (+ JetStream), MongoDB, Cassandra, Elasticsearch,
  Valkey, Keycloak, auth-service, plus 10 backend workers:
  - message-gatekeeper, message-worker, history-service
  - room-service, room-worker
  - broadcast-worker, notification-worker, search-service, search-sync-worker
  - inbox-worker (cross-site federation)
- **siteA only**: `broadcast-worker-a-enc` -- second broadcast worker
  with `DURABLE_NAME=broadcast-worker-enc` + `ENCRYPTION_ENABLED=true`
  used by `TestEncryption_OnSmoke`. Independent durable means it doesn't
  split events with the plaintext worker.
- **Realm**: `chatapp` realm with `alice` and `bob` preprovisioned in
  both Keycloak instances (same realm export, federation simulated).

Host port allocations and per-service env are documented at the top of
`compose.e2e.yaml`.

## Test categories

| File | Coverage | Tests |
|------|----------|-------|
| `smoke_test.go` | Deps reachable (one ping per backend) | 1 |
| `message_send_test.go` | Single-site send + broadcast | 1 |
| `message_edit_delete_test.go` | Edit + delete | 2 |
| `rooms_test.go` | DM idempotency, invite/list/remove, mark-read | 4 |
| `threads_test.go` | Parent + replies | 1 |
| `negative_test.go` | Oversized payload, non-member send, dup-MessageID, bad creds, history-as-non-member | 5 |
| `errors_test.go` | Send to non-existent room, malformed payload, bad creds | 3 |
| `request_id_test.go` | X-Request-ID propagates through broadcast chain | 1 |
| `tracing_test.go` | W3C traceparent propagates through broadcast chain | 1 |
| `search_test.go` | Message visible after index | 1 |
| `subject_coverage_test.go` | MsgGet, MsgNext, MsgSurrounding, RoomsList, RoomsGet, OrgMembers, SearchRooms | 7 |
| `auth_http_test.go` | auth-service direct HTTP coverage (healthz + 5 /auth paths) | 6 |
| `cassandra_direct_test.go` | messages_by_room + messages_by_id schema invariants | 3 |
| `stress_burst_test.go` | 100-message burst (ack + broadcast + cassandra delivery) | 1 |
| `encryption_test.go` | broadcast-worker-a-enc encrypts → ciphertext round-trips with seeded room key | 1 |
| `federation_invite_test.go` | Cross-site invite + state-stays-site-local negative | 2 |
| `federation_events_test.go` | Cross-site role update + cross-site member remove | 2 |
| `federation_outbox_test.go` | subscription_read + thread_subscription_upserted | 2 |
| `zz_federation_catchup_test.go` | Worker-outage backlog catchup (testcontainers only) | 1 |

## Parallelism

Tests opt in via `t.Parallel()`. **Single-site, per-test-room** tests
are parallel-safe; **federation tests** are kept serial because they
share inbox-worker-b state and the cross-site gateway flow.

| Group | Parallel? | Reason |
|-------|-----------|--------|
| Single-site message / room / thread / negative / errors / subject / auth-HTTP / cassandra / stress / encryption / search | yes | Each test creates its own room and registers cleanup. Authenticate returns a fresh per-call NATS connection (different nkey each time) so multiple parallel tests can share the same realm user without colliding. Search additionally mints a fresh Keycloak user via `MintEphemeralUser` to isolate the per-account valkey cache + ES user-room doc. |
| `TestRoom_DM_CreateIsIdempotent` | no | Shares the alice-bob DM roomID; idempotency assertion would flake against other parallel tests touching the realm-fixed users. |
| `TestFederation_CatchUpAfterOutage` (catchup) | no | Stop/starts inbox-worker-b. Skipped entirely under REUSE. |
| Other 5 federation tests | no | Share inbox-worker-b consumer state; attempted parallel cut wall time but introduced 15s+ timeouts on shared OUTBOX→INBOX gateway sourcing. |

Per-test Keycloak user synthesis would unlock federation parallelism;
not implemented (needs Keycloak admin API + per-test cleanup).

## Production code changes shipping alongside the e2e harness

The harness surfaced four real production behaviours worth changing in
small, additive PRs:

1. **`broadcast-worker/main.go`** -- `DURABLE_NAME` (default
   `broadcast-worker`) + `DeliverNewPolicy` + `MAX_DELIVER` (default
   `-1` = unlimited, set to `2` on the encrypted variant). Required so
   a second worker variant can attach to MESSAGES_CANONICAL without
   splitting messages with the primary.
2. **`inbox-worker/bootstrap.go`** -- preserves existing
   `Sources + SubjectTransforms` on `CreateOrUpdateStream`. Previously
   every worker restart silently cleared federation config that ops/IaC
   (or the e2e harness's `BootstrapFederation`) had layered on.
3. **`pkg/otelutil/otel.go`** -- always registers the W3C TextMap
   propagator, and always builds a real `TracerProvider` (just without
   an OTLP exporter when `OTEL_SDK_DISABLED=true`). Previously the
   no-op global provider meant traceparent was silently dropped from
   every cross-service publish even when downstream collectors were
   wired up.
4. **`pkg/natsutil/request_id.go` (`NewMsg`)** -- always allocates a
   non-nil `nats.Header`. Otherwise otelnats's `HeaderCarrier.Set` is a
   silent no-op and traceparent injection drops.

None of these touch caller code; they're all transparent improvements.

## Harness helpers

`e2e/harness/clients.go` is the hub:

| Method | Purpose |
|--------|---------|
| `Authenticate(account)` | Keycloak password grant → /auth → fresh nkey + NATS conn. Per-call (no caching). |
| `MongoDB()` / `CassandraSession()` / `JetStream()` / `SystemConn()` | Direct DB / NATS handles. |
| `SeedRemoteUser(account, siteID)` | Simulates cross-site user discovery (writes the user row into the OTHER site's mongo). |
| `SeedUserRoom(account, roomIDs)` | Pre-populates ES user-room doc for `search-service` to authorise queries. |
| `FlushSearchRestrictedCache(account)` | Invalidates the per-account valkey restricted-rooms cache. |
| `SeedRoomKey(roomID, pair)` / `DeleteRoomKey(roomID)` | Writes a P-256 key pair into valkey via `roomkeystore.NewValkeyStore`. |

Test-file helpers in `helpers_test.go`:

- `requestReply(conn, subj, req, timeout, into)` -- classic NATS RR with X-Request-ID header.
- `sendAndAwaitReply(...)` -- for msg.send (JS-published + async-replied; can't be RR).
- `awaitDurableReady`, `awaitCanonicalAcked` -- consumer lifecycle waits.
- `awaitSubscription`, `awaitMessage` -- mongo + sub waits.
- `registerRoomCleanup` -- per-test cleanup hook that deletes a room's
  rows from all listed sites' mongos at test end.

## Adding a new test -- checklist

Work top-to-bottom. Each item has a "trap" callout where copy-paste from
a sibling test commonly omits it.

- [ ] **File has `//go:build e2e` tag on line 1.** Required on *every*
  file in this package. Without it the file gets compiled into the
  default `go test ./...` run, which has no compose stack and will
  panic during `TestMain`. Trap: copy-pasting a new file from
  `helpers_test.go` is safe (it has the tag); creating one from
  scratch in your editor often forgets it.

- [ ] **Pick the right file (or add a new one)** per the test categories
  table above. Adding a federation case? It goes in a
  `federation_*_test.go` file, not in `message_send_test.go`.

- [ ] **Default to single-site (`stack.SiteA`)** unless the assertion
  genuinely needs cross-site behaviour. Federation tests are slower
  and serial (see below).

- [ ] **Authenticate users via `site.Authenticate(t, ctx, "alice"|"bob")`.**
  Don't reuse an `Identity` across tests -- `Authenticate` returns a
  fresh per-call NATS connection (different nkey each time) so
  parallel tests can share the same realm user without colliding.

- [ ] **Create a per-test room with `subject.RoomCreate(...)`**, then
  immediately register cleanup with `registerRoomCleanup(t, sites,
  roomID)`. Cleanup hits Mongo + Cassandra + ES + Valkey best-effort;
  see "What `registerRoomCleanup` cleans" below for what it does and
  doesn't reach.

- [ ] **Trap: `CreateRoom` reply is "request accepted", NOT "data is
  durable".** After `subject.RoomCreate` returns, you MUST call
  `awaitSubscription(t, ctx, db, account, roomID)` for every account
  that will publish or query. `CreateRoomReply` fires before
  room-worker has persisted the subscription rows; sending to the
  room before this point trips message-gatekeeper's "user is not
  subscribed" check and the test will fail with a confusing
  authorisation error.

- [ ] **Use the right reply helper for the subject:**
  - `requestReply(conn, subj, req, timeout, into)` -- classic
    synchronous NATS request/reply. Use for room-service's
    create/list/get RPCs, history-service.LoadHistory, search-service,
    and every `chat.user.{account}.request.…` subject.
  - `sendAndAwaitReply(t, conn, account, requestID, sendSubj, payload,
    timeout)` -- use ONLY for the `msg.send` path
    (`chat.user.{account}.room.{roomID}.{siteID}.msg.send`).
    `msg.send` is JS-published with an async out-of-band reply on
    `subject.UserResponse`; classic RR will time out against it.

- [ ] **If your test queries Cassandra/ES/Mongo directly**, prefer the
  harness methods (`stack.SiteA.MongoDB()`,
  `stack.SiteA.CassandraSession()`, etc.) over building your own
  client. They share connection pools and pick up the compose stack's
  port allocations.

- [ ] **For async chains, never `time.Sleep` -- use polled waits.** Pick
  the right one:
  - `awaitDurableReady` -- consumer exists on the stream
  - `awaitMessageByID(t, ctx, sess, msgID)` -- Cassandra row landed
    for a SPECIFIC message_id. **Use this in any `t.Parallel()`
    test.** It hits the `messages_by_id` dedup view (PK is
    message_id) so each parallel sibling waits for its own row.
  - `awaitCanonicalAcked(t, ctx, js, stream, durable, publishSeq)`
    -- consumer ack floor reached `publishSeq`. **DO NOT use this
    under `t.Parallel()` on the canonical durable.** The seq-based
    wait races: AckFloor advances on whichever message finishes
    first, so a parallel sibling's ack can satisfy your wait
    before YOUR message is in Cassandra. Reserve this for serial
    tests asserting worker-lifecycle behaviour.
  - `require.Eventually` -- generic poll for everything else.

- [ ] **Decide on `t.Parallel()`** -- see "When NOT to t.Parallel()"
  immediately below.

### When NOT to `t.Parallel()`

The Parallelism table above is the source of truth, but the easy traps
when copy-pasting a sibling test:

- **Federation tests MUST NOT `t.Parallel()`.** Every
  `federation_*_test.go` test (including `zz_federation_catchup_test.go`)
  shares inbox-worker-b consumer state and the cross-site
  OUTBOX→INBOX gateway sourcing. Running them in parallel introduces
  15s+ timeouts on the shared gateway flow even when each test owns
  its own room. The test categories table marks these "no" -- when
  you add a new federation test, copy the *structure* from a sibling
  but DROP the `t.Parallel()` call if you added it.
- **Tests that mutate shared per-account state must NOT `t.Parallel()`.**
  That means anything touching alice/bob's valkey restricted-rooms
  cache or her ES user-room doc -- search tests are the canonical
  example.
- **Tests that stop/start a worker container must NOT `t.Parallel()`.**
  Catchup is the canonical example; any future worker-lifecycle test
  follows the same rule.

If your test is single-site, creates its own room, registers cleanup,
and touches no shared per-account state, `t.Parallel()` is safe.

### What `registerRoomCleanup` cleans

| Backend | What it deletes | Notes |
|---------|-----------------|-------|
| Mongo | `subscriptions`, `rooms`, `room_members`, `thread_subscriptions`, `thread_rooms` per site | Matches `roomId` / `rid` / `_id`. |
| Cassandra | `chat.messages_by_room WHERE room_id = ?`, `chat.thread_messages_by_room WHERE room_id = ?` | The `messages_by_id` dedup view CANNOT be cleaned by room (its PK is `message_id`); rows leak there permanently. Tests that count `messages_by_id` rows globally will drift. |
| Elasticsearch | Best-effort delete-by-query `{"term":{"roomId":"<rid>"}}` against `messages-*` and `user-room-*` indices | Logs and continues on error. |
| Valkey | Best-effort `DEL room:<rid>:key` and `DEL room:<rid>:key:prev` | Room-key state for encrypted rooms. |

All Cassandra / ES / Valkey steps are best-effort: they log and
continue on error so a failing teardown can't mask the actual test
failure. If you add new per-room state in any backend, extend
`registerRoomCleanup` in the same PR.

## When tests fail

- **Federation test 15s timeout**: gateway not sourcing. Check
  `INBOX_siteB.Sources` is non-empty via the nats CLI:
  ```sh
  docker run --rm --network chat-e2e_chat-e2e \
    -v /home/user/chat/docker-local/e2e/secrets:/creds:ro \
    natsio/nats-box:0.16.0 \
    nats --server nats://nats-b:4222 --creds /creds/backend.creds \
    stream info INBOX_siteB
  ```
  If empty, the BootstrapFederation step in `harness.Start` failed or
  was reset by an inbox-worker restart with stale code.

- **Encryption test "no encrypted RoomEvent"**: `broadcast-worker-a-enc`
  is down or stuck NAKing on old un-keyed rooms. Drop its consumer +
  restart:
  ```sh
  docker run --rm --network chat-e2e_chat-e2e ... \
    nats consumer rm -f MESSAGES_CANONICAL_siteA broadcast-worker-enc
  docker compose -f compose.e2e.yaml restart broadcast-worker-a-enc
  ```

- **Search test 0 results**: check ES messages index `roomId` mapping is
  `keyword` (not `text`). If it's text+keyword, the e2e template at
  `docker-local/e2e/compose.e2e.yaml` got partially applied -- rerun
  `search-init-a` and delete the stale messages index.

- **Suite hangs on TestMain**: the stack hasn't fully come up. Look at
  `docker logs inbox-worker-a` for `bootstrap streams failed`. Usually
  cured by `docker compose down -v && docker compose up -d`.
