# NATS Traffic Estimation

> Sizing model for a single site's NATS/JetStream traffic. Inputs are parameterized
> so the model can be re-run when assumptions change. All figures are estimates for
> capacity planning, not measured production telemetry.

## 1. Scope

This document estimates NATS message rates, bandwidth, connection-state load, and
JetStream storage for one independent site. It covers:

- The core JetStream streams and request/reply (R/R) endpoints that carry traffic.
- Per-payload size references used in the estimate.
- A traffic model for a **single connection per user** (baseline).
- A traffic model for **multiple concurrent connections per user** (desktop + mobile +
  web tabs).
- **Storage estimation** from per-stream retention (TTL).

Each site runs its own NATS server, so these figures are per-site.

> **Pending streams:** `HRSYNC_{siteID}` and `PUSH_NOTIFICATIONS_{siteID}` are not yet
> merged. They are included here on the spec described to date and should be revisited
> when the real spec lands.

## 2. Stream & Endpoint Inventory

### 2.1 JetStream streams

| Stream | Subject | Producer | Consumer(s) | In estimate? |
|--------|---------|----------|-------------|:---:|
| `MESSAGES_{siteID}` | `chat.user.*.room.*.{siteID}.msg.>` | Client | message-gatekeeper | ✅ |
| `MESSAGES_CANONICAL_{siteID}` | `chat.msg.canonical.{siteID}.>` | message-gatekeeper, room-worker (sys msgs) | message-worker, broadcast-worker, notification-worker, search-sync-worker | ✅ |
| `ROOMS_{siteID}` | `chat.user.*.request.room.*.{siteID}.member.>` | room-service | room-worker | ✅ |
| `INBOX_{siteID}` | `chat.inbox.{siteID}.*` + `…aggregate.>` | room-worker (local member events), remote OUTBOX (sourced) | inbox-worker, search-sync-worker | ✅ |
| `PUSH_NOTIFICATIONS_{siteID}` | `chat.server.notification.push.{siteID}.>` | notification-worker *(spec pending)* | push-gateway worker → APNs/FCM *(spec pending)* | ✅ |
| `HRSYNC_{siteID}` | `hr.sync.>` | HR sync source *(spec pending)* | hr-sync consumer *(spec pending)* | ✅ |
| `MIGRATION_OPLOG_{siteID}` | `migration.oplog.>` | Migration source *(spec pending)* | migration applier (1) *(spec pending)* | ✅ |
| `OUTBOX_{siteID}` | `outbox.{siteID}.to.{destSiteID}.{eventType}` | room-worker, room-service, message-worker | Remote site INBOX | ❌ excluded per scope |

### 2.2 Core NATS delivery subjects (server → client)

| Subject | Publisher | Purpose |
|---------|-----------|---------|
| `chat.room.{roomID}.stream.msg` (+ DM `chat.user.{a}.stream.msg`) | broadcast-worker | Message delivery (incl. member-change sys messages) |
| `chat.room.{roomID}.event.metadata.update` | broadcast-worker | Room metadata (`lastMessageAt`) — emitted per message |
| `chat.user.{account}.notification` | notification-worker | Desktop banner (mentions/DMs) |
| `chat.user.{account}.event.subscription.update` | room-worker, inbox-worker | Room added/removed |
| `chat.user.{account}.event.presence` | Presence (future) / client heartbeat | Online/offline/away |

### 2.3 Request/Reply endpoints

| Group | Subjects | Heaviest response |
|-------|----------|-------------------|
| Subscription R/R | `…subscription.{getCurrent,getRooms,getChannels,getApps,count}` | `subscriptionListResp{Subscriptions[]}` |
| Message history R/R | `…msg.{history,next,surrounding,thread,get,edit,delete}` | `LoadHistoryResponse{Messages[]}` |
| Room R/R | `…rooms.create`, `…room.{siteID}.info.batch`, `…member.list`, `…key.ensure` | `RoomsInfoBatchResponse` / `ListRoomMembersResponse` |
| Search R/R | `…search.{siteID}.{messages,rooms,apps,users}` | `SearchMessagesResponse` |

## 3. Payload Size Reference

