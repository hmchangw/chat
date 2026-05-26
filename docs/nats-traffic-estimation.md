# NATS Traffic & Operations Analysis

**Baseline assumptions:** 1M rooms · 200M messages/year · 10 members/room · 100K unique users · peak = 10× avg · date: 2026-05-26

---

## 1. NATS Use Case Summary

### 1.1 Core NATS (Request/Reply — Synchronous)

| Use Case | Subject Pattern | Direction | Queue Group |
|---|---|---|---|
| Send message (async response) | `chat.user.{acct}.response.{reqID}` | gatekeeper → client | — |
| Room create | `chat.user.{acct}.request.room.{siteID}.create` | client → room-service | `room-service` |
| DM create | `chat.server.request.room.{siteID}.create.dm` | server → room-worker | `room-worker` |
| Member add/remove/role | `chat.user.{acct}.request.room.{roomID}.{siteID}.member.*` | client → room-service | `room-service` |
| Read receipt / thread read | `chat.user.{acct}.request.room.{roomID}.{siteID}.message.*` | client → room-service | `room-service` |
| Member list | `chat.user.{acct}.request.room.{roomID}.{siteID}.member.list` | client → room-service | `room-service` |
| Batch room info | `chat.server.request.room.{siteID}.info.batch` | server → room-service | `room-service` |
| Room key ensure | `chat.server.request.room.{siteID}.key.ensure` | server → room-service | `room-service` |
| Mute toggle | `chat.user.{acct}.request.room.{roomID}.{siteID}.mute.toggle` | client → room-service | `room-service` |
| Message history | `chat.user.{acct}.request.room.{roomID}.{siteID}.msg.history` | client → history-service | `history-service` |
| Message get/edit/delete | `chat.user.{acct}.request.room.{roomID}.{siteID}.msg.{get\|edit\|delete}` | client → history-service | `history-service` |
| Thread queries | `chat.user.{acct}.request.room.{roomID}.{siteID}.msg.thread.*` | client → history-service | `history-service` |
| Message search | `chat.user.{acct}.request.search.messages` | client → search-service | `search-service` |
| Room/user/app search | `chat.user.{acct}.request.search.{rooms\|users\|apps}` | client → search-service | `search-service` |
| User profile/status | `chat.user.{acct}.request.user.{siteID}.profile.*` | client → user-service | `user-service` |
| Subscription queries | `chat.user.{acct}.request.user.{siteID}.subscription.*` | client → user-service | `user-service` |
| Org members | `chat.user.{acct}.request.orgs.{orgID}.members` | client → room-service | `room-service` |

### 1.2 Core NATS (Pub/Sub — Live Broadcast, No Persistence)

| Use Case | Subject Pattern | Producer | Subscribers |
|---|---|---|---|
| Live message to room | `chat.room.{roomID}.event` | broadcast-worker | all online room members |
| Member join/leave event | `chat.room.{roomID}.event.member` | room-worker | all online room members |
| Room metadata update | `chat.room.{roomID}.event.metadata.update` | history-service | all online room members |
| User room list update | `chat.user.{acct}.event.room.update` | gatekeeper, room-worker | that user's connections |
| Subscription update | `chat.user.{acct}.event.subscription.update` | room-service | that user's connections |
| Room key update | `chat.user.{acct}.event.room.key` | room-worker | that user's connections |
| User notification | `chat.user.{acct}.notification` | notification-worker | that user's connections |

### 1.3 JetStream Streams (Persistent, At-Least-Once)

| Stream | Subject Filter | Producers | Consumers | Purpose |
|---|---|---|---|---|
| `MESSAGES_{siteID}` | `chat.user.*.room.*.{siteID}.msg.>` | client SDK | message-gatekeeper (1) | Buffer raw user sends |
| `MESSAGES_CANONICAL_{siteID}` | `chat.msg.canonical.{siteID}.>` | gatekeeper, room-worker, history-service | broadcast-worker, message-worker, notification-worker, search-sync-worker (4) | Validated canonical events |
| `ROOMS_{siteID}` | `chat.room.canonical.{siteID}.>` | room-service | room-worker (1) | Room CRUD events |
| `INBOX_{siteID}` | `chat.inbox.{siteID}.>` | room-service (local), OUTBOX source (federated) | inbox-worker, search-sync-worker×2 (3) | Federated member events |
| `OUTBOX_{siteID}` | `outbox.{siteID}.>` | room-service | — sourced by remote INBOX | Cross-site event bridge |

