# e2e suite

End-to-end tests for the chat backend. Drives the full compose stack
(two sites, NATS supercluster gateway, MongoDB + Cassandra + ES per site,
Keycloak per site, auth-service + 8 backend workers per site) and
asserts behaviour at the user-API boundary.

## Quick start

```bash
make e2e            # testcontainers-owned lifecycle (CI parity)
make e2e-up         # bring up compose stack in the background
E2E_REUSE_STACK=1 go test -tags e2e ./e2e/...   # iterate against the up stack
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
| Single-site message / room / thread / negative / errors / subject / auth-HTTP / cassandra / stress / encryption | yes | Each test creates its own room and registers cleanup. Authenticate returns a fresh per-call NATS connection (different nkey each time) so multiple parallel tests can share the same realm user without colliding. |
| Search (`TestSearch_MessageVisibleAfterIndex`) | no | Mutates alice's per-account valkey + ES user-room doc. |
| Catchup (`TestFederation_CatchUpAfterOutage`) | no | Stop/starts inbox-worker-b. Skipped entirely under REUSE. |
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

## Adding a new test

1. Pick the right file (or add a new one) per the table above.
2. Default to single-site (`stack.SiteA`) unless the assertion needs
   federation.
3. Create a per-test room with `subject.RoomCreate(...)` and register
   cleanup with `registerRoomCleanup`.
4. Authenticate users via `site.Authenticate(t, ctx, "alice"|"bob")`.
   Don't reuse `Identity` across tests.
5. If your test queries Cassandra/ES/Mongo directly, prefer the harness
   methods over building your own client.
6. For async chains, never sleep -- use `awaitCanonicalAcked` /
   `require.Eventually`.
7. Add `t.Parallel()` if the test is single-site + per-test-room + does
   not mutate shared per-account state (valkey, ES user-room).

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