| Category | Streams / Endpoints | avg | max |
|----------|---------------------|-----|-----|
| Message JetStream | MESSAGES, MESSAGES_CANONICAL | 500B–1.5KB | ~20KB |
| Push notification | PUSH_NOTIFICATIONS | ~0.8KB | ~15KB |
| HR sync | HRSYNC (≈ `model.User`: ~12 fields) | ~0.5KB | ~1KB |
| Migration oplog | MIGRATION_OPLOG | ~130KB | — |
| Room JetStream | ROOMS | 200–400B | ~5KB |
| Federation | INBOX | 200–400B | ~5KB |
| Message R/R | history, search-messages | 15–50KB | 100KB+ |
| Room R/R | RoomsInfoBatch, CreateRoom, member.list | 2–20KB | ~65KB |
| Subscription R/R | subscription.get*, member-list | 100–300KB | ~700KB |

## 4. Parameters (locked inputs)

| Parameter | Symbol | Value |
|-----------|--------|-------|
| Users | U | 20,789 |
| Subjects subscribed per connection | S | 100 (user + room) |
| Presence subjects watched per connection | P | **20** |
| Messages per day (human + bot) | M | 4,000,000 |
| Recipients per room (fan-out) | F | 100 |
| Subscription R/R ops per day per connection | R_sub | **1,000** |
| Message-history R/R ops per day per connection | R_hist | **150** |
| Room R/R ops per day per user | R_room | **250** |
| Member add/remove ops per day per user *(assumption — subset of R_room)* | R_member | **50** |
| Search ops per day per user | R_search | ~5 |
| Presence status changes per day per user | C_pres | ~20 |
| Push notifications per day | M_push | 4,000,000 |
| HR sync records per daily run (burst @ 100 msg/s) | M_hr | 40,000 |
| Migration oplog QPS (sustained 24/7, 130KB payload, 1 consumer) | Q_mig | 4,000 |
| Peak factor (business-hours concentration) | k | ~4× |

> **R_member is an assumption.** The 250 room ops/day "include member add/remove," so
> `R_member = 50` is the member-change slice that drives the ROOMS stream + sys-message
> fan-out; the remaining ~200 are read-only Room R/R. Every line tagged *(member-driven)*
> scales linearly with `R_member` — if member changes are rarer (e.g. ~5/day), those
> lines shrink ~10×.

## 5. Methodology — ingress vs. fan-out

NATS traffic is **not** the inbound action count; it is dominated by fan-out (delivery
to subscribers). Each message flows:

```
Client ─pub→ MESSAGES ─→ gatekeeper ─pub→ CANONICAL ─→ 4 consumers
                                                       └─ broadcast-worker ─pub→ chat.room.{id}.stream.msg ──→ ×F members
                                                       └─ broadcast-worker ─pub→ chat.room.{id}.event.metadata.update ──→ ×F members
                                                       └─ notification-worker ─pub→ chat.user.{a}.notification
```

Per message: **~7 JetStream hops** (2 stores + 5 consumer deliveries, persisted) plus
**~2×F core deliveries** (room stream + metadata). The broadcast-worker publishes once
per subject; the multiplication happens at the NATS delivery layer.

`PUSH_NOTIFICATIONS` and `HRSYNC` are **terminal JetStream streams** — a single
worker consumes and forwards out-of-band (APNs/FCM, HR DB), so they incur no ×F NATS
fan-out.

## 6. Single-Connection Baseline — per stream & endpoint

One NATS connection per user (connections = U = 20,789). Peak ≈ avg × k. "Ops/day"
counts publishes + consumer deliveries (JetStream), deliveries (core), or requests (R/R).

### 6.1 JetStream streams

