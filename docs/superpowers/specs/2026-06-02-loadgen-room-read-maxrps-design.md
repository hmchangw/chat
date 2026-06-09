# Design: `room-read` max-rps workload for loadgen

**Date:** 2026-06-02
**Status:** Approved (brainstorm) — pending implementation plan

## Goal

Measure the maximum sustainable RPS for **marking a room as read** (room-service's
`message.read` RPC), under a realistic read pattern, using the existing
`tools/loadgen` `max-rps` harness. "Sustainable" means the highest RPS step at
which p95/p99 latency and error rate stay within SLO — the same verdict model
already used by the `messages` and `history` workloads.

## Background

`max-rps` is a subcommand of the `loadgen` binary with two pluggable workloads
(`messages`, `history`) behind a small `rpsWorkload` interface
(`RunStep(ctx, targetRPS, warmup, hold) (rpsStepInputs, error)` + `Label()`).
The interface gets the ramp engine, SLO gating, verdict logic, report, and CSV
output for free. `history` is the closest analog: a synchronous NATS
request/reply workload, latency-gated only (no consumer durable / pending
growth).

"Mark room as read" maps to room-service's `message.read` RPC:

- Subject: `chat.user.{account}.request.room.{roomID}.{siteID}.message.read`
  (built via `subject.MessageRead`). No request body; marks the whole room read
  "as of now."
- Handler `handleMessageRead` hot path:
  1. `GetSubscription` (Mongo read).
  2. `UpdateSubscriptionRead` — sets `LastSeenAt = now` (Mongo write).
  3. Parallel `GetUserSiteID` + `GetRoom`.
  4. **Conditionally** publish a cross-site outbox event — only when the user is
     remote (`userSiteID != siteID`).
  5. **Conditionally** recompute the room read-floor: short-circuits if
     `room.LastMsgAt == nil` or `sub.LastSeenAt.After(room.LastMsgAt)`; otherwise
     runs `MinSubscriptionLastSeenByRoomID` (scan over the room's subscriptions)
     and, if the floor changed, `UpdateRoomMinUserLastSeenAt` (write to the hot
     `rooms` document).

The conditional floor scan/write is the interesting contention point and must be
exercised realistically for the ceiling to be meaningful.

## Decisions (from brainstorm)

1. **Goal:** find the realistic sustainable ceiling — same verdict model as the
   other workloads.
2. **Fixtures:** reuse the existing `messages` presets and add a read-state
   backfill (rather than minting dedicated `room-read-*` presets).
3. **Steady-state model:** backfill `rooms.LastMsgAt` to a timestamp **ahead of**
   the run window so the floor path stays live for the whole run.
4. **Integration shape:** new `room-read` workload behind the existing
   `rpsWorkload` interface in the `max-rps` subcommand (Approach A) — not a
   standalone subcommand, not folded into `history`.

## Why "LastMsgAt ahead of run" sustains the floor path

`UpdateSubscriptionRead` stamps `LastSeenAt = time.Now()` (run-time). The
floor-path short-circuit is `sub.LastSeenAt.After(room.LastMsgAt)`, evaluated
against the **pre-update** subscription snapshot. If every room's `LastMsgAt` is
a timestamp in the future (past the entire ramp window), then a reader's
freshly-stamped `LastSeenAt` (= present) is still *before* `LastMsgAt`, so the
reader never "catches up." Consequence:

- The floor **scan** (`MinSubscriptionLastSeenByRoomID`) fires on **every**
  request for the whole run — the realistic "user opening a room with unread
  content" case.
- The floor **write** (`UpdateRoomMinUserLastSeenAt`) fires only when the reader
  was the room's current floor-pinner — a rate set naturally by room size and the
  read distribution (small rooms / DMs → frequent floor writes; large rooms →
  expensive scans, rare writes).

If instead `LastMsgAt` were in the past, each member would catch up after its
first read and the path would self-extinguish — not a realistic steady state.

## Design

### CLI surface (mirrors `messages` / `history`)

- `loadgen seed --workload=room-read --preset=<name> [--seed=42]`
  Builds the messages fixtures (`BuildFixtures`) and **stamps read-state before
  insert**:
  - `rooms.LastMsgAt` = future timestamp (ahead of the whole run window).
  - `subscriptions.LastSeenAt` = deterministic spread behind `LastMsgAt`
    (derived from the RNG seed for reproducibility).
  - `rooms.MinUserLastSeenAt` = computed per-room floor (min of members'
    `LastSeenAt`).
  No room keys are seeded (the read path does not decrypt). Self-contained — no
  separate base-seed step required.
- `loadgen teardown --workload=room-read --preset=<name> [--seed=42]`
  Reuses the messages teardown (drops `users` / `rooms` / `subscriptions`).
- `loadgen max-rps --workload=room-read --preset=<name> [flags]`
  Runs the ramp.

Reuses the existing `messages` presets (`small` / `medium` / `large` /
`realistic`). Their room-size distribution drives floor-write contention for free.

### Workload adapter — `maxrps_roomread.go`

`roomReadWorkload` implements the existing `rpsWorkload` interface.
`newRoomReadWorkload(ctx, cfg, preset, seed, params) (*roomReadWorkload, func(), error)`
connects NATS, starts the metrics HTTP server, builds in-memory fixtures for
target selection, and returns a cleanup closure (metrics-server shutdown + NATS
drain). `RunStep` runs warmup (discarded) then hold (measured) as two sequential
generator runs via the same `runFor` helper used by `history`.
`buildRoomReadInputs(targetRPS, hold, collector)` produces `rpsStepInputs` with a
single latency series named `"room-read"`,
`FailedOps = timeouts + reply errors + bad replies`, `AttemptedOps`,
`Saturation`, and no `Pending` (synchronous RPC). Compile-time guard:
`var _ rpsWorkload = (*roomReadWorkload)(nil)`.

### Generator — `roomread_generator.go`

Open-loop rate limiter + `MaxInFlight` semaphore (mirrors `HistoryGenerator`).
Per request:

1. Pick a room by the preset's popularity weight (uniform, or Zipf for
   `realistic`), then pick a random member of that room → `(account, roomID)`.