### 1.4 Cross-Site Federation Flow

```
Site A                                   Site B
room-service ──► OUTBOX_A ─────────────────► INBOX_B (via JetStream source)
                                               │
                                        inbox-worker (MongoDB upserts)
                                        search-sync-worker (ES indexing)
```

Event types carried in OUTBOX/INBOX:
- `member_added`, `member_removed` — room membership
- `subscription_read`, `subscription_mute_toggled` — per-user subscription state
- `thread_read` — thread read receipts

---

## 2. Traffic Estimation at Scale

### 2.1 Derived Rate Parameters

| Metric | Calculation | Value |
|---|---|---|
| Messages/year | given | 200,000,000 |
| Messages/day | ÷ 365 | 548,000 |
| Messages/sec (avg) | ÷ 86,400 | **6.3 msg/s** |
| Messages/sec (daily peak, 8-hr window) | × 3 | **19 msg/s** |
| Messages/sec (burst, 60-sec spike) | × 10 | **63 msg/s** |
| Unique users | given | 100,000 |
| Rooms per user (avg) | 1M rooms × 10 members ÷ 100K users | **100 rooms/user** |
| Messages per user per year | 200M ÷ 100K | **2,000 (~5.5/day)** |
| Peak concurrent users | 100K × 50% (business hours) | **50K** |
| Peak NATS client connections | 50K × 1.2 (multi-device) | **~60K** |

### 2.2 JetStream Stream Traffic

#### MESSAGES stream

| Metric | Value | Notes |
|---|---|---|
| Publish rate (avg / peak) | 6.3 / 63 msg/s | 1:1 with user sends |
| Message size | ~600 B | msgID + roomID + content (~300 B) + requestID + threadID |
| Throughput (avg / peak) | 3.8 KB/s / 38 KB/s | |
| Daily data written | ~330 MB | |
| Annual data written | ~120 GB | |
| **Recommended TTL** | **2 days** | gatekeeper ACKs within seconds; 2-day safety buffer for restarts |
| **Max stream storage** | **~660 MB** | 2 days × 330 MB/day |
| Consumers | **1** (message-gatekeeper) | |
| Consumer max in-flight | 200 (2 × MAX_WORKERS=100) | per gatekeeper replica |

#### MESSAGES_CANONICAL stream

| Metric | Value | Notes |
|---|---|---|
| Publish rate (avg / peak) | 7 / 70 msg/s | messages + system events (member_added etc.) |
| Message size | ~1.5 KB | full message struct + mentions array + siteID |
| Throughput (avg / peak) | 10.5 KB/s / 105 KB/s | |
| Daily data written | ~907 MB | |
| Annual data written | ~330 GB | |
| **Recommended TTL** | **7 days** | slowest consumer (search-sync-worker) may lag on restarts |
| **Max stream storage** | **~6.4 GB** | 7-day rolling window |
| Consumers | **4** | broadcast-worker, message-worker, notification-worker, search-sync-worker |
| Consumer max in-flight (per consumer) | 200 | |
| Total in-flight (4 consumers × 3 replicas) | ~2,400 msgs | |

#### ROOMS stream

| Metric | Value | Notes |
|---|---|---|
| Publish rate (avg) | 0.4 msg/s | room creates + member events; bursty on org onboarding |
| Publish rate (burst) | ~50 msg/s | bulk org imports (all members added at once) |
| Message size | ~300 B | roomID + user list + role + timestamp |
| Annual data written | ~3.8 GB | |
| **Recommended TTL** | **3 days** | room-worker is fast; 3-day buffer for deploys |
| **Max stream storage** | **~31 MB** | |
| Consumers | **1** (room-worker) | |

#### INBOX stream