| Stream | Driver | Pub/day | Deliveries/day | Ops/day | avg msg/s | Payload | Bytes/day |
|--------|--------|--------:|---------------:|--------:|----------:|---------|----------:|
| `MESSAGES` | M (client → gatekeeper) | 4M | 4M | 8M | 93 | 1KB | 8 GB |
| `MESSAGES_CANONICAL` | (M + member sys) × (1 pub + 4 consumers) | 5M | 20M | 25M | 289 | 1KB | 25 GB |
| `ROOMS` *(member-driven)* | R_member × U | 1.04M | 1.04M | 2.08M | 24 | 0.4KB | 0.8 GB |
| `INBOX` *(member-driven)* | local member events × 2 consumers | 1.04M | 2.08M | 3.12M | 36 | 0.3KB | 0.9 GB |
| `PUSH_NOTIFICATIONS` | M_push × (1 pub + 1 consumer) | 4M | 4M | 8M | 93 | 0.8KB | 6.4 GB |
| `HRSYNC` | M_hr × (1 pub + 1 consumer); 100 msg/s burst | 40K | 40K | 80K | ~1 (200/s burst) | 0.5KB | 0.04 GB |
| **Subtotal (excl. migration)** | | | | **~46M** | **~536** | | **~41 GB** |
| `MIGRATION_OPLOG` | Q_mig × 86,400 × (1 pub + 1 consumer); sustained | 345.6M | 345.6M | 691M | 8,000 | 130KB | 89,860 GB |
| **Subtotal (incl. migration)** | | | | **~737M** | **~8,536** | | **~89.9 TB** |

`MESSAGES_CANONICAL` pub = 4M user messages + ~1.04M member-change system messages.
`INBOX` carries local member add/remove events (room-worker publishes to the local INBOX
regardless of site); federated cross-site inflow is excluded with OUTBOX.

### 6.2 Core delivery subjects (server → client, fanned out ×F or ×P)

| Subject | Driver | Pub/day | Deliveries/day | avg msg/s | Payload | Bytes/day |
|---------|--------|--------:|---------------:|----------:|---------|----------:|
| `chat.room.*.stream.msg` (+DM) | M × F | 4M | 400M | 4,630 | 1KB | 400 GB |
| `chat.room.*.event.metadata.update` | M × F | 4M | 400M | 4,630 | 0.3KB | 120 GB |
| member-change broadcast (sys msg/event) *(member-driven)* | R_member × U × F | 1.04M | 104M | 1,204 | 0.4KB | 42 GB |
| `chat.user.*.notification` | M × ~10% | 0.4M | 0.4M | 5 | 1KB | 0.4 GB |
| `chat.user.*.event.subscription.update` *(member-driven)* | R_member × U × ~2 | 1.04M | 2.08M | 24 | 0.5KB | 1 GB |
| `chat.user.*.event.presence` | U × C_pres × P | 0.42M | 8.3M | 96 | 0.15KB | 1.3 GB |
| `chat.room.*.event.typing` | active room only | — | — | — | — | *(ignored)* |
| **Core delivery subtotal** | | | **~915M** | **~10,590** | | **~565 GB** |

Presence delivery dropped sharply (was 42M) because P went 100 → 20.

### 6.3 Request/Reply endpoints

| Endpoint group | Driver | Req/day | req/s | Resp payload | Bytes/day |
|----------------|--------|--------:|------:|-------------|----------:|
| **Subscription R/R** | R_sub × U | 20.79M | 240 | 150KB | **3,119 GB** |
| Message history R/R | R_hist × U | 3.12M | 36 | 30KB | 94 GB |
| Room R/R | R_room × U | 5.20M | 60 | 10KB | 52 GB |
| Search R/R | R_search × U | 0.10M | 1.2 | 30KB | 3 GB |
| **R/R subtotal** | | **~29.2M** | **~338** | | **~3,268 GB** |

### 6.4 Totals

| Layer | Ops or deliveries/day | avg msg/s | Bytes/day |
|-------|----------------------:|----------:|----------:|
| JetStream (excl. migration) | ~46M | ~536 | ~41 GB |
| Core delivery subjects | ~915M | ~10,590 | ~565 GB |
| R/R (req + resp) | ~58M | ~676 | ~3,268 GB |
| **Steady-state subtotal (excl. migration)** | **~1.02B/day** | **~11,800 avg · ~47,000 peak** | **~3.87 TB/day** |
| `MIGRATION_OPLOG` (sustained, flat) | ~691M | ~8,000 | ~89.9 TB |
| **TOTAL (incl. migration)** | **~1.71B/day** | **~19,800 avg · ~55,000 peak** | **~93.8 TB/day** |

