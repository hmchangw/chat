# Extend `tools/loadgen` with read-load workloads for natsrouter services

**Date:** 2026-05-07
**Status:** scoping (this PR proposes the design; implementation lands in a follow-up)
**Phase 2 (added 2026-05-08):** scope expanded to cover every service in the codebase except `auth-service` (HTTP, deferred) and `inbox-worker` (federation, deferred). See "Phase 2 — additional services" near the end of this document.
**Phase 3 (added 2026-05-08):** harness-level upgrades — auto warm-up, live progress reporting, service readiness probes, rate ramping, saturation auto-detect, per-tenant connection multiplexing. See "Phase 3 — harness upgrades" at the bottom of this document. Chaos-engineering toggles considered and explicitly deferred.

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

**Phase 1 (history + search):**
- Write-side history-service handlers (EditMessage, DeleteMessage). Mutate state; revisit when write-load matters.
- Federation / cross-site read patterns (both services are per-site).
- Connection-pool / keepalive tuning sweeps. The existing `MAX_IN_FLIGHT` knob already covers concurrency.
- Per-user rate limiting on the loadgen side (open-loop publishing is the design choice).
- gRPC / HTTP probes — both services are NATS-only.
- Comparing natsrouter vs. raw `nc.QueueSubscribe` services (the original "how much faster" question). The other request/reply services in the codebase use raw subscribe; an apples-to-apples comparison would require a parallel non-natsrouter fork of one of these handlers, which is out of proportion to the value.
- A dedicated seed-via-direct-write path for either service (warm-up via messaging-pipeline is sufficient).

**Phase 2 (additional services):**
- `auth-service` — HTTP transport. Loadgen has no HTTP scenario type today. Adding it requires a `Requester` analogue over Resty, separate metrics labels for HTTP status codes, and an entirely new test fixture path (sign-in flow with synthetic SSO tokens). Tracked in a follow-up spec.
- `inbox-worker` — federation INBOX consumer. Driving it requires a synthetic `OutboxEvent` publisher targeting `outbox.{srcSite}.to.{siteID}.{eventType}` plus INBOX-stream `Sources`/`SubjectTransforms` config. Substantially more involved than the other Phase 2 work and benefits from being scoped on its own. Tracked in a follow-up spec.
- Per-service write-path mutation budgets. The `room-rpc` mix caps writes at 10%; tighter budgeting (per-room write quotas, write-batch teardown) is left for when concrete data shows it's needed.

## Risk and reversibility

**Phase 1:**
- Pure additive: existing messaging-pipeline preset is unchanged.
- Compose stack grows substantially (Cassandra + Elasticsearch + Valkey + 3 services). Local-only — production untouched. Disk + memory footprint of the compose run roughly doubles; called out in the README.
- Failure mode: a buggy generator floods either service. The existing `MAX_IN_FLIGHT` cap bounds in-flight requests; same protection as the messaging-pipeline scenario. natsrouter's own `WithMaxConcurrency` (when configured) provides a server-side ceiling that returns `ErrUnavailable` rather than collapsing under load — the load test will surface that ceiling clearly via the `status="busy"` counter.

**Phase 2 incremental risk:**
- `room-rpc` mutates state (room creates, member adds). Mitigated by run-time isolation (`MONGO_DB=loadgen` + `make teardown`) and by capping writes at 10% of the mix. Worst case: a long run leaves an inflated `rooms` collection in the loadgen Mongo DB until teardown runs.
- Compose stack grows by **one more service**: `room-service`. Already in the existing `docker-local` stack so the Dockerfile / wiring exists; just needs to be added to `tools/loadgen/deploy/docker-compose.loadtest.yml`.
- Sampler additions are zero-risk: gauges that read 0 when the consumer isn't being driven. No effect on existing scenarios.
- Phase 2 increases CI integration-test wallclock by ~30s (room-service startup) at most. If that becomes a problem, gate Phase 2 scenarios behind the same `LOADGEN_NATSROUTER=1` env-var hatch already proposed for Phase 1 in the plan's Risk section.

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

### Sampler-only services — `room-worker`, `notification-worker`, `search-sync-worker`

These three services are stream-only consumers with no client-facing surface. They process events that the existing scenarios already produce:

- `room-worker` consumes `ROOMS_{siteID}` — driven by the new `room-rpc` scenario above.
- `notification-worker` consumes `MESSAGES_CANONICAL_{siteID}` — driven by the existing `messaging-pipeline` scenario.
- `search-sync-worker` consumes `MESSAGES_CANONICAL_{siteID}` and `INBOX_{siteID}` across **three durables** — driven by `messaging-pipeline`.

So no new generator types are needed. What's missing is **visibility**: their consumer-lag metrics aren't in the loadgen scrape. The fix is one `ConsumerSampler` per durable, registered in `main.go` next to the existing `message-worker` / `broadcast-worker` samplers.

**Sampler additions per service.**

| Service | Stream | Durable name(s) | Driven by |
|---|---|---|---|
| `room-worker` | `ROOMS_{siteID}` | `room-worker` | `room-rpc` |
| `notification-worker` | `MESSAGES_CANONICAL_{siteID}` | `notification-worker` | `messaging-pipeline` |
| `search-sync-worker` (msgs) | `MESSAGES_CANONICAL_{siteID}` | `search-sync-worker-messages` | `messaging-pipeline` |
| `search-sync-worker` (spotlight) | `INBOX_{siteID}` | `search-sync-worker-spotlight` | `messaging-pipeline` (member events) |
| `search-sync-worker` (user-room) | `INBOX_{siteID}` | `search-sync-worker-user-room` | `messaging-pipeline` (member events) |

**What to build.** A single edit to `main.go` extending the `samplers := []*ConsumerSampler{...}` slice with the five new entries. Each sampler reuses the existing `NewConsumerSampler` constructor — no new code paths. The terminal report's "Consumers" section auto-includes them.

**Activation.** The samplers are scenario-aware in spirit: a `messaging-pipeline` run will see meaningful values for the four MESSAGES_CANONICAL/INBOX-fed durables but zero values for `room-worker` (no driver). A `room-rpc` run will see the inverse. The loadgen run command should always register all of them — empty-stream gauges read 0 cleanly, and toggling them per-scenario adds switch-case noise without value.

### Phase 2 dependency on Phase 1 work

Phase 2 is purely additive on the chassis built in Phase 1 Tasks 1-5:

- `Preset` struct already extensible (`HistoryMix`, `SearchMix` precedents).
- `Requester` interface already exists; `room-rpc` reuses it unchanged.
- `pickWeighted` is already generic (`K ~int`); `pickRoomKind` is a 1-line wrapper.
- Tick + bounded-pool dispatch model already proven by `HistoryReadGenerator` / `SearchReadGenerator`; `RoomRPCGenerator` copies it.
- `--scenario` flag already in `main.go`; new value is one switch arm.
- Metrics labels (`scenario`, `kind`) already designed to admit any scenario.

**No structural changes from Phase 1 are required for Phase 2.**

---

## Phase 3 — harness upgrades

**Status:** added 2026-05-08, in scope for the same PR. Six harness-level features that close the rough edges in Phase 1 + Phase 2: auto warm-up, live progress reporting, readiness probes, rate ramping, saturation auto-detect, and per-tenant connection multiplexing. Chaos-engineering toggles considered (drop-replies / malformed / burst / disconnect) and dropped from v1 by explicit decision.

### Why these six

Phases 1-2 build the chassis (presets, generators, scenarios, metrics, scripts). Running them on a fresh laptop today exposes the same failure modes every time:

1. **Read scenarios start cold** — `history-read` fires `GetMessageByID` against an empty Cassandra and gets all errors, because nobody seeded message IDs.
2. **Long runs feel dead** — no output between `loadgen run` and final summary; you can't tell at minute 4 whether minute 5 will be useful.
3. **First-second errors** — services aren't ready when the generator starts; the first ~500ms of samples are publish errors that contaminate the warmup window.
4. **Fixed-rate runs are blunt** — to find the knee you re-run at 100, 200, 500, 1000 rps. Slow and imprecise.
5. **Capacity-finding requires a babysitter** — a long run that's clearly saturated keeps generating millions of error samples until duration expires.
6. **Single connection** — every loadgen instance opens one NATS connection regardless of `--rate`. Real client populations open thousands.

Each upgrade below addresses one of these.

### 3.1 Auto warm-up for read scenarios