| Metric | Value | Notes |
|---|---|---|
| Sources | room-service (local) + OUTBOX from remote sites | |
| Publish rate (avg) | 0.6 msg/s | ~12M member events/year + cross-site overlap |
| Message size | ~400 B | roomID + accounts[] + roomName + historySharedSince |
| Annual data written | ~8 GB (per site, 2-site federation) | |
| **Recommended TTL** | **7 days** | search-sync-worker may lag on restarts |
| **Max stream storage** | **~154 MB** | |
| Consumers | **3** | inbox-worker, search-sync-spotlight, search-sync-user-room |

#### OUTBOX stream

| Metric | Value | Notes |
|---|---|---|
| Publish rate (avg) | 0.5 msg/s | member events + subscription events + thread reads |
| Message size | ~400 B | |
| Annual data written (per site) | ~6.3 GB | |
| **Recommended TTL** | **7 days** | remote site must source before expiry |
| **Max stream storage** | **~22 MB** | |
| Consumers | **0** (sourced by remote sites) | |

#### JetStream Storage Summary

| Stream | TTL | Max Storage | Consumers | Throughput (avg) |
|---|---|---|---|---|
| MESSAGES | 2 days | 660 MB | 1 | 3.8 KB/s |
| MESSAGES_CANONICAL | 7 days | 6.4 GB | 4 | 10.5 KB/s |
| ROOMS | 3 days | 31 MB | 1 | 120 B/s |
| INBOX | 7 days | 154 MB | 3 | 240 B/s |
| OUTBOX | 7 days | 22 MB | 0 | 200 B/s |
| **Total** | | **~7.3 GB** | **9** | **~15 KB/s** |

### 2.3 Core NATS (Sync RPC) Traffic

| Subject Group | Rate (avg) | Request Size | Response Size | Notes |
|---|---|---|---|---|
| Message history/get | 1.5/s | 200 B | 50–200 KB | Paginated; 10–100 msgs per page |
| Room member ops | 0.4/s | 200 B | 100 B | |
| Subscription queries | 5/s | 100 B | 2–20 KB | Triggered by every room open |
| Search queries | 0.5/s | 300 B | 10–100 KB | |
| User profile/status | 3/s | 100 B | 500 B | |
| Room key ensure | 0.05/s | 100 B | 100 B | |
| Read receipts | 1/s | 100 B | 50 B | |
| **Total sync RPC** | **~12/s avg, 60/s peak** | | | |

### 2.4 Core NATS (Live Broadcast) Traffic

Fan-out accounts for online ratio: 50K concurrent ÷ 100K total = 50% of room members online at peak → ~5 online subscribers per room on average.

| Subject | Publish Rate (avg/peak) | Online fan-out | Delivery Rate (avg/peak) | Payload |
|---|---|---|---|---|
| `chat.room.{roomID}.event` | 6.3 / 63 msg/s | 5 online/room | **32 / 315 del/s** | 2 KB |
| `chat.user.{acct}.event.room.update` | ~50 / 500 pub/s | 1 (user-scoped) | 50 / 500 pub/s | 500 B |
| `chat.user.{acct}.notification` | 3 / 30 /s | 1 | 3 / 30 /s | 300 B |
| Room member events | 0.4 / 40 /s | 5 | 2 / 200 del/s | 300 B |

| Bandwidth Metric | Value |
|---|---|
| Inbound to NATS (publish) | ~105 KB/s avg |
| Outbound from NATS (fan-out) | **~310 KB/s avg** |
| Peak outbound (burst) | **~3.1 MB/s** |

### 2.5 NATS Connection & Subscription Summary

| Resource | Value | Basis |
|---|---|---|
| Peak concurrent users | 50K | 50% of 100K during business hours |
| Client connections (×1.2 multi-device) | 60K | |
| Service connections | ~100 | 10 services × 5 replicas × 2 conns |
| **Total NATS connections** | **~60,100** | |
| Active room subscriptions per client | ~15 | user has 100 rooms; ~15 open at once |
| User-scoped + notification subs | 2 | |
| Subscriptions per client | **17** | |
| Total client subscriptions | **~1.02M** | 60K × 17 |
| Service subscriptions | ~500 | RPC queue groups + JetStream consumers |
| **Total subscriptions** | **~1.02M** | |
| JetStream consumers (per site) | **9** | 1+4+1+3 across 5 streams |

