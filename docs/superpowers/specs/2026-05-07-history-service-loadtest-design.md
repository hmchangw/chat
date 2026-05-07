# Extend `tools/loadgen` with history-service read-load workload

**Date:** 2026-05-07
**Status:** scoping (this PR proposes the design; implementation lands in a follow-up)

## Goal

Add a read-side load-test scenario to the existing `tools/loadgen` harness that exercises history-service's NATS request/reply handlers (history pagination, thread queries, single-message fetch) under sustained load, with the same Prometheus-driven reporting the messaging-pipeline scenario already produces.

## Background

`tools/loadgen` (added in [docs/superpowers/specs/2026-04-21-load-test-messaging-workers-design.md](2026-04-21-load-test-messaging-workers-design.md)) is a Go binary with three subcommands (`seed` / `run` / `teardown`) that drives the **messaging pipeline** — `message-gatekeeper → MESSAGES_CANONICAL → message-worker + broadcast-worker`. It opens an NATS connection, seeds users/rooms/subscriptions, fires `SendMessageRequest` events with an open-loop ticker, and tracks reply-correlation, broadcast-correlation, and consumer lag via Prometheus.

What it does NOT cover today: history-service. history-service is a NATS request/reply service (no JetStream consumer in the hot path), backed by Cassandra for messages and MongoDB for subscriptions/thread metadata. Its workload profile is read-heavy: 8 distinct request handlers, fanned out across users with different access windows.

## history-service handler surface (as of `b505e47`)

Registered in `history-service/internal/service/service.go:91-99`:

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

Plus the service depends on a subscription lookup against MongoDB (`subscriptions` collection) for the access-window check on every read.

## Proposed workload — preset `history-read`

Open-loop publisher driven by `time.Ticker` (the existing pattern in `tools/loadgen/generator.go`). For each tick:

1. Pick a `(user, room)` pair from the seeded population (existing seed.go scaffolding works as-is — users/rooms/subscriptions were already seeded for the messaging-pipeline scenario; history-service consumes the same collections).
2. Pick a request type from a configurable mix:
   - 60 % `LoadHistory` (canonical "open the room" path; fetches the most-recent N messages)
   - 20 % `GetMessageByID` (deep-link / quote-resolve path; single-message lookup)
   - 10 % `LoadSurroundingMessages` (jump-to-message path; window query)
   - 10 % `GetThreadMessages` (thread-open path; thread-bucket walk)
   - (Edits/deletes excluded from v1 — they mutate state and would interact with the messaging-pipeline scenario; revisit if write-load coverage becomes interesting.)
3. Marshal the request struct (`models.LoadHistoryRequest`, `models.GetMessageByIDRequest`, etc. — already public).
4. `nc.Request(subject, payload, deadline)`. Record latency in a per-request-type histogram. Track success / 4xx-style validation errors / timeout / NATS error.

Open-loop pacing: target rate is `RPS` per second; if the consumer can't keep up, requests queue (which surfaces as latency p99 climbing). Same pattern as the existing generator — no closed-loop wait.

## Required additions

### Code (under `tools/loadgen/`)

| File | Change |
|---|---|
| `preset.go` | New built-in preset `history-read` with the request-type mix above. |
| `generator.go` | New `historyReadGenerator` type implementing the existing generator interface, OR a `--scenario=history-read` flag that switches the generator's hot path. |
| `metrics.go` | Per-handler latency histogram (`loadgen_history_request_duration_seconds{handler="LoadHistory|GetMessageByID|...",status="ok|error|timeout"}`) and a request-counter. Reuse existing histogram buckets. |
| `report.go` | Add a "History Read" section to the terminal summary: per-handler p50/p95/p99 latency, success rate, RPS achieved, error counts. CSV export gains the same columns. |
| `integration_test.go` | New `//go:build integration` test that spins up real NATS + Mongo + Cassandra + history-service, seeds with `small` preset (existing), runs `history-read` for 5s, asserts non-empty samples + zero error rate. |
| `README.md` | Document the new preset, request mix, and recommended seed sizes. |

### Deploy

- `tools/loadgen/deploy/docker-compose.loadtest.yml`: add the `history-service` container plus its dependencies (Cassandra is new — the existing compose only had Mongo and NATS for the messaging-pipeline scenario; bring in `cassandra:4.1.3` per `pkg/testutil/testimages`).
- `tools/loadgen/deploy/grafana/dashboards/loadtest.json`: add panels for the new metrics. Re-use existing dashboard structure (latency p50/p95/p99 over time, RPS counter, error-rate gauge).

### Seed data

The messaging-pipeline scenario already seeds users/rooms/subscriptions in MongoDB. history-service needs **populated message history in Cassandra** to produce realistic latencies — an empty `messages_by_room` table makes every read return zero rows in O(1).

Two options:
- **(A) Reuse the messaging-pipeline scenario as a warm-up.** Run `loadgen run` against `messaging-pipeline` for 60s first to populate Cassandra via `message-worker`, then run `history-read`. Cleanest because no duplicate seeding code.
- **(B) Add a `seed --history` flag** that bulk-writes synthetic messages directly into `messages_by_room` and `messages_by_id` from the loadgen process, bypassing the messaging pipeline.

Recommended: (A) for v1 (zero new code, realistic data shape). Document the run order in `tools/loadgen/README.md`. If (B) becomes useful for isolating history-service from worker performance, add it later.

## Out of scope (this PR / first cut)

- Write-side handlers (EditMessage, DeleteMessage). Mutate state; revisit when write-load matters.
- Federation / cross-site read patterns (history-service is per-site).
- Connection-pool / keepalive tuning sweeps. The existing `MAX_IN_FLIGHT` knob already covers concurrency.
- Per-user rate limiting on the loadgen side (open-loop publishing is the design choice).
- gRPC / HTTP probes — history-service is NATS-only.

## Risk and reversibility

- Pure additive: existing messaging-pipeline preset is unchanged.
- New deploy entry adds Cassandra to the compose stack; runs locally only via `make -C tools/loadgen/deploy run-history`. Production untouched.
- Failure mode: a buggy generator floods history-service. The existing `MAX_IN_FLIGHT` cap bounds in-flight requests; same protection as the messaging-pipeline scenario.

## Implementation plan (follow-up PR)

If the design above is accepted, the implementation breaks into ~8 tasks:

1. Add `history-read` preset + request-type weighted picker (preset.go + tests).
2. Marshalable request templates per handler (uses existing `pkg/model.GetMessageByIDRequest` etc.).
3. Generator scenario: per-handler `nc.Request` with timeout + error classification.
4. Metrics: per-handler latency histogram + counter; reuse existing buckets.
5. Report: terminal summary section + CSV columns.
6. Compose: add `history-service` + `cassandra` to docker-compose.loadtest.yml.
7. Grafana dashboard panels.
8. Integration test (real NATS+Mongo+Cassandra+history-service via testcontainers).

Each lands as a TDD task per the codebase's spec → plan → implementation pattern.

## Decision needed from maintainer

- Approve the preset request-type mix (60/20/10/10) or propose a different weighting?
- Approve seed-via-warm-up (option A) over the dedicated `seed --history` flag (option B)?
- Approve the out-of-scope list, or pull anything back in?

Once those three are settled, the implementation plan lands as a separate PR.
