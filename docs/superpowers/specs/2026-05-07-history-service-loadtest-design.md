# Extend `tools/loadgen` with read-load workloads for natsrouter services

**Date:** 2026-05-07
**Status:** scoping (this PR proposes the design; implementation lands in a follow-up)
**Phase 2 (added 2026-05-08):** scope expanded to cover every service in the codebase except `auth-service` (HTTP, deferred) and `inbox-worker` (federation, deferred). See "Phase 2 — additional services" near the end of this document.

## Goal

Add read-side load-test scenarios to the existing `tools/loadgen` harness that exercise the **NATS request/reply services built on `pkg/natsrouter`** under sustained load, with the same Prometheus-driven reporting the messaging-pipeline scenario already produces.

Today only two services use natsrouter: **history-service** and **search-service**. Both are read-heavy, request/reply, and share a seed population (users / rooms / subscriptions) with the messaging-pipeline preset, so adding both is a single coherent change with one shared compose stack.

**Phase 2 broadens this** to also cover `room-service` (NATS RPC, non-natsrouter — the chassis is transport-agnostic) plus consumer-lag samplers for the remaining stream-only workers (`room-worker`, `notification-worker`, `search-sync-worker`). The Phase 2 scope is detailed at the bottom of this document.

## Background

`tools/loadgen` (added in [docs/superpowers/specs/2026-04-21-load-test-messaging-workers-design.md](2026-04-21-load-test-messaging-workers-design.md)) is a Go binary with three subcommands (`seed` / `run` / `teardown`) that drives the **messaging pipeline** — `message-gatekeeper → MESSAGES_CANONICAL → message-worker + broadcast-worker`. It opens a NATS connection, seeds users/rooms/subscriptions, fires `SendMessageRequest` events with an open-loop ticker, and tracks reply-correlation, broadcast-correlation, and consumer lag via Prometheus.

What it does NOT cover today: any natsrouter consumer. We extend it with two new read presets that share the existing seed and dispatch infrastructure.

## natsrouter consumers — handler surface

### history-service (`history-service/internal/service/service.go:91-99`)

| Subject (template) | Handler | Storage hit |
|---|---|---|
| `chat.user.{account}.request.msg.history` | `LoadHistory` | Cassandra `messages_by_room` |
| `chat.user.{account}.request.msg.next` | `LoadNextMessages` | Cassandra `messages_by_room` |
| `chat.user.{account}.request.msg.surrounding` | `LoadSurroundingMessages` | Cassandra (fwd + bwd window queries) |
| `chat.user.{account}.request.msg.get` | `GetMessageByID` | Cassandra `messages_by_id` |
| `chat.user.{account}.request.msg.edit` | `EditMessage` | Cassandra write |
| `chat.user.{account}.request.msg.delete` | `DeleteMessage` | Cassandra write |
| `chat.user.{account}.request.msg.thread` | `GetThreadMessages` | Cassandra `messages_by_room` (thread bucket walk) |
| `chat.user.{account}.request.msg.thread-parents` | `GetThreadParentMessages` | MongoDB `thread_rooms` + Cassandra fanout |

Plus a subscription lookup against MongoDB (`subscriptions` collection) for the access-window check on every read.

### search-service (`search-service/handler.go:47-49`)

| Subject (template) | Handler | Storage hit |
|---|---|---|
| `chat.user.{account}.request.search.messages` | `searchMessages` | Elasticsearch `messages-*` index + Valkey restricted-rooms cache |
| `chat.user.{account}.request.search.rooms` | `searchRooms` | Elasticsearch `spotlight` index |

Both handlers consult Valkey for a per-user restricted-rooms cache (TTL = `RESTRICTED_ROOMS_CACHE_TTL`, default 5m); on miss they fall back to a MongoDB lookup.

## Proposed workloads

Open-loop publishers driven by `time.Ticker` (the existing pattern in `tools/loadgen/generator.go`). Two new presets, each with its own request-type mix.

### Preset `history-read`

Per tick:

1. Pick a `(user, room)` pair from the seeded population.
2. Pick a request type from the fixed mix:
   - **60 % `LoadHistory`** — canonical "open the room" path; fetches the most-recent N messages.
   - **20 % `GetMessageByID`** — deep-link / quote-resolve path; single-message lookup.
   - **10 % `LoadSurroundingMessages`** — jump-to-message path; window query.
   - **10 % `GetThreadMessages`** — thread-open path; thread-bucket walk.
   - (Edits / deletes / `LoadNextMessages` / `GetThreadParentMessages` excluded from v1 — mutators interact with the messaging-pipeline scenario; the latter two are minor variants of the four above.)
3. Marshal the typed request struct (`model.LoadHistoryRequest`, `model.GetMessageByIDRequest`, etc. — already public).
4. `nc.Request(subject, payload, deadline)`. Record latency in a per-handler histogram. Track success / 4xx-style validation errors / timeout / NATS error.

### Preset `search-read`

Per tick:

1. Pick a `user` from the seeded population.
2. Pick a request type from the fixed mix:
   - **50 % `searchMessages`** — full-text query against the messages index.
   - **50 % `searchRooms`** — spotlight room-name query.