### 2.6 Operational Sizing Implications

| Concern | Assessment |
|---|---|
| NATS server RAM | ~1–2 GB for routing table at 1M subscriptions |
| NATS `max_connections` | Raise default (64K) to 100K |
| JetStream cluster | 3 nodes × 8 GB RAM — comfortable for 7.3 GB total storage |
| Fan-out bandwidth | 1 Gbps NIC sufficient at peak 3.1 MB/s |
| MESSAGES_CANONICAL (slowest consumer) | Primary storage watch item: 6.4 GB rolling; monitor search-sync-worker lag weekly |

---

## 3. Test Scenarios & Failover Solutions

### 3.1 Test Scenarios

#### Category A — NATS Cluster Failure

| Scenario | Trigger | Expected Behaviour | Verify Via |
|---|---|---|---|
| **A1** Single NATS node crash | `docker stop nats-2` in 3-node cluster | Leader election < 6s; clients reconnect via `MaxReconnects(-1)` + `ReconnectWait(1s)` | Message delivery resumes; no JetStream consumer rebalancing failure |
| **A2** Network partition (split-brain) | `iptables` block between NATS nodes | Raft quorum lost; services suspend publishes until healed | Clients queue in-process; no duplicate delivery after heal |
| **A3** Full NATS outage → restart | Kill all NATS nodes, restart | JetStream consumers re-attach with durable name; pick up from last ACK sequence | No message loss; consumer lag increases during outage window |
| **A4** JetStream leader failover | Kill JetStream meta-leader | New leader elected < 10s; stream operations resume | `js.CreateOrUpdateStream` idempotent call succeeds after reconnect |
| **A5** OUTBOX source interruption | Kill remote NATS site | INBOX stream stops receiving; inbox-worker stalls at last ACK | After healing, sourcing resumes; `AckPolicy:Explicit` + `MaxDeliver:5` retries events |

#### Category B — Consumer/Service Failure

| Scenario | Trigger | Expected Behaviour | Verify |
|---|---|---|---|
| **B1** message-gatekeeper crash mid-message | Kill process while ACK pending | MESSAGES stream redelivers; `Msg-ID` dedup prevents duplicate canonical publish | Cassandra has exactly 1 record per msgID |
| **B2** broadcast-worker stuck (slow encrypt) | Simulate 5s encryption delay | Semaphore backs up; consumer lag grows; no new messages lost | Lag returns to 0 after processing; no OOM |
| **B3** notification-worker behind (search-sync ACKs ahead) | Rate-limit notification-worker | MESSAGES_CANONICAL held by slowest consumer; TTL=7d safety margin | notification-worker catches up; stream storage stays within max |
| **B4** search-sync-worker restart with uncommitted offset | Restart without Elasticsearch | Durable consumer replays from last ACK; ES bulk indexing on next start | No gaps in search index after catchup |
| **B5** room-worker crash during member event fan-out | Kill after MongoDB write, before NATS publish | ROOMS stream redelivers event; idempotent upserts in MongoDB prevent double-write | `chat.room.{roomID}.event.member` published exactly once per event |

#### Category C — DB / Storage Failure

| Scenario | Trigger | Expected Behaviour | Verify |
|---|---|---|---|
| **C1** MongoDB primary failover | `rs.stepDown()` in replica set | Services log mongo write error; JetStream NAK + exponential backoff redelivery | Event eventually persisted after replica elected |
| **C2** Cassandra node down (RF=3) | Kill 1 of 3 Cassandra nodes | `LocalQuorum` satisfied with 2 of 3; no write errors | message-worker continues uninterrupted |
| **C3** Cassandra split quorum | Kill 2 of 3 nodes | Write fails; message-worker NAKs; consumer lag grows | After restore: no duplicate messages (idempotent insert by msgID) |
| **C4** Elasticsearch cluster yellow/red | Shard reallocation | search-sync-worker bulk flush returns 429/503; exponential backoff retry | Bulk buffer drains after ES recovers; `BulkFlushInterval` catches up |
| **C5** Valkey cluster node failure | Kill 1 Valkey shard | Affected slot keys temporarily unavailable; room-worker triggers key re-derive | Keys re-derived from room-service via NATS; no encryption gap > 10s |

