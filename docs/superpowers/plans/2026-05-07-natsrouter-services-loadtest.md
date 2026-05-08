# Plan: extend `tools/loadgen` with read-load presets for natsrouter services

**Date:** 2026-05-07
**Spec:** [`docs/superpowers/specs/2026-05-07-history-service-loadtest-design.md`](../specs/2026-05-07-history-service-loadtest-design.md)
**Status:** ready (lands as a separate PR stacked on the spec PR once approved)
**Phase 2 (added 2026-05-08):** Tasks 11-15 added at the bottom; same PR. Cover `room-service` RPC scenario + sampler-only services (`room-worker`, `notification-worker`, `search-sync-worker`). `auth-service` and `inbox-worker` deferred to follow-up specs.

## Context

Two NATS request/reply services are built on `pkg/natsrouter` today: history-service and search-service. The spec extends `tools/loadgen` with two new read-load presets — `history-read` (60/20/10/10 across `LoadHistory` / `GetMessageByID` / `LoadSurroundingMessages` / `GetThreadMessages`) and `search-read` (50/50 across `searchMessages` / `searchRooms`) — that share the existing seed pool and the messaging-pipeline preset as their warm-up step. This plan breaks that work into TDD-ordered tasks.

**Phase 2 expands scope** to one more client-driven scenario (`room-rpc`, against `room-service` — non-natsrouter but transport-identical) plus consumer-lag samplers for the remaining stream-only workers. The chassis built in Tasks 1-5 absorbs this with no architectural changes; Tasks 11-15 are appended at the bottom.

Existing harness layout (the work edits in place — no new directory):

```
tools/loadgen/
  preset.go        preset_test.go
  generator.go     generator_test.go
  metrics.go
  report.go        report_test.go
  collector.go     collector_test.go
  consumerlag.go   consumerlag_test.go
  seed.go
  main.go          main_test.go
  integration_test.go
  README.md
  deploy/
    docker-compose.loadtest.yml
    grafana/dashboards/loadtest.json
```

## TDD ordering

