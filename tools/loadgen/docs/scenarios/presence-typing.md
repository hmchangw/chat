# presence-typing scenario (Phase 3 §3.3)

## Status

**DEFERRED-UNTIL-SUT-READY.** This scenario ships as a skeleton behind the
`presence_ready` build tag (added 2026-05-18 in commit `<TBD>`). The
scenario will fully activate once the SUT defines presence subject builders
in `pkg/subject/`.

## SUT subject contract

The scenario consumes these subjects (names suggested; SUT team owns the
final naming):

- `chat.user.{account}.presence.update` — operator-emitted presence updates
  (online/offline/away).
- `chat.user.{account}.typing.active` — operator-emitted "I'm typing now"
  events (token-bucket limited, ~1 per 3s per user).
- `chat.room.{roomID}.presence.subscribe` — observer subscription for
  presence/typing fanout in a room.

The expected `pkg/subject/` builders:

- `subject.PresenceUpdate(account string) string`
- `subject.TypingActive(account, roomID string) string`
- `subject.PresenceSubscribe(roomID string) string`

## Activation steps

When the SUT lands these builders:

1. Remove the `//go:build presence_ready` tag from
   `tools/loadgen/scenario_presence.go` and `scenario_presence_test.go`.
2. Flip `presenceSubjectsAvailable()` in `tools/loadgen/scenario_roomopen.go`
   to return `true` (Task 3.14 uses this to include the presence leg).
3. Add `loadgen_presence_fanout_seconds` and
   `loadgen_presence_delivered_ratio` metrics to `metrics.go`.
4. Wire the scenario's `Run` method to consume the subjects (see
   `scenario_presence.go` doc header for the full algorithm).
5. Append `|presence-typing` to the `--scenario` flag help in `flags.go`.
6. Regenerate `tools/loadgen/testdata/refactor-baseline/run-help.golden`.

## Algorithm summary (when activated)

Per (user, room) maintain a token bucket sized 1 token / 3s
(`--typing-bucket-period`). When the user is "currently-typing" (a subset
of `--typing-active-users` from the fixture pool), emit at the bucket rate.
`--typing-unthrottled` disables the bucket for ceiling tests.

Observer-side: for every fixture user, subscribe to
`PresenceSubscribe(roomID)` via `runtime.Subscribers().Subscribe(...)`. The
handler records `(messageID, recipient, receivedAt)` into a fanout tracker.

## Metrics (when activated)

- `loadgen_presence_fanout_seconds{preset}` histogram — emit → observer-
  receipt latency.
- `loadgen_presence_delivered_ratio` gauge — fraction of observers who
  received each presence event within the SLO window.

## Phase 3 §3.16 cross-reference

When `--federation` is on, presence events on a remote-homed room generate
cross-site `subscription_read` OUTBOX traffic. The federation-lag scenario
(Phase 3 §3.9) subscribes to `outbox.{site}.>.subscription_read` to
capture this — see scenario_federation.go.