3. Build the request from a small bag of `searchText` tokens drawn from the seeded message corpus (so hits are realistic, not all empty). For `searchRooms`, randomise `Scope` across `"all"` / `"channel"` / `"dm"` proportional to seeded room mix.
4. `nc.Request(subject, payload, deadline)`. Same metric/error classification as `history-read`.

### Open-loop pacing

Target rate is `RPS` per second; if the consumer can't keep up, requests queue (which surfaces as latency p99 climbing). Same pattern as the existing generator — no closed-loop wait.

## Required additions

### Code (under `tools/loadgen/`)

| File | Change |
|---|---|
| `preset.go` | Two new built-in presets: `history-read` (60/20/10/10) and `search-read` (50/50). |
| `generator.go` | Two new generator types — `historyReadGenerator` and `searchReadGenerator` — selected by a `--scenario={messaging-pipeline,history-read,search-read}` flag. |
| `metrics.go` | Two new per-handler latency histograms: `loadgen_history_request_duration_seconds{handler,status}` and `loadgen_search_request_duration_seconds{handler,status}`. Reuse existing histogram buckets. |
| `report.go` | "History Read" and "Search Read" terminal-summary sections: per-handler p50/p95/p99 latency, success rate, RPS achieved, error counts. CSV export gains the same columns. |
| `integration_test.go` | New `//go:build integration` cases that spin up real NATS + Mongo + Cassandra + Elasticsearch + history-service + search-service + search-sync-worker (+ Valkey), seed via the messaging-pipeline preset for 30s, run each new preset for 5s, assert non-empty samples + zero error rate. |
| `README.md` | Document the new presets, request mixes, recommended seed sizes, and the "warm up first" workflow. |

### Deploy

`tools/loadgen/deploy/docker-compose.loadtest.yml` gains the dependencies neither preset has today:

- `cassandra:4.1.3`
- `elasticsearch:8.x` (per `pkg/testutil/testimages`)
- `valkey:8`
- `history-service` (built from repo root)
- `search-service` (built from repo root)
- `search-sync-worker` (needed so search indexes are populated as the messaging-pipeline warm-up runs — without it the Elasticsearch indexes stay empty and `search-read` returns zero hits)

`tools/loadgen/deploy/grafana/dashboards/loadtest.json`: add panels per preset (latency p50/p95/p99 over time, RPS counter, error-rate gauge), reusing the existing dashboard structure.

### Seed data — warm up via messaging-pipeline

Both new presets need realistic backing state:

- history-service: populated rows in Cassandra `messages_by_room` / `messages_by_id`.
- search-service: populated documents in Elasticsearch `messages-*` and `spotlight`.

Both populate naturally as a side-effect of running the messaging-pipeline preset (message-worker writes Cassandra; search-sync-worker mirrors MESSAGES_CANONICAL into Elasticsearch). Decision: **reuse the messaging-pipeline preset as a warm-up rather than adding dedicated seed paths.**

Documented run order:

```
loadgen seed                                         # users/rooms/subscriptions in Mongo
loadgen run --scenario=messaging-pipeline --duration=60s   # warm up Cassandra + ES
loadgen run --scenario=history-read --duration=2m
loadgen run --scenario=search-read   --duration=2m
```

Zero new seed code; realistic data shape; a single `make -C tools/loadgen/deploy run-natsrouter` target wraps the four steps for ergonomics.

If, in a later iteration, isolating either service from worker performance becomes interesting, a `seed --history` / `seed --search` direct-write path can land then. Not in v1.

## Out of scope (this PR / first cut)

- Write-side history-service handlers (EditMessage, DeleteMessage). Mutate state; revisit when write-load matters.
- Federation / cross-site read patterns (both services are per-site).
- Connection-pool / keepalive tuning sweeps. The existing `MAX_IN_FLIGHT` knob already covers concurrency.
- Per-user rate limiting on the loadgen side (open-loop publishing is the design choice).
- gRPC / HTTP probes — both services are NATS-only.
- Comparing natsrouter vs. raw `nc.QueueSubscribe` services (the original "how much faster" question). The other request/reply services in the codebase use raw subscribe; an apples-to-apples comparison would require a parallel non-natsrouter fork of one of these handlers, which is out of proportion to the value.
- A dedicated seed-via-direct-write path for either service (warm-up via messaging-pipeline is sufficient).

## Risk and reversibility

- Pure additive: existing messaging-pipeline preset is unchanged.
- Compose stack grows substantially (Cassandra + Elasticsearch + Valkey + 3 services). Local-only — production untouched. Disk + memory footprint of the compose run roughly doubles; called out in the README.
- Failure mode: a buggy generator floods either service. The existing `MAX_IN_FLIGHT` cap bounds in-flight requests; same protection as the messaging-pipeline scenario. natsrouter's own `WithMaxConcurrency` (when configured) provides a server-side ceiling that returns `ErrUnavailable` rather than collapsing under load — the load test will surface that ceiling clearly via the `status="busy"` counter.

## Implementation plan (follow-up PR)