**Problem.** `history-read`'s `GetMessageByID` / `LoadSurroundingMessages` / `GetThreadMessages` request kinds need a pool of real message IDs that exist in Cassandra. Today the pool is `nil` so those kinds fire with empty IDs and fail with `no_message_ids`. Only `LoadHistory` works without warm-up.

**Solution.** A `--auto-warmup-rate=N` flag (default 200 rps). When the selected scenario is `history-read` *and* the configured `HistoryMix` includes any kind that needs an ID, loadgen runs an internal `messaging-pipeline` phase first for the duration of `--warmup`, captures the published message IDs from the existing `Collector.byMsgID` map, and hands the resulting `[]string` to the `HistoryReadGenerator`. The phase is transparent to the user — same `--warmup` window, same final summary, just a populated pool.

**Wire-up.**
- New `Collector.MessageIDs() []string` method exposing `keys(byMsgID)` after the warmup phase.
- New `runHistoryReadWithWarmup(ctx, ...)` helper in `main.go` that orchestrates two `Run()` calls back-to-back.
- `--auto-warmup=false` opt-out for the rare case the user already populated Cassandra externally.

**Doesn't apply to.** `search-read` (warm-up via messaging-pipeline already populates Elasticsearch via search-sync-worker — same code path Phase 1 already uses). `room-rpc` (no message IDs needed).

### 3.2 Live progress reporting

**Problem.** Today `loadgen run --duration=5m` prints nothing for 5 minutes, then dumps the summary. There's no way to tell mid-run if the test is healthy.

**Solution.** Every 10 seconds (configurable via `--progress-interval`), emit one structured-log line:

```
{"level":"info","time":"...","msg":"progress",
 "elapsed":"30s","remaining":"4m30s",
 "rate":498.2,"target":500,
 "p50_ms":4.1,"p95_ms":18,"p99_ms":42,
 "errors":3,"saturated":0}
```

**Implementation.**
- A `progress.go` goroutine started by the run command; reads `loadgen_*` Prometheus counters/histograms via `Registry.Gather()` (same pattern as `gatheredCounterValue` in `main.go`).
- Computes deltas vs. the previous tick so reported rate is instantaneous, not lifetime.
- Stops when the run context cancels.
- Output goes through the existing `slog` JSON handler for grep-friendliness.

### 3.3 Service readiness probes

**Problem.** When `loadgen run` starts immediately after `compose up`, the first ~500ms of publishes hit services that haven't finished initialising. These show up as `bad_reply` / `gatekeeper` errors and contaminate even the post-warmup measured window if warmup is short.

**Solution.** Before the generator starts, probe the target services. The probe shape varies by scenario:

| Scenario | Probe |
|---|---|
| `messaging-pipeline` | `nc.RequestWithContext(subject.RoomsList(probeUser), {}, 2s)` — bounces off room-service. |
| `history-read` | `nc.RequestWithContext(subject.MsgHistory(probeUser, probeRoom, siteID), {}, 2s)` — bounces off history-service. |
| `search-read` | `nc.RequestWithContext(subject.SearchMessages(probeUser), {searchText:"probe"}, 2s)` — bounces off search-service. |
| `room-rpc` | same as `messaging-pipeline`. |

Loop with exponential backoff (start 200ms, max 2s) for up to 30s. Exit non-zero if the probe never succeeds — that's a stack-misconfig signal worth surfacing.

**Implementation.**
- New `readiness.go` with a `WaitForReady(ctx, scenario, nc, fixtures)` function.
- Called from `runRun` before the generator's `Run`. No-op if `--skip-readiness` is set (escape hatch for tests where the probe traffic itself is unwanted).

### 3.4 Rate ramping

**Problem.** Finding the capacity ceiling today means running at 100, 200, 500, 1000 rps in separate runs and eyeballing the latency curve. Slow, imprecise, samples scattered across CSVs.

**Solution.** New flags:

```
--ramp-from=10        # start rate (rps)
--ramp-to=1000        # end rate (rps)
--ramp-duration=2m    # time to climb from --ramp-from to --ramp-to
--ramp-shape=linear   # linear|exponential
```

When `--ramp-from`/`--ramp-to` are non-zero they override `--rate` (which becomes the steady rate AFTER the ramp finishes; `0` means "stop after the ramp"). The generator's tick interval is recomputed every second based on a curve function `currentRate(t) → rps`. Each published sample is labelled with the rate it was published at: `loadgen_published_total{phase="ramp",rate_bucket="100-200"}`.