#### Category D — Load & Backpressure

| Scenario | Description | Pass Criteria |
|---|---|---|
| **D1** 10× message burst (630 msg/s for 60s) | Spike test via NATS publish loop | MESSAGES consumer lag < 1,000; broadcast p99 < 500ms; no OOM |
| **D2** 60K concurrent connections | Ramp clients to 60K | NATS memory < 2 GB; subscription routing latency < 1ms |
| **D3** Large room broadcast (1,000 members) | Publish to room with 1K subscribers | `chat.room.{roomID}.event` fan-out within 200ms for all subscribers |
| **D4** Cross-site replication lag | Throttle OUTBOX source to 10 msg/s | INBOX consumer lag < 10,000; no event loss; lag recovers on un-throttle |
| **D5** JetStream stream full (storage limit hit) | Set MESSAGES max-bytes = 100 MB | New publishes fail with `429 MaxBytesExceeded`; gatekeeper returns error to client |

### 3.2 Failover Solutions

#### NATS Cluster Resilience

```
┌────────────────────────────────────────────────────────────────────┐
│  3-node JetStream cluster  (R=3 for all streams)                   │
│  nats-1 (leader)    nats-2 (follower)    nats-3 (follower)        │
│  ┌─────────────┐   ┌──────────────┐   ┌──────────────┐           │
│  │JetStream R=3│   │JetStream R=3 │   │JetStream R=3 │           │
│  └─────────────┘   └──────────────┘   └──────────────┘           │
│  Raft quorum: needs 2/3 nodes for writes                          │
└────────────────────────────────────────────────────────────────────┘
```

| Layer | Solution | Config |
|---|---|---|
| NATS cluster | 3-node JetStream cluster with Raft replication | All streams: `Replicas: 3` |
| Client reconnect | Built-in nats.go reconnect logic | `MaxReconnects(-1)`, `ReconnectWait(1s)`, `ConnectHandler` + `DisconnectHandler` hooks |
| Consumer durability | Durable consumers with explicit ACK | `AckPolicy: Explicit`, `MaxDeliver: 5`, `AckWait: 30s` |
| Deduplication | JetStream `Msg-ID` header on every canonical publish | All MESSAGES_CANONICAL and OUTBOX publishes set `Nats-Msg-Id: {msgID}`; 2-minute dedup window |
| Stream limits | Size + age limits with `DiscardNew` policy | Prevents unbounded growth; clients receive clear 429 errors |
| Cross-site | OUTBOX TTL=7d; remote INBOX sources with `StartPolicy: LastReceived` | `MaxDeliver: -1` for sourced streams; federation config owned by ops/IaC |

#### Client-Side (Application Layer) Resilience

| Layer | Solution | Implementation |
|---|---|---|
| NATS reconnect buffer | Pending message buffer during disconnect | `nats.PendingLimits(1000, 64*1024*1024)` — up to 1K msgs / 64 MB buffered while reconnecting |
| JetStream publish retry | Retry loop on `js.Publish` failure | `js.PublishMsg` with `MsgID`; retry up to 5× with exponential backoff (100ms → 1.6s) |
| Consumer NAK backoff | Slow retry for transient DB errors | `msg.NakWithDelay(baseDelay * 2^attempt + jitter)` |
| MongoDB resilience | Replica set with `retryWrites: true` | `mongoutil.Connect` handles RS failover; `w: majority` writes |
| Cassandra resilience | RF=3, `LocalQuorum` consistency | `cassutil.Connect` sets `Consistency: LocalQuorum`; survives 1 node loss |
| Valkey cluster resilience | 3-master × 3-replica cluster | `redis.ClusterClient` routes by slot; `MaxRetries: 3`; failover transparent to app |
| Subscription cache warm | Pre-warm on startup | message-gatekeeper loads active subscriptions at start; stale entries expire in 2m |
| Outbox idempotency | `Msg-ID` on all outbox publishes | Prevents duplicate federation events on NATS leader failover |

#### MongoDB Failover