2. Send `Requester.Request(ctx, subject.MessageRead(account, roomID, siteID),
   nil, timeout)`.
3. Validate the reply is `{"status":"accepted"}`; classify `errNotRoomMember` /
   non-accepted as bad-reply; classify transport timeouts and reply errors via a
   shared `classifyRequesterError`.
4. Record latency / error class to the collector.

A narrow `RoomReadRequester` interface (`Request(ctx, subject, data, timeout)
([]byte, error)`) is the transport seam; the production impl wraps
`nats.Conn.RequestWithContext`, tests inject a recorder — no real NATS in unit
tests.

### Collector — `roomread_collector.go`

Single-series latency tape plus counters (timeout / reply / bad-reply /
saturation). A focused mirror of `HistoryCollector` (which is multi-series).

### SLOs, verdict, report — reused

Reuses `rpsThresholds`, the ramp engine, verdict logic, `renderRPSReport`, and
`writeRPSCSV`. Defaults match `history`: `--slo-p95=100ms`, `--slo-p99=250ms`,
`--slo-error-rate=0.001`; all overridable via the existing shared flags. No
pending-growth gate (synchronous RPC). Default steps: `200,500,1000,2000,5000`
(overridable via `--steps`). All seeded users are local → `GetUserSiteID` returns
the local site → **no cross-site outbox publish**, consistent with the README's
single-site non-goal.

### Files & wiring

New files (each with a sibling `_test.go`):

- `maxrps_roomread.go` — workload adapter + `buildRoomReadInputs`.
- `roomread_generator.go` — generator + `RoomReadRequester` interface + NATS impl.
- `roomread_collector.go` — collector.
- `roomread_seed.go` — read-state stamping + seed/teardown helpers.

Wiring:

- `main.go`: add `room-read` cases to the `runSeed`, `runTeardown`, and
  `runMaxRPS` switches.
- `tools/loadgen/deploy/Makefile`: add `seed-roomread`, `run-roomread`,
  `teardown-roomread` targets.
- `tools/loadgen/README.md`: add a "Room-read workload" section.

No `docs/client-api.md` change — `message.read`'s request/response schema is
unchanged; this adds only a benchmark.

## Testing (TDD)

Unit tests (no real infrastructure; injected fakes):

- Backfill stamps `LastMsgAt` in the future, `LastSeenAt` behind it, and the
  correct per-room `MinUserLastSeenAt` floor; deterministic for a given seed.
- Generator emits correct `message.read` subjects, respects the rate and
  in-flight cap, and records latency / error classes.
- Target distribution yields only valid member↔room pairs (reader is a member of
  the room).
- Collector aggregation (latency tape + counters).
- `buildRoomReadInputs` maps collector state to `rpsStepInputs` correctly
  (single series, failed count, no pending).

Integration test (`//go:build integration`, `testutil.MongoDB` + `testutil.NATS`
with an echo `message.read` responder, mirroring `history_integration_test.go`):
seed → short ramp → assert on `rpsStepInputs`. `TestMain` drives
`testutil.RunTests`.

Coverage target ≥80% (project floor), ≥90% for the generator and collector.

## Non-goals

- Not a CI regression gate — invoked manually, like the rest of `loadgen`.
- Not a cross-site benchmark — single-site only; no outbox path exercised.
- Not an auth benchmark — uses shared `backend.creds`.
- Not an absolute-number tool — compare within one host across changes.