**Why a curve, not multiple runs.** A single ramped run produces one continuous latency series, so the knee is visible directly in Grafana as the point where p99 starts climbing exponentially. Far better signal than discrete-rate runs.

**Implementation.**
- `pkg/loadgen/ramp.go` (or sibling file) with a `Ramp` struct + `func (r *Ramp) RateAt(t time.Duration) int` method. Pure function, easy to unit-test.
- `Generator.Run` reads `r.RateAt(time.Since(start))` once per second and rebuilds the ticker. Cheap; `time.Ticker.Reset(interval)` is the standard pattern.
- `--ramp-shape=exponential` uses `rps = from * (to/from)^(t/dur)`. Useful when the linear ramp blasts past the knee in too few samples.

### 3.5 Saturation auto-detect & abort

**Problem.** A capacity-finding run that's clearly past saturation (p99 > 5s, 50% errors) keeps generating millions of useless samples until `--duration` expires. Wastes time and mucks up averages.

**Solution.** Two flags:

```
--abort-on-p99-ms=2000   # if p99 > 2s sustained for 30s, stop and report "saturated at X rps"
--abort-on-error-pct=10  # if error rate > 10% sustained for 10s, same
```

When triggered, the generator stops, the report tags the run as `status="saturated"`, and exit code is 2 (distinguishable from clean-fail or clean-pass).

**Implementation.**
- A goroutine alongside the progress reporter (§3.2) that consumes the same metrics. Maintains a sliding window (last 30s of samples) and checks both thresholds.
- On trigger, calls `cancelRun()` (the existing context cancellation hook); the generator's drain logic does the rest.
- The "sustained" requirement is important — momentary spikes are normal, what matters is sustained degradation.

**Pairs naturally with §3.4.** A ramped run with `--abort-on-p99-ms=1000` walks the rate curve until the knee, then stops. The final summary reports the rate at which the threshold was breached — that IS the capacity number.

### 3.6 Per-tenant connection multiplexing

**Problem.** Today loadgen opens **one** NATS connection regardless of `--rate` or fixture count. Real client populations open thousands. NATS resource limits (subscription tables, slow-consumer detection, per-connection memory) only become visible when there's connection-level fan-out.

**Solution.** A `--connections=N` flag (default 1, matching today's behavior). When `> 1`, loadgen:

1. Opens N NATS connections at startup, all sharing the same NKeys/JWT credentials.
2. Partitions the user fixtures across them (`user_i → connection_i mod N`).
3. Each generator tick picks a user, looks up that user's connection, and publishes/requests via that connection.
4. Reply / broadcast subscriptions remain on a single shared "observer" connection — no need to fan-out the reply path; the observation is loadgen-side, not server-side.

**What this surfaces.**
- Server-side per-connection state (NATS subscription table sizes, slow-consumer disconnects).
- Connection-pool exhaustion paths in services that open per-request resources.
- More realistic queue-group distribution: with one connection, every published message goes to the same queue-group member; with N connections, the distribution is closer to real-world.

**What this does NOT do.**
- It doesn't simulate per-user authentication. All connections share the same JWT. That's a separate (deferred) feature; today even production-realistic loadgen would re-use a service-level JWT.
- It doesn't simulate per-user reply subscriptions. The shared observer connection sees all replies; in production each client connection has its own reply mailbox. That gap is acceptable for capacity testing — fixing it would require N reply-correlation maps and 2N connections, more complexity than value for v1.

**Implementation.**
- New `connpool.go` with a `ConnPool` type carrying `[]*nats.Conn` plus a `For(userID string) *nats.Conn` selector.
- `natsCorePublisher` and `natsRequester` take a `ConnPool` instead of a single `*nats.Conn`. Choosing the connection per-call is a hash + index, not a round-robin (so the same user always uses the same connection — easier to interpret).
- The shared observer connection lives on `ConnPool.Observer()`, used by the E1/E2 reply subscribers and consumer samplers.
- All N data connections drained on shutdown; observer drained last.

**Default stays at 1** so existing scripts and the default smoke run are unchanged. Bumping `--connections=10` is a deliberate "I want connection-level fan-out" decision.
