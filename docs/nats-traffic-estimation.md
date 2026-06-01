# NATS Traffic Estimation

> Sizing model for a single site's NATS/JetStream traffic. Inputs are parameterized
> so the model can be re-run when assumptions change. All figures are estimates for
> capacity planning, not measured production telemetry.

## 1. Scope

This document estimates NATS message rates, bandwidth, and connection-state load for
one independent site. It covers:

- The core JetStream streams and request/reply (R/R) endpoints that carry traffic.
- Per-payload size references used in the estimate.
- A traffic model for a **single connection per user** (baseline).
- A traffic model for **multiple concurrent connections per user** (desktop + mobile +
  web tabs).

Each site runs its own NATS server, so these figures are per-site. Cross-site
federation (OUTBOX/INBOX) is included but is a minor term at the assumed scale.

## 2. Stream & Endpoint Inventory

### 2.1 JetStream streams

| Stream | Subject | Producer | Consumer(s) |
|--------|---------|----------|-------------|
| `MESSAGES_{siteID}` | `chat.user.*.room.*.{siteID}.msg.>` | Client | message-gatekeeper |
| `MESSAGES_CANONICAL_{siteID}` | `chat.msg.canonical.{siteID}.>` | message-gatekeeper | message-worker, broadcast-worker, notification-worker, search-sync-worker |
| `ROOMS_{siteID}` | `chat.user.*.request.room.*.{siteID}.member.>` | room-service | room-worker |
| `OUTBOX_{siteID}` | `outbox.{siteID}.to.{destSiteID}.{eventType}` | room-worker (member_added/removed, role_updated), room-service (subscription_read, thread_read, subscription_mute_toggled), message-worker (thread_subscription_upserted) | Remote site INBOX |
| `INBOX_{siteID}` | `chat.inbox.{siteID}.*` + `…aggregate.>` | Remote OUTBOX (JetStream-sourced) | inbox-worker, search-sync-worker |

INBOX is a JetStream-sourced mirror of remote OUTBOX streams, so OUTBOX and INBOX carry
identical wire payloads.

### 2.2 Core NATS delivery subjects (server → client)

| Subject | Publisher | Purpose |
|---------|-----------|---------|
| `chat.room.{roomID}.stream.msg` | broadcast-worker | Group room message delivery |
| `chat.user.{account}.stream.msg` | broadcast-worker | DM message delivery |
| `chat.room.{roomID}.event.metadata.update` | broadcast-worker | Room metadata (`lastMessageAt`, etc.) — emitted per message |
| `chat.user.{account}.notification` | notification-worker | Desktop banner (mentions/DMs) |
| `chat.user.{account}.event.subscription.update` | room-worker, inbox-worker | Room added/removed |
| `chat.room.{roomID}.event.typing` | room-service (relay) | Typing indicator (active room only) |
| `chat.user.{account}.event.presence` | Presence (future) / client heartbeat | Online/offline/away |

### 2.3 Request/Reply endpoints

| Group | Subject | Heaviest response |
|-------|---------|-------------------|
| Message history | `…room.{roomID}.{siteID}.msg.{history,next,surrounding,thread}` | `LoadHistoryResponse{Messages[]}` |
| Subscription lists | `…subscription.{getCurrent,getRooms,getChannels,getApps,…}` | `subscriptionListResp{Subscriptions[]}` |
| Room | `…rooms.create`, `chat.server.request.room.{siteID}.info.batch`, `…member.list` | `RoomsInfoBatchResponse` / `ListRoomMembersResponse` |

## 3. Payload Size Reference

| Category | Streams / Endpoints | avg | max |
|----------|---------------------|-----|-----|
| Message JetStream | MESSAGES, MESSAGES_CANONICAL | 500B–1.5KB | ~20KB |
| Room JetStream | ROOMS | 200–400B | ~5KB |
| Federation (OUTBOX = INBOX) | OUTBOX, INBOX | 200–400B | ~5KB |
| Message R/R | history, search-messages | 15–50KB | 100KB+ |
| Room R/R | RoomsInfoBatch, CreateRoom, member.list | 2–20KB | ~65KB |
| Subscription R/R | subscription.get*, member-list | 100–300KB | ~700KB |

Notes:
- `OutboxEvent.Payload` is `[]byte`, so JSON base64-encodes the inner event (~33%
  overhead on the envelope). member_removed is one account per event; member_added is a
  per-destination-site subset of the add batch — both share the ROOMS bound.
- Metadata-update payloads are small (~0.3KB) but are emitted **once per message**.

## 4. Parameters (locked inputs)

| Parameter | Symbol | Value |
|-----------|--------|-------|
| Users | U | 20,789 |
| Subjects subscribed per connection | S | 100 (user + room) |
| Presence subjects watched per connection | P | 100 |
| Messages per day (human + bot) | M | 4,000,000 |
| Recipients per room (fan-out) | F | 100 |
| Subscription R/R ops per day per connection | R_sub | 150 |
| Message-history R/R ops per day per connection | R_hist | ~75 |
| Presence status changes per day per user | C_pres | ~20 |
| Room ops per day per user | R_room | 10 |
| Peak factor (business-hours concentration) | k | ~4× |

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
**~2×F core deliveries** (room stream + metadata, fanned out by NATS to F subscribers).

The broadcast-worker publishes once per subject; the multiplication happens at the NATS
delivery layer, scaling with the number of subscribers.

## 6. Single-Connection Baseline

One NATS connection per user. Peak ≈ avg × k.