| Failure | Recovery | RTO |
|---|---|---|
| Primary stepdown | Automatic RS election | ~10–15s |
| Primary + 1 secondary down | Manual intervention required | Minutes |
| Full RS loss | Restore from continuous backup | Hours |
| Write during election | `retryWrites: true` replays automatically | Transparent |

#### Cassandra Failover

| Failure | Recovery | RTO |
|---|---|---|
| 1 node down (RF=3) | `LocalQuorum` satisfied by 2 nodes | Transparent |
| 2 nodes down | `LocalQuorum` fails; message-worker stalls; events replay from JetStream | Automatic after restore |
| Full DC loss | Requires `NetworkTopologyStrategy` with 2 DCs | Minutes (cross-DC) |

---

## 4. Development & Rollout Schedule

**Anchors:** Integration testing begins end of June 2026 · Production push end of August 2026

```
May 26 ──────────────────────────────────────────────────── Aug 31
  │
  ├── Phase 1: Development & Unit Tests ──────────── Jun 13
  ├── Phase 2: Integration Test Preparation ───────── Jun 27
  ├── Phase 3: Integration Testing ─────────────────── Jul 25
  ├── Phase 4: Staging & Performance ────────────────── Aug 15
  └── Phase 5: Production Rollout ───────────────────── Aug 29
```

### Phase 1 — Development & Unit Tests (May 26 – Jun 13, 3 weeks)

| Week | Work Items | Exit Criteria |
|---|---|---|
| W1 (May 26–Jun 1) | Review stream configs for scale (TTL, replica count, storage limits per §2.2); design failover retry utilities; NATS reconnect config baseline | Stream config YAML drafted; `pkg/natsutil` reconnect helpers designed |
| W1 | Define JetStream publish retry wrapper with `Msg-ID` dedup; add to `pkg/natsutil` | Unit tests green (mock JetStream) |
| W2 (Jun 2–8) | Implement NAK backoff in all JetStream consumers (broadcast-worker, message-worker, notification-worker, search-sync-worker, inbox-worker) | All consumer handlers NAK with exponential delay on transient errors |
| W2 | Add NATS reconnect buffer config to all `main.go` connection setups; implement `DisconnectHandler` / `ReconnectHandler` logging | Unit test: disconnect simulation doesn't drop in-flight publishes |
| W2 | Implement dedup `Msg-ID` header on MESSAGES_CANONICAL and OUTBOX publishes | Unit test: duplicate publish detected within 2-min window |
| W3 (Jun 9–13) | Update stream bootstrap helpers with production-grade limits (§2.2 storage/TTL values) | `make test` green across all services |
| W3 | `make lint`, `make sast` clean; fix any gosec/semgrep findings | `make sast` exits 0 |
| W3 | Update `docs/client-api.md` if any handler subjects changed | PR diff has no undocumented subject changes |

### Phase 2 — Integration Test Preparation (Jun 14 – Jun 27, 2 weeks)

| Week | Work Items | Exit Criteria |
|---|---|---|
| W4 (Jun 14–20) | Write integration test scaffolding: `testutil.NATS(t)` with 3-node JetStream; define `TestMain` for all affected services | `make test-integration SERVICE=message-gatekeeper` runs cleanly |
| W4 | Write Scenario A1/A3: NATS node crash + reconnect; verify consumer resumes from last ACK | Test passes in CI Docker environment |
| W4 | Write Scenario B1: gatekeeper crash mid-ACK → MESSAGES redelivery → no duplicate canonical | 0 duplicates in Cassandra after 100 messages through restart |
| W5 (Jun 21–27) | Write Scenario C1/C2: MongoDB stepDown, Cassandra node down; verify consumer NAK + retry | All tests pass; no message loss |
| W5 | Write Scenario D1 load test: 10× burst (630 msg/s for 30s) | Consumer lag < 1,000; p99 broadcast < 500ms |
| W5 | Write cross-site federation integration test: OUTBOX→INBOX sourcing with simulated site B | INBOX consumer receives all events; inbox-worker persists correctly |
| **Jun 27** | **Integration test code complete; all tests tagged `//go:build integration`** | `make test-integration` exits 0 |