`MIGRATION_OPLOG` alone is **~96% of all bytes** (~1.04 GB/s of the ~1.09 GB/s total).
It is sustained 24/7 with no business-hours peaking, so it adds a flat term to both avg
and peak. The steady-state subtotal is broken out so the rest of the system stays
legible underneath it.

### 6.5 Connection state

> **Connection calculation.** Subscription interests = connections × avg subscriptions
> per connection. Example: **50,000 connections × 100 subscriptions = 5,000,000 (5M)
> interests.** (Note: 5,000 × 100 = 500K — to reach 5M you need 50,000 connections, the
> multi-device scenario in §7.) Presence adds `connections × P` more interests.

At the single-connection baseline: 20,789 connections × (S=100 + P=20) =
**~2.5M subscription interests**.

### 6.6 Bottlenecks

- **MIGRATION_OPLOG = ~90 TB/day (≈ 96% of all bytes, ~1.04 GB/s sustained)** — the
  single dominant load while migration runs. At ~8.3 Gbps each way it is a
  **network-capacity** problem, not a message-rate one; size NICs/inter-AZ links and
  JetStream disk (15 TB logical / ~45 TB at R=3) for it explicitly, and consider an
  isolated stream/account or dedicated nodes so it cannot starve live chat traffic.
- **Subscription R/R = ~3.12 TB/day** — the dominant *steady-state* (post-migration)
  bandwidth driver after R_sub rose 150 → 1,000/day. 1,000 full 150KB list-fetches per
  user per day is ~1 every ~30s of an 8h workday — verify this is real client behavior,
  not redundant polling.
- **Message + metadata delivery = ~800M deliveries/day (~9,300/s)** — the message-rate /
  connection bottleneck, driven by F = 100.
- **Member-change fan-out = ~104M deliveries/day** — sized by the `R_member = 50`
  assumption; confirm before trusting.

### 6.7 Optimization levers

- **Lighter/delta subscription endpoint** (instead of full 150KB list) — highest-value
  lever; directly attacks the dominant 3.12 TB/day.
- **Coalesce metadata updates** (e.g. max 1/sec/room) → halves the message-delivery term.

## 7. Multiple Connections Per User

Real users connect from several clients at once — e.g. **1 desktop + 1 mobile + 3 web
tabs = 5 concurrent connections** (`D = 5`). Each connection is an independent NATS
subscriber that re-subscribes and fetches its own state.

### 7.1 What scales with D and what stays flat

| Scales linearly with D | Stays flat (independent of D) |
|------------------------|-------------------------------|
| All core deliveries (message, metadata, member-change, presence, notification) | Message ingress (user sends from one client) |
| Subscription, history, room, search R/R | JetStream pipeline & terminal streams (server-side) |
| Connections & subscription interests | `MIGRATION_OPLOG`, `PUSH`, `HRSYNC` (server-side, not client-facing) |

Rule of thumb: **ingress and server-side processing are flat; delivery/egress, R/R, and
connection state scale with D.** Effective fan-out becomes `F × D = 500` per message.

### 7.2 D=1 vs. D=5

| Layer | D=1 deliveries/day | D=5 deliveries/day | D=1 bytes/day | D=5 bytes/day |
|-------|-------------------:|-------------------:|--------------:|--------------:|
| JetStream (excl. migration) | ~46M | ~46M *(flat)* | ~41 GB | ~41 GB |
| Core delivery | ~915M | ~4,575M | ~565 GB | ~2,825 GB |
| R/R (req+resp) | ~58M | ~292M | ~3,268 GB | ~16,340 GB |
| **Steady-state subtotal** | **~1.02B** | **~4.91B** | **~3.87 TB** | **~19.2 TB** |
| `MIGRATION_OPLOG` *(flat across D)* | ~691M | ~691M | ~89.9 TB | ~89.9 TB |
| **TOTAL (incl. migration)** | **~1.71B** | **~5.60B** | **~93.8 TB** | **~109 TB** |
| **avg / peak msg/s (incl. migration)** | ~19.8k / ~55k | **~65k / ~235k** | | |