| Source | Formula | Deliveries/day | avg msg/s | Payload | Bytes/day |
|--------|---------|---------------:|----------:|---------|----------:|
| Message ingress (pub) | M | 4M | 46 | 1KB | 4 GB |
| JetStream pipeline | M × 7 | 28M | 324 | 1KB | 28 GB |
| Message delivery (room stream) | M × F | 400M | 4,630 | 1KB | 400 GB |
| Metadata update delivery | M × F | 400M | 4,630 | 0.3KB | 120 GB |
| Notifications | M × ~10% | 0.4M | 5 | 1KB | 0.4 GB |
| Room events + 2° fan-out | R_room × U × ~4 | ~0.8M | ~10 | 0.3–1KB | <1 GB |
| Presence delivery | U × C_pres × P | ~42M | ~480 | 0.15KB | 6 GB |
| Subscription R/R (responses) | R_sub × U | 3.1M | 36 | 150KB | 468 GB |
| Message history R/R | R_hist × U | 1.6M | 18 | 30KB | 47 GB |
| Typing | active room only | — | — | — | *(ignored, negligible)* |
| **TOTAL** | | **~870M/day** | **~10,000/s avg · ~40,000/s peak** | | **~1.07 TB/day** |

Connection state: ~2.08M live message/room subscriptions + ~2.08M presence
subscriptions = **~4.2M subscription interests** across 20,789 connections.

### 6.1 Bottlenecks

- **Message + metadata delivery = ~800M deliveries/day (~9,300/s)** — the
  message-rate / connection bottleneck, driven entirely by F = 100.
- **Subscription R/R = ~468 GB/day** — the bandwidth bottleneck, despite only 36 ops/s,
  because each response is 150KB. It rivals all message-delivery bytes combined.

### 6.2 Optimization levers

- **Coalesce metadata updates** (e.g., max 1/sec/room) → halves the dominant delivery
  term (removes the second `M × F` row).
- **Lighter subscription endpoint** (delta/paginated instead of full 150KB list) →
  attacks the bandwidth bottleneck on payload, not count.

## 7. Multiple Connections Per User

Real users connect from several clients at once — e.g. **1 desktop app + 1 mobile +
3 web tabs = 5 concurrent connections** (device multiplier `D = 5`). Each connection is
an independent NATS subscriber that re-subscribes to the same subjects and fetches its
own state on connect.

### 7.1 What scales with D and what stays flat

| Scales linearly with D | Stays flat (independent of D) |
|------------------------|-------------------------------|
| Message delivery (room stream) | Message ingress (user sends from one client) |
| Metadata-update delivery | JetStream pipeline (server-side, driven by ingress) |
| Notification delivery | broadcast-worker publish count (fan-out is at NATS layer) |
| Presence delivery | Presence/room event **publishes** (per-user state) |
| Subscription & history R/R (each connection fetches independently) | |
| Connections & subscription interests | |

Rule of thumb: **ingress and server-side processing are flat; everything on the
delivery/egress side and all connection state scale with D.** Effective fan-out becomes
`F × D = 100 × 5 = 500` deliveries per message.

### 7.2 Single connection (D=1) vs. 5 connections (D=5)

| Source | D=1 deliveries/day | D=5 deliveries/day | D=1 bytes/day | D=5 bytes/day |
|--------|-------------------:|-------------------:|--------------:|--------------:|
| Message ingress | 4M | 4M | 4 GB | 4 GB |
| JetStream pipeline | 28M | 28M | 28 GB | 28 GB |
| Message delivery (room stream) | 400M | 2,000M | 400 GB | 2,000 GB |
| Metadata update delivery | 400M | 2,000M | 120 GB | 600 GB |
| Notifications | 0.4M | 2M | 0.4 GB | 2 GB |
| Presence delivery | 42M | 210M | 6 GB | 30 GB |
| Subscription R/R | 3.1M | 15.6M | 468 GB | 2,340 GB |
| Message history R/R | 1.6M | 7.8M | 47 GB | 235 GB |
| **TOTAL deliveries** | **~870M/day** | **~4.26B/day** | | |
| **avg / peak msg/s** | **~10k / ~40k** | **~49k / ~197k** | | |
| **TOTAL bytes/day** | | | **~1.07 TB** | **~5.2 TB** |

Connection state at D=5: **~104k connections** and **~20.8M subscription interests**
(message/room + presence).

### 7.3 Takeaways for multi-device

- Traffic scales **~linearly with D** on the delivery side (~4.9× from D=1 to D=5),
  because ingress (the only flat term) is a small fraction of the total.
- **Subscription R/R becomes the single largest line** at D=5 (~2.34 TB/day), since each
  of the 5 connections independently fetches the 150KB list. This makes the lighter
  subscription endpoint (§6.2) the highest-value optimization for multi-device fleets.
- **Reconnect storms multiply by D**: a site restart triggers up to `U × D ≈ 104k`
  simultaneous subscription-list fetches of 150KB each — a thundering-herd risk worth
  rate-limiting or jittering.

## 8. Caveats

- **Presence is not implemented** (marked "future"). The estimate assumes event-driven
  status changes (~20/user/day). If presence becomes **heartbeat-based** (e.g., every
  30s while online), it explodes to billions of deliveries/day and becomes a top-3
  source — re-run the model once the design is fixed.
- **Peak factor k ≈ 4** assumes 80% of daily traffic in a 10-hour window with 2×
  intra-window peaking. Adjust for sharper login spikes.
- **Subscription R/R per-connection assumption**: clients that share state across tabs
  (service worker / shared cache) would fetch fewer than `R_sub × D` times. The model
  takes the conservative per-connection view.
- Figures are first-order estimates. Validate against production telemetry before
  hardware procurement.