If the design above is accepted, the implementation breaks into ~10 TDD tasks; see the companion plan at [`docs/superpowers/plans/2026-05-07-natsrouter-services-loadtest.md`](../plans/2026-05-07-natsrouter-services-loadtest.md).

Each lands as a TDD task per the codebase's spec → plan → implementation pattern.

## Decisions baked in (no further input required)

- Request-type mix: history `60/20/10/10`, search `50/50`. Tunable later; these match the most-likely real traffic shape.
- Warm-up via messaging-pipeline (option A from the previous draft). No dedicated `seed --history` / `seed --search` flag in v1.
- Out-of-scope list as above; nothing pulled back in.

---

## Phase 2 — additional services

**Status:** added 2026-05-08, in scope for the same PR as Phase 1. Chassis built in Tasks 1-5 (Preset, Requester, picker, sibling generators, `--scenario` flag) is reused as-is; no architectural changes.

**Scope rule.** Every service in the repo except `auth-service` (HTTP, deferred) and `inbox-worker` (federation, deferred). Both deferred services are tracked in their own follow-up specs.

### Service coverage matrix (post Phase 2)

| Service | Driver | Coverage | Mechanism |
|---|---|---|---|
| `history-service` | client RPC | Phase 1 scenario | `--scenario=history-read` |
| `search-service` | client RPC | Phase 1 scenario | `--scenario=search-read` |
| `message-gatekeeper` | client publish | Phase 0 (already covered) | `messaging-pipeline` `InjectFrontdoor` |
| `message-worker` | downstream of canonical | Phase 0 (already covered) | indirect + existing `ConsumerSampler` |
| `broadcast-worker` | downstream of canonical | Phase 0 (already covered) | indirect + existing `ConsumerSampler` |
| `room-service` | client RPC | **Phase 2 scenario** | `--scenario=room-rpc` |
| `room-worker` | downstream of room-service | **Phase 2 sampler** | `ConsumerSampler` only |
| `notification-worker` | downstream of canonical | **Phase 2 sampler** | `ConsumerSampler` only |
| `search-sync-worker` | downstream of canonical + INBOX | **Phase 2 samplers (×3)** | `ConsumerSampler` ×3 |
| `inbox-worker` | federation INBOX | **out of scope (deferred)** | needs synthetic federation driver |
| `auth-service` | client HTTP | **out of scope (deferred)** | needs HTTP transport in loadgen |

### `room-service` — new scenario `room-rpc`

**Transport.** NATS request/reply (queue group). Client-driven. **Not** natsrouter — uses bare `nc.QueueSubscribe`. Chassis is transport-agnostic; the new scenario plugs in identically to history-read / search-read.

**Handler surface to load-test.** Subjects below already exist in `pkg/subject` (no new builders needed except payload marshalling).

| Subject | `pkg/subject` builder | Request type | Response type | Default weight |
|---|---|---|---|---|
| `chat.user.{account}.request.rooms.list` | `RoomsList` | empty | `model.ListRoomsResponse` | 60 |
| `chat.user.{account}.request.rooms.get.{roomID}` | `RoomsGet` | empty | `model.Room` | 20 |
| `chat.user.{account}.request.room.{roomID}.{siteID}.member.list` | `MemberList` | `model.ListRoomMembersRequest` | `model.ListRoomMembersResponse` | 10 |
| `chat.user.{account}.request.room.{siteID}.create` | `RoomCreate` | `model.CreateRoomRequest` | `model.Room` | 8 |
| `chat.user.{account}.request.room.{roomID}.{siteID}.member.add` | `MemberAdd` | `model.AddMembersRequest` | ack (async) | 2 |

**Mix rationale.** Read-heavy (90% list/get/member-list) reflecting real usage. Writes (create/member-add) capped at 10% so the test exercises the write path under load without dominating. Tunable per-preset.

**What to build.**

| File | Change |
|---|---|
| `preset.go` | Add `RoomMix map[roomRequestKind]int` to `Preset`; new `roomRequestKind` enum; new `room-rpc` preset entry. |
| `preset.go` | Add `pickRoomKind` (1-line wrapper around the existing generic `pickWeighted`). |
| `request_builder_room.go` (new) | `buildRoomRequest(kind, args) → (subject, body, error)`, paralleling `buildHistoryRequest` / `buildSearchRequest`. |
| `room_generator.go` (new) | `RoomRPCGenerator` sibling to `HistoryReadGenerator` — same tick + bounded-pool dispatch. ~150 LoC. |
| `main.go` | Add `room-rpc` to the `--scenario` switch. |

**Metrics.** Reuses the new `loadgen_requests_total` and `loadgen_request_errors_total` (added in Task 5 of Phase 1) with `scenario="room"` label. No new histograms beyond what Task 6 lands.

**Decision: write-path inclusion.** Including `MemberAdd` and `RoomCreate` in the mix mutates state during the run. Mitigations:
- Test runs against a dedicated `tools/loadgen` Mongo database — `make teardown` drops it at the end.
- Generated room/member IDs use the `loadgen-` prefix so they're clearly identifiable in any forensic check.
- Asymmetric mix (only 10% writes) keeps state growth bounded over a 2-minute run.