### Phase 3 — Integration Testing Execution (Jun 28 – Jul 25, 4 weeks)

| Week | Activity | Exit Criteria |
|---|---|---|
| W6 (Jun 28–Jul 4) | Run full `make test-integration` suite; triage failures; fix Scenario A/B failures | A1–A5, B1–B5 all green |
| W7 (Jul 5–11) | Run Scenario C (DB failover) tests in CI; fix Cassandra quorum loss behaviour | C1–C5 all green |
| W7 | Run Scenario D (load) tests locally with Docker; tune semaphore sizes and stream limits | D1–D3 pass at target p99 |
| W8 (Jul 12–18) | Cross-site federation testing: stand up 2-site docker-compose; run D4 | D4 passes; no federation event loss |
| W8 | Fix bugs found during integration; re-run affected test categories | No open P1 bugs |
| W9 (Jul 19–25) | Final integration test regression pass; update `docs/` with confirmed stream limits and TTL values | All integration tests green; test report signed off |

### Phase 4 — Staging Validation & Performance Testing (Jul 26 – Aug 15, 3 weeks)

| Week | Activity | Exit Criteria |
|---|---|---|
| W10 (Jul 26–Aug 1) | Deploy to staging (3-node NATS, 3-node Mongo RS, 3-node Cassandra); validate all services healthy | All `GET /healthz` return 200 |
| W10 | Replay 1M synthetic messages; monitor consumer lag, Cassandra write latency, NATS memory | No consumer > 500 msg lag; Cassandra p99 write < 50ms |
| W11 (Aug 2–8) | Chaos test in staging: kill NATS leader, kill Mongo primary, kill 1 Cassandra node | Zero message loss confirmed by Cassandra rowcount vs NATS sequence |
| W11 | Validate cross-site federation in staging (2 staging sites) | Federation lag < 5s at 1K events/s |
| W12 (Aug 9–15) | Soak test: 48-hour continuous load at 6.3 msg/s avg with 60K simulated connections | No memory leak; JetStream storage within §2.2 limits; no goroutine leak |
| W12 | Final `make sast` + `make lint` gate; security review of any new NATS subjects | CI pipeline green; SAST exits 0 |

### Phase 5 — Production Rollout (Aug 16 – Aug 29, 2 weeks)

| Date | Activity | Notes |
|---|---|---|
| Aug 16 | Production readiness review: stream config, NATS cluster sizing, MongoDB index review | Sign-off required before rollout |
| Aug 17–18 | Bootstrap streams in production NATS (ops runs `BOOTSTRAP_STREAMS=true` once) | All 5 streams created with R=3 replicas and production limits |
| Aug 19–22 | Canary rollout: deploy updated services to 10% of traffic; monitor consumer lag and error rates | No P1 errors; lag < 100 msgs |
| Aug 23–25 | Ramp to 50% → 100%; monitor; keep previous version available for 24h rollback | Linear traffic shift via Kubernetes weight |
| Aug 26–28 | Observe production metrics: NATS dashboard, consumer lag, Cassandra write latency, MongoDB slow query log | All SLIs within targets |
| **Aug 29** | **Go/no-go decision; remove canary traffic split; production rollout complete** | Post-rollout runbook updated |

### Key Risks & Mitigation

| Risk | Probability | Mitigation |
|---|---|---|
| NATS `max_connections` default (64K) hit before tuning | Medium | Raise to 100K in NATS config early; monitor FD limits |
| Cassandra hot partition at burst (63 msg/s to same room) | Low | Bucket key `(room_id, bucket)` distributes writes; tune `MESSAGE_BUCKET_HOURS` |
| Cross-site OUTBOX→INBOX federation config owned by ops | High (coordination risk) | Include federation config template in `deploy/` as reference; align with ops in W6 |
| search-sync-worker ES lag causing MESSAGES_CANONICAL to hold 7 days of data | Medium | Add `MaxAckPending` limit; monitor consumer lag weekly; ensure ES timeout < `AckWait` |
| Valkey key-slot miss under partial cluster failure | Medium | Add `KEY_GRACE_PERIOD` buffer; cover Valkey failover in Scenario C5 integration test |