Connection state at D=5: **~104k connections** × (100 + 20) = **~12.5M subscription
interests**. `MIGRATION_OPLOG` is server-side and does not scale with D — at D=5 it
shrinks from 96% to ~83% of total bytes simply because client traffic grew around it.

### 7.3 Takeaways for multi-device

- Traffic scales **~linearly with D** (~5×), because the only flat term (ingress + terminal
  streams) is a tiny fraction of the total.
- **Subscription R/R dominates everything at D=5 (~15.6 TB/day)** — each of the 5
  connections independently re-fetches the 150KB list 1,000×/day. The delta/lighter
  endpoint (§6.7) is by far the highest-value optimization.
- **Reconnect storms multiply by D**: a restart triggers up to `U × D ≈ 104k`
  simultaneous 150KB list fetches — jitter/rate-limit to avoid a thundering herd.

## 8. Storage Estimation (per-stream TTL)

JetStream storage at steady state ≈ `publish-rate × retention (TTL) × payload`. Figures
are **logical** bytes (single replica); multiply by the replication factor (typically
R=3) for on-disk usage. TTLs are per the stream spec.

| Stream | TTL | Pub/day | Payload | Retained msgs | Logical storage |
|--------|-----|--------:|---------|--------------:|----------------:|
| `MESSAGES` | 8 hr | 4M | 1KB | 1.33M | 1.3 GB |
| `PUSH_NOTIFICATIONS` | 8 hr | 4M | 0.8KB | 1.33M | 1.1 GB |
| `HRSYNC` | 8 hr | 40K (daily burst) | 0.5KB | 40K | 0.02 GB |
| `MESSAGES_CANONICAL` | 7 day | 5M | 1KB | 35M | 35 GB |
| `INBOX` | 7 day | 1.04M | 0.3KB | 7.3M | 2.2 GB |
| `ROOMS` | 1 day | 1.04M | 0.4KB | 1.04M | 0.4 GB |
| **Subtotal (excl. migration)** | | | | | **~40 GB** |
| `MIGRATION_OPLOG` | 8 hr | 345.6M | 130KB | 115.2M | 14,976 GB |
| **TOTAL (logical, incl. migration)** | | | | | **~15.0 TB** |
| **TOTAL (R=3 on disk)** | | | | | **~45 TB** |

Notes:
- `MIGRATION_OPLOG` dominates total storage (~15 TB logical / ~45 TB at R=3) even at
  only 8 hr retention, purely from 4k QPS × 130KB. Provision dedicated disk for it and
  keep TTL as short as the migration tolerates.
- `MESSAGES_CANONICAL` dominates the *steady-state* footprint (7-day retention × full
  message bodies). It is the canonical source of truth, so retention is intentional —
  but it's the first place to look for disk pressure once migration is excluded.
- `HRSYNC` is a once-daily 40K burst; with an 8 hr TTL the whole batch is retained for
  ~8 hr then expires. Peak ingest is 100 msg/s during the ~7-minute burst.
- For TTL < 1 day, retained ≈ `(pub/day) × (TTL_hours / 24)`.

## 9. Caveats

- **R_member = 50/day/user is an assumption** (§4). All *(member-driven)* lines scale
  with it.
- **Presence is not implemented** ("future"). The estimate assumes ~20 event-driven
  status changes/user/day; a heartbeat design would explode this.
- **OUTBOX and federated INBOX inflow are excluded** from this estimate per scope; INBOX
  here reflects local member events only.
- **PUSH_NOTIFICATIONS / HRSYNC / MIGRATION_OPLOG are pre-merge** — revisit when the
  real specs land.
- **MIGRATION_OPLOG (sustained 4k QPS × 130KB) dwarfs everything** at ~90 TB/day traffic
  and ~15 TB storage. It is modeled as a permanent stream per the stated spec; if it is
  actually a one-off migration window, treat it as transient and exclude it from
  steady-state capacity. Strongly consider isolating it (own stream/account or nodes) so
  it cannot starve live chat traffic.
- **Subscription R/R at 1,000/day/user** is the dominant cost and the assumption most
  worth validating against real client behavior.
- **Peak factor k ≈ 4** assumes 80% of traffic in a 10-hour window with 2× intra-window
  peaking. Figures are first-order; validate against production telemetry.
