# Loadgen `max-rps` read-receipt workload

**Date:** 2026-06-02
**Status:** Approved (brainstorming) — pending implementation plan

## Summary

Add a `read-receipt` workload to the loadgen `max-rps` ramp command. The workload
drives the room-service read-receipt RPC (`chat.user.{account}.request.room.{roomID}.{siteID}.message.read-receipt`)
at increasing RPS steps and reports the maximum sustainable rate under the
configured latency/error SLOs.

Read receipts are a synchronous NATS request/reply read ("who has read message
X"), so the existing `history` workload — also a synchronous request/reply read
with no JetStream consumer — is the template. The new workload plugs into the
existing `rpsWorkload` interface, reusing the ramp engine, verdict logic, and
report rendering unchanged.

## Background

The read-receipt handler (`room-service/handler.go`, `handleMessageReadReceipt`)
enforces the following before returning the reader list:

1. The requester is subscribed to the room (`GetSubscription`).
2. The target message exists (`msgReader.GetMessageRoomAndCreatedAt`, reads
   Cassandra `messages_by_id`).
3. The message belongs to the subject's room.
4. **The requester is the message's sender** (`msgSender == requesterAccount`),
   else `errNotMessageSender`.
5. `ListReadReceipts(roomID, since=msgCreatedAt, excludeAccount=sender, limit)`
   runs a Mongo aggregate: `$match {roomId, lastSeenAt >= since, u.account != sender}`
   → `$lookup` users → `$unwind` → `$replaceWith` → `$limit`.

Therefore a valid load target is a tuple `(senderAccount, roomID, messageID)`
where the message exists in Cassandra and the sender has a Mongo subscription.

**Key realism constraint:** history-seeded subscriptions set no `lastSeenAt`
(it is `*time.Time` with `omitempty`). Without it, the `ListReadReceipts`
`$match` matches zero documents and short-circuits before the `$lookup`/`$unwind`,
making the query artificially cheap. The workload must seed `lastSeenAt` on a
configurable fraction of subscribers to exercise the real query path.

## Decisions (from brainstorming)

- **Integration:** new `--workload read-receipt` adapter for the existing
  `max-rps` ramp command. Not a standalone sustained generator.
- **Fixtures:** reuse the history seed. `BuildHistoryFixtures` + `BuiltinHistoryPreset`
  produce users/rooms/subscriptions/messages; read-receipt targets are derived
  from `plan.Messages`.
- **Reader seeding:** tunable `--read-ratio` (default `0.7`). Stamp `lastSeenAt`
  on that fraction of each room's non-sender subscribers so the query returns
  realistic fan-out.
- **Targets:** top-level messages only (`ThreadParentID == ""`).

## Architecture

The `max-rps` command already owns a pluggable workload seam:

```go
type rpsWorkload interface {
    RunStep(ctx context.Context, targetRPS int, warmup, hold time.Duration) (rpsStepInputs, error)
    Label() string
}
```

The ramp engine (`ramp.go`), verdict evaluation (`verdict.go`), normalized
step inputs (`rpsStepInputs`), and report rendering (`maxrps_report.go`) are
reused as-is. The read-receipt workload only supplies a new adapter plus its
generator/collector/requester, mirroring the history workload's file layout.

### New files (each mirrors a history counterpart)

| File | Mirrors | Responsibility |
|------|---------|----------------|
| `tools/loadgen/maxrps_readreceipt.go` | `maxrps_history.go` | `readReceiptWorkload` implementing `rpsWorkload`. `newReadReceiptWorkload` wires NATS, the metrics HTTP server, the requester, and derives targets from `BuildHistoryFixtures`. `RunStep` runs warmup (discarded) then hold (measured) and returns `rpsStepInputs`. `Label()` returns `"read-receipt"`. |
| `tools/loadgen/readreceipt_generator.go` | `history_generator.go` | `ReadReceiptGenerator` with a `Rate` ticker and a `MaxInFlight` semaphore. Each tick picks a random target, issues the request via the requester, and records the result in the collector. Saturation (pool full on tick) is recorded, not dropped silently. |
| `tools/loadgen/readreceipt_collector.go` | `history_collector.go` | In-memory latency tape plus `timeout`, `reply-error`, `bad-reply`, and `saturation` counters. Thread-safe (mutex), `Reset()`-able. |
| `tools/loadgen/readreceipt_requester.go` | history requester (`newNATSHistoryRequester`) | `ReadReceiptRequester` interface + `newNATSReadReceiptRequester(nc)`. Builds the subject via `subject.MessageReadReceipt(account, roomID, siteID)`, marshals `model.ReadReceiptRequest`, calls `nc.Request(...)` with the per-request timeout, and classifies the reply (success / reply-error / bad-reply / timeout). |
| `tools/loadgen/readreceipt_seed.go` | `history_seed.go` | `SeedReadReceiptState(ctx, db, plan, readRatio, seed)`: stamps `lastSeenAt` on a deterministic `readRatio` sample of each room's non-sender subscribers. |

### Reused unchanged

- `BuildHistoryFixtures`, `BuiltinHistoryPreset` (`history.go`, `preset.go`).
- `Seed` (`seed.go`) — already creates `users`, `rooms`, `subscriptions`
  collections with the `roomId` and `u.account` indexes the RPC needs.
- `ramp.go`, `verdict.go`, `maxrps_report.go`, `rpsStepInputs`.

### Wiring in existing files

- `maxrps.go`: add `case "read-receipt"` to the `runMaxRPS` workload switch,
  constructing `newReadReceiptWorkload`. Reuse the existing `--request-timeout`
  flag for the per-request timeout (currently labelled history-only).
- `main.go`: add a `seed-read-receipt` subcommand that runs the full history
  seed (`Seed`, `SeedRoomKeys`, `SeedThreadRooms`, `SeedHistoryCassandra`) then
  `SeedReadReceiptState`, parameterized by `--preset`, `--seed`, `--read-ratio`.
- `maxrps.go` `defaultSteps`: add a `read-receipt` branch returning
  `"200,500,1000,2000,5000"` (history-like read profile).

## Data flow

1. **Seed (one-time):** `loadgen seed-read-receipt --preset <hp> --read-ratio 0.7`
   - Runs the existing history seed (Mongo users/rooms/subscriptions + Cassandra
     messages + room keys + thread_rooms).
   - `SeedReadReceiptState` then sets `lastSeenAt = latestTargetCreatedAt + 1ms`
     on a deterministic `read-ratio` sample of each room's non-sender subscribers.
2. **Fixtures (at run time):** `BuildHistoryFixtures(preset, seed, siteID, now)`
   → filter `plan.Messages` to `ThreadParentID == ""` →
   `[]readReceiptTarget{Account, RoomID, MessageID}`. The same `seed` reproduces
   the identities the seed step wrote.
3. **Per step:** the generator fires at `targetRPS`; for each tick it selects a
   random target and issues the read-receipt request. The collector tapes E2E
   latency on reply and counts hard errors and saturation.
4. **Normalized inputs:** `buildReadReceiptInputs` maps the hold-window collector
   to `rpsStepInputs`:
   - One latency series named `"read-receipt"`.
   - `AttemptedOps = replies + failed`.
   - `FailedOps = timeout + reply-error + bad-reply`.
   - `Saturation = saturated`.
   - `Pending` empty (synchronous read, no JetStream consumer — same as history).
   Latency SLO gating (`--slo-p95`/`--slo-p99`) and error-rate gating
   (`--slo-error-rate`) apply; `--slo-pending-growth` is ignored (like history).

## RunStep behavior

Mirrors `historyWorkload.RunStep`: run a fresh generator for `warmup` against a
throwaway collector (samples discarded), then run a fresh generator for `hold`
against the measured collector, sleep briefly to drain trailing in-flight
replies, and return `buildReadReceiptInputs(targetRPS, hold, collector)`.

## Error handling

- Requester classifies each request outcome: success (reply, no `error` field),
  reply-error (reply with non-empty `error`), bad-reply (unmarshal failure),
  timeout (`nats.ErrTimeout`/context deadline). Each maps to a collector counter.
- Saturation: when the in-flight semaphore is full on a tick, increment the
  saturation counter rather than blocking the ticker or dropping silently.
- Seed errors wrap with context (`fmt.Errorf("seed read-receipt state: %w", err)`).
- `--read-ratio` validated to `0 < r <= 1`; invalid returns exit code 2 with a
  message, matching the existing flag-validation convention in `runMaxRPS`.

## Testing (TDD — Red/Green/Refactor)

Unit tests (`package main`, `testify`):

- `parseReadRatio` / read-ratio validation: bounds `0 < r <= 1`, rejects `0`,
  negatives, `>1`, non-numeric.
- Target derivation: only top-level messages selected; thread replies excluded;
  tuple fields populated from the plan.
- `SeedReadReceiptState` sampling: deterministic for a fixed seed; selects the
  configured fraction of non-sender subscribers; `lastSeenAt` is after the
  target message `createdAt`; senders excluded.
- Collector accounting: latency tape length, per-reason error counts,
  saturation count, `Reset()` clears state.
- `buildReadReceiptInputs`: correct mapping to `rpsStepInputs` (single series,
  attempted/failed/saturation math, empty `Pending`).
- Generator: honors `Rate` (tick cadence) and records saturation when the
  semaphore is full, using a fake `ReadReceiptRequester`.
- Requester reply classification: success / reply-error / bad-reply / timeout
  against a fake NATS responder.

Integration test (`//go:build integration`, mirrors `history_integration_test.go`):

- Use `testutil.NATS` for the shared NATS URL (the existing history test uses
  only NATS plus a canned responder — it exercises the loadgen request/reply
  plumbing, not the real handler).
- Subscribe a minimal responder on `subject.MessageReadReceiptWildcard(siteID)`
  that returns a canned `model.ReadReceiptResponse`, build the workload, run one
  ramp step, and assert the step produced replies (non-empty `read-receipt`
  latency series, zero `FailedOps`).
- `SeedReadReceiptState` factors its selection into a pure function
  (`selectReaders(subs, readRatio, seed) -> []selected`) covered by unit tests;
  the Mongo write itself is thin bulk-update glue.
- `TestMain` drives `testutil.RunTests(m)` per CLAUDE.md.

## Documentation

- Update `tools/loadgen/README.md`: document `--workload read-receipt`, the
  `seed-read-receipt` subcommand, `--read-ratio`, and an example invocation.
- No `docs/client-api.md` change: this is load tooling only; the read-receipt
  RPC contract is unchanged.

## Out of scope (YAGNI)

- A standalone sustained-rate read-receipt subcommand (no max-RPS discovery).
- Thread-reply read-receipt targets.
- Prometheus per-reason counters for the verdict (the in-memory collector is the
  source of truth, matching history).
- Hot-room / Zipf target distribution (uniform pick, matching history).
