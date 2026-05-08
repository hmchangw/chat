# Plan: extend `tools/loadgen` with read-load presets for natsrouter services

**Date:** 2026-05-07
**Spec:** [`docs/superpowers/specs/2026-05-07-history-service-loadtest-design.md`](../specs/2026-05-07-history-service-loadtest-design.md)
**Status:** ready (lands as a separate PR stacked on the spec PR once approved)

## Context

Two NATS request/reply services are built on `pkg/natsrouter` today: history-service and search-service. The spec extends `tools/loadgen` with two new read-load presets — `history-read` (60/20/10/10 across `LoadHistory` / `GetMessageByID` / `LoadSurroundingMessages` / `GetThreadMessages`) and `search-read` (50/50 across `searchMessages` / `searchRooms`) — that share the existing seed pool and the messaging-pipeline preset as their warm-up step. This plan breaks that work into TDD-ordered tasks.

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

- `make test SERVICE=loadgen` is green.
- `make test-integration SERVICE=loadgen` is green (CI-eligible — same shape as the existing integration test).
- `make lint` is green.
- Coverage for new code in `tools/loadgen` ≥ 80%.
- `make -C tools/loadgen/deploy run-natsrouter` produces non-empty samples in both the `history_request_duration` and `search_request_duration` histograms within 30s of `loadgen run` start, with zero error-rate.

## Risk

- The compose stack grows from ~3 services to ~9. CI run-time and local laptop footprint both go up. If CI integration-test run-time becomes a problem, gate the new test behind a `LOADGEN_NATSROUTER=1` env var so the stack only spins up when explicitly requested.
- Elasticsearch + Cassandra startup is slow (30-60s combined). Readiness probes in task 8 must be patient or the test will be flaky on cold cache; budget at least 90s for setup.
- search-sync-worker's Mongo→ES sync has measurable lag. The 30s warm-up should be enough for the seeded message corpus, but if `search-read` integration assertions are flaky, bump to 60s rather than reduce the corpus size.