Each task is Red → Green → Refactor → Commit on a single dedicated branch (`claude/natsrouter-loadtest-impl`, stacked on the spec PR's branch). Tasks 1-7 are unit-test-driven and self-contained; task 8 is the integration test; tasks 9-10 are deploy + docs and don't drive new behavior so they share a final commit.

### Task 1 — `history-read` preset definition

**Red:** In `preset_test.go`, add a case asserting that a `Preset("history-read")` lookup returns a struct whose request-type weights are `{LoadHistory: 60, GetMessageByID: 20, LoadSurroundingMessages: 10, GetThreadMessages: 10}` and whose seed counts equal the existing `small` preset (so the warm-up workflow Just Works).

**Green:** Add the preset entry to `preset.go`. Use a typed enum (`type historyRequestKind int`) for the four kinds rather than free-form strings so the weighted-pick lookup is compile-checked.

**Refactor:** If the existing preset map shape needs broadening to carry per-scenario knobs, do it here once.

### Task 2 — `search-read` preset definition

**Red:** Same shape as task 1 in `preset_test.go`: `Preset("search-read")` returns weights `{SearchMessages: 50, SearchRooms: 50}` and a small bag of seeded query tokens (e.g. 10 short strings drawn from a fixture so tests are deterministic).

**Green:** Add the entry to `preset.go` plus a `searchRequestKind` enum.

### Task 3 — weighted picker

**Red:** New `picker_test.go` (or extend `preset_test.go`) that asserts: with seed `r := rand.New(rand.NewSource(42))`, calling `pickHistoryKind(r)` 10 000 times produces counts within ±2% of the configured weights. Same for search. Use `math/rand/v2`.

**Green:** Implement `pickHistoryKind` / `pickSearchKind` in `preset.go` using cumulative-weight + binary search. One generic helper if the two ergonomics align cleanly; two siblings if not.

### Task 4 — typed request builders

**Red:** In `generator_test.go`, add table-driven cases that assert `buildHistoryRequest(kind, user, room, fixture) → (subject, []byte, error)` returns the right `pkg/subject` builder output and a JSON-marshalled body matching the corresponding `pkg/model` struct (e.g. `model.LoadHistoryRequest{RoomID: room.ID, Size: 50}`). Same for search: `buildSearchRequest(kind, user, queryToken)`.

**Green:** Implement the two builders in `generator.go`. Lean on existing `pkg/subject` constructors (`subject.MsgHistory`, `subject.SearchMessages`, etc.) and `pkg/model` types — no new model code.

### Task 5 — generator scenarios

**Red:** Extend `generator_test.go` with a fake `nc.Request` (use the existing test double pattern from `messaging-pipeline` cases). Assert that `historyReadGenerator.tick(ctx, fakeNC)` issues exactly one request per tick on the right subject, classifies success / `ErrBadRequest` / timeout / `ErrUnavailable` (the natsrouter saturation reply) into the right metric labels, and bumps the per-handler histogram. Same for search.

**Green:** Add `historyReadGenerator` and `searchReadGenerator` types alongside the existing messaging-pipeline generator. Wire scenario selection through a `--scenario={messaging-pipeline,history-read,search-read}` flag in `main.go`.

**Refactor:** If `messaging-pipeline`'s generator can extract a shared `requester` interface, do it here so the three generators share dispatch + metric plumbing.

### Task 6 — metrics

**Red:** In a new `metrics_test.go` case (or by extending the existing collector tests), assert that registering the loadgen Prometheus registry exposes `loadgen_history_request_duration_seconds{handler,status}` and `loadgen_search_request_duration_seconds{handler,status}` with the existing histogram buckets, and that `status` admits exactly `{ok, error, timeout, busy}` (the last for `ErrUnavailable`).

**Green:** Add the two histograms + counters in `metrics.go`. Reuse the existing bucket constant — don't re-declare.

### Task 7 — report (terminal + CSV)

**Red:** Extend `report_test.go` with cases that feed synthetic samples into the report builder and assert the rendered terminal output contains a "History Read" section with per-handler p50/p95/p99 + error counts, a "Search Read" section with the same shape, and that the CSV export gains the matching columns. Use `cupaloy` snapshot or `assert.Contains` on key lines — match the style of existing tests.

**Green:** Implement the two new report sections in `report.go`. The existing percentile helper handles the math.

### Task 8 — integration test

**Red:** A new `//go:build integration` case in `integration_test.go`:
1. Spin up NATS + Mongo + Cassandra + Elasticsearch + Valkey via `testcontainers-go` (use `pkg/testutil/testimages` for image pinning).
2. Start in-process binaries (via `os/exec` against pre-built service binaries, matching the existing pattern) for `message-gatekeeper`, `message-worker`, `broadcast-worker`, `search-sync-worker`, `history-service`, `search-service`.
3. Seed via the existing `loadgen seed` path.
4. Run `messaging-pipeline` for 30s as warm-up.
5. Run `history-read` for 5s, assert: ≥ 100 samples observed, error rate = 0, p99 < 1s.
6. Run `search-read` for 5s, same assertions.
7. Cleanup is `t.Cleanup`-driven per the existing pattern.

**Green:** Land whatever wiring the test surfaces (likely a `withReadinessProbe` helper to wait for service-up before generating, and an extra arg or two on existing setup helpers).

### Task 9 — compose + Grafana

No tests for this task — pure infra config. Edits:

- `tools/loadgen/deploy/docker-compose.loadtest.yml`: add `cassandra:4.1.3`, `elasticsearch:8.x`, `valkey:8`, `history-service`, `search-service`, `search-sync-worker` services. Match existing build context = repo root, env vars per `BOOTSTRAP_STREAMS=true` in dev, and depends_on chains so a `docker compose up` actually reaches a healthy state.
- `tools/loadgen/deploy/grafana/dashboards/loadtest.json`: add two preset-scoped row groups mirroring the existing messaging-pipeline rows. Pull bucket/labels from task 6 directly.
- `tools/loadgen/deploy/Makefile`: add a `run-natsrouter` target that runs the four-step warm-up + `history-read` + `search-read` sequence end-to-end.

Smoke-test via `make -C tools/loadgen/deploy run-natsrouter` and confirm the dashboards render samples.

### Task 10 — README

Document, in `tools/loadgen/README.md`:
- The two new presets and their request mixes.
- The warm-up workflow (`messaging-pipeline` first, then the read presets).
- Resource expectations (compose stack roughly doubles in memory/disk vs. the messaging-pipeline-only run).
- The `make run-natsrouter` shortcut.

Tasks 9 + 10 may share a single commit since neither lands new code paths.

## Done definition

**Phase 1 (Tasks 1-10):**
- `make test SERVICE=loadgen` is green.
- `make test-integration SERVICE=loadgen` is green (CI-eligible — same shape as the existing integration test).
- `make lint` is green.
- Coverage for new code in `tools/loadgen` ≥ 80%.
- `make -C tools/loadgen/deploy run-natsrouter` produces non-empty samples in both the `history_request_duration` and `search_request_duration` histograms within 30s of `loadgen run` start, with zero error-rate.

**Phase 2 (Tasks 11-15) — additional gates on top of Phase 1:**
- `BuiltinPreset("room-rpc")` returns the documented mix; `make test SERVICE=loadgen` covers it.
- `loadgen run --scenario=room-rpc --preset=room-rpc --duration=10s` against the local compose stack produces non-zero `loadgen_requests_total{scenario="room"}` for at least three of the five room-request kinds, with zero error-rate.
- `loadgen run --scenario=messaging-pipeline --duration=10s` produces non-zero `loadgen_consumer_pending` gauges for `notification-worker` and the three `search-sync-worker-*` durables (visibility, not health threshold).
- New code paths for Phase 2 maintain ≥ 80% coverage.

## Risk

**Phase 1:**
- The compose stack grows from ~3 services to ~9. CI run-time and local laptop footprint both go up. If CI integration-test run-time becomes a problem, gate the new test behind a `LOADGEN_NATSROUTER=1` env var so the stack only spins up when explicitly requested.
- Elasticsearch + Cassandra startup is slow (30-60s combined). Readiness probes in task 8 must be patient or the test will be flaky on cold cache; budget at least 90s for setup.
- search-sync-worker's Mongo→ES sync has measurable lag. The 30s warm-up should be enough for the seeded message corpus, but if `search-read` integration assertions are flaky, bump to 60s rather than reduce the corpus size.

**Phase 2 incremental:**
- `room-rpc` mutates Mongo state. Mitigated by running against the loadgen-owned `MONGO_DB=loadgen` and `WriteIDPrefix="loadgen-"` for forensic identification. `make teardown` already drops the database. Worst case: a long run leaves an inflated `rooms` collection in the loadgen DB until teardown.
- Compose stack grows by **one more service** (`room-service`). Phase 1 already required substantial growth; one extra service is incremental. Phase 2 integration-test time impact is ~30s additional startup.
- Sampler additions for stream consumers are zero-risk: they read JetStream consumer state via the same `ConsumerSampler` already used for `message-worker` / `broadcast-worker`. Empty gauges read as 0.
- Task 15 doesn't add unit tests — it's pure config. The Phase 1 integration test from Task 8 doesn't exercise the new samplers; Phase 2 should extend it to assert the new gauges are present (one extra `assert.NotEmpty(samplers)` per durable).

---

## Phase 2 — additional services

**Spec section:** [Phase 2 — additional services](../specs/2026-05-07-history-service-loadtest-design.md#phase-2--additional-services)

Same PR as Phase 1; tasks land after Task 10 in the same branch. Each task follows the Red → Green → Refactor → Commit cycle. Estimated total: ~1.5 days of focused work.

### Task 11 — `room-rpc` preset definition

**Red:** In `preset_test.go`, add a case asserting that `BuiltinPreset("room-rpc")` returns a struct whose `RoomMix` weights are `{RoomsList: 60, RoomsGet: 20, MemberList: 10, RoomCreate: 8, MemberAdd: 2}` (total 100), with `Users: 1000, Rooms: 100` (matching the existing `realistic` seed shape) and a `WriteIDPrefix: "loadgen-"` field for forensic identification of generated state.

**Green:** Add to `preset.go`:
- `roomRequestKind` typed enum (5 variants).
- `RoomMix map[roomRequestKind]int` field on `Preset`.
- `WriteIDPrefix string` field (zero-value = no prefix; `room-rpc` sets it).
- `room-rpc` preset entry in `BuiltinPreset`.
- `pickRoomKind(r, weights)` 1-line wrapper around the existing generic `pickWeighted`.

**Refactor:** None expected — `Preset` already carries `HistoryMix` / `SearchMix` of the same shape.

### Task 12 — `room-rpc` request builder

**Red:** New file `request_builder_room_test.go`. Table-driven cases asserting `buildRoomRequest(kind, args) → (subject, body, error)` returns:
- For `RoomsList`: subject `chat.user.{account}.request.rooms.list`, body `{}`.
- For `RoomsGet`: subject `chat.user.{account}.request.rooms.get.{roomID}`, body `{}`.
- For `MemberList`: subject `chat.user.{account}.request.room.{roomID}.{siteID}.member.list`, body marshals `model.ListRoomMembersRequest`.
- For `RoomCreate`: subject `chat.user.{account}.request.room.{siteID}.create`, body marshals `model.CreateRoomRequest` with `Name = WriteIDPrefix + <random>`.
- For `MemberAdd`: subject `chat.user.{account}.request.room.{roomID}.{siteID}.member.add`, body marshals `model.AddMembersRequest` with one member drawn from fixtures.
- Unknown kind returns an error.

**Green:** Implement `buildRoomRequest` in a new `request_builder_room.go`. Reuse existing `pkg/subject` builders (`RoomsList`, `RoomsGet`, `MemberList`, `RoomCreate`, `MemberAdd`) — no new subject builders needed.

### Task 13 — `RoomRPCGenerator`

**Red:** Add `room_generator_test.go` cases (parallel to `read_generator_test.go`):
- `TestRoomRPCGenerator_ProducesValidSubjects` — runs briefly and asserts every recorded subject matches one of the five concrete patterns.
- `TestRoomRPCGenerator_RespectsRoomMix` — 100% `RoomsList` mix produces only `rooms.list` subjects.
- `TestRoomRPCGenerator_NoFixtures_NoRequests` — empty fixtures → zero requests issued.
- `TestRoomRPCGenerator_RequestErrorIncrementsMetric` — seed a `Requester` that returns errors; assert `loadgen_request_errors_total{scenario="room"}` increments.
- `TestRoomRPCGenerator_ZeroRate_ReturnsError`.

**Green:** New file `room_generator.go`. Copy the tick + bounded-pool dispatch from `HistoryReadGenerator`; only the per-tick body differs. Call `buildRoomRequest`; record metrics with `scenario="room"` and `kind=roomKindLabel(kind)`. Reuse the `Requester` interface unchanged.

**Refactor:** If the three generator types now have substantially identical Run() loops (likely true), consider extracting a shared `tickLoop` helper. Out-of-line per the user's earlier "sibling types, no refactor" direction; revisit only if Task 13's diff is offensive.

### Task 14 — `--scenario=room-rpc` dispatch

**Red:** Extend `main.go`-adjacent test (or smoke-test by binary build): `loadgen run --scenario=room-rpc --preset=room-rpc` returns a meaningful "rate must be > 0" error when rate is 0; returns a meaningful "preset not found" when preset is missing. (Existing `unknown scenario` case already handles validation.)

**Green:** In `main.go` `runRun`, add a new `case "room-rpc":` arm to the dispatch switch that constructs `NewRoomRPCGenerator(...)` with the `requester` adapter already in place.

**Refactor:** Trim the generator-construction switch if it's grown to the point of duplicating preset-lookup boilerplate.

### Task 15 — sampler additions for `room-worker`, `notification-worker`, `search-sync-worker`

No new tests — the `ConsumerSampler` is exercised by Phase 1's integration test. Edit:

- `main.go`: extend the `samplers := []*ConsumerSampler{...}` slice with five new entries (one per durable from the spec table). Reuse the existing `NewConsumerSampler` constructor; reuse `stream.Rooms(siteID)` / `stream.Inbox(siteID)` for stream names.
- The terminal report's "Consumers" section auto-includes them via the existing iteration.

**Smoke test.** `loadgen run --scenario=messaging-pipeline --duration=10s` should produce non-zero `loadgen_consumer_pending` for `notification-worker`, `search-sync-worker-messages`. `loadgen run --scenario=room-rpc --duration=10s` should produce non-zero values for `room-worker`. Other gauges read 0 cleanly per scenario.

---

## Phase 3 — harness upgrades

**Spec section:** [Phase 3 — harness upgrades](../specs/2026-05-07-history-service-loadtest-design.md#phase-3--harness-upgrades)

Same PR; tasks land after Task 15 in the same branch. Six upgrades; each is its own TDD task. Estimated total: ~4 days of focused work, with Tasks 16-18 (UX) deliverable in ~2 days as a partial-merge if needed.

### Pre-Phase-3 cleanup (carried from baseline review)

Before Task 16 lands, address the warnings the baseline reviewer surfaced. These are not blockers for shipped Phase 1 code but matter for Phase 2 / Phase 3 because the third generator type (`RoomRPCGenerator`) drops into the chassis they touch:

- **Cleanup A:** Replace bare-string `errors` in `tools/loadgen/read_generator.go:62,219` and `request_builder.go:83,121` with sentinels (`var ErrInvalidRate = errors.New(...)`). CLAUDE.md §3 mandates `fmt.Errorf` wrapping; sentinels are the right alternative when there's no underlying error.
- **Cleanup B:** Extract a shared `tickLoop(ctx, rate, maxInFlight, scenarioLabel, tickFn)` from `HistoryReadGenerator.Run` / `SearchReadGenerator.Run`. Today the two are byte-identical aside from saturation labels; with `RoomRPCGenerator` incoming they'd become byte-identical *thrice*.
- **Cleanup C:** Replace `RequestErrors{kind="saturated", reason="saturated"}` (the duplicate label values) with `kind="*"` since saturation is a queue-level event, not a per-RPC event.
- **Cleanup D:** Move hard-coded `Scope: "channel"` and `Size: 20` from `read_generator.go:289-291` onto `Preset.SearchScope` / `Preset.SearchSize`. Keeps the preset the single source of workload truth.

Cleanup tasks share one commit; they're a refactor before Phase 2 Task 13, not a new feature.

### Task 16 — auto warm-up for read scenarios

**Red:** New `auto_warmup_test.go`. Cases:
- `TestAutoWarmup_PopulatesMessageIDPool` — run a brief messaging-pipeline phase (using a fake Publisher), assert the resulting Collector has ≥ N messages in `byMsgID` and that `Collector.MessageIDs()` returns them.
- `TestAutoWarmup_HandsOffToHistoryRead` — orchestrator function takes `(ctx, cfg)` and yields a `MessageIDs` slice; assert the slice is passed through to `HistoryReadConfig`.
- `TestAutoWarmup_SkipsWhenNotNeeded` — when `HistoryMix` only contains `LoadHistory`, warm-up phase isn't run (no message IDs needed).
- `TestAutoWarmup_OptOut` — `--auto-warmup=false` skips even when needed; user accepts the empty-pool error counter.

**Green:**
- New `Collector.MessageIDs() []string` method (lock + key extract from `byMsgID`).
- New `runWithAutoWarmup(ctx, cfg) ([]string, error)` orchestrator in `main.go` (or a sibling `auto_warmup.go`).
- `runRun` calls it conditionally before the read generator dispatch when `scenario=="history-read" && needsWarmup(preset)`.

**Refactor:** None expected. Auto-warmup is purely additive; existing scenarios are untouched.

### Task 17 — live progress reporting

**Red:** New `progress_test.go` cases:
- `TestProgress_EmitsAtConfiguredInterval` — wire a fake clock + capture stdout; assert one line per interval.
- `TestProgress_ComputesInstantaneousRate` — feed synthetic counter deltas, assert reported rate matches the delta over the last interval (not lifetime).
- `TestProgress_StopsOnContextCancel` — cancel context, assert no further lines.

**Green:**
- New `progress.go` with a `Progress` type holding metrics references + a tick goroutine.
- `Progress.Run(ctx)` reads `Registry.Gather()` once per `--progress-interval` (default 10s), computes deltas vs. previous, emits one structured slog line.
- Started by `runRun` after readiness probe (Task 18) succeeds. Stopped on context cancel.

**Refactor:** Hoist the metric-name string constants to a private package-level block so `progress.go` and `main.go` share a single source of truth for what to scrape.

### Task 18 — service readiness probes

**Red:** `readiness_test.go` cases:
- `TestReadiness_SucceedsOnFirstReply` — fake nc.Request returns immediately; assert `WaitForReady` returns nil within one probe interval.
- `TestReadiness_RetriesUntilReady` — fake fails N times then succeeds; assert backoff sequence (200ms, 400ms, 800ms, ...) caps at 2s, total elapsed ≤ 30s.
- `TestReadiness_ReturnsErrorAfterDeadline` — fake always fails; assert `ctx.DeadlineExceeded` (or wrapped equivalent).
- `TestReadiness_ScenarioDispatch` — table-driven, one entry per scenario; assert the right probe subject is selected.

**Green:**
- New `readiness.go` with `WaitForReady(ctx, scenario, nc, fixtures)` and per-scenario probe builders. Probe subjects sourced from `pkg/subject` (no new builders needed).
- `runRun` calls it after `nc` connect, before generator construction. Skipped when `--skip-readiness=true`.

**Refactor:** None.
