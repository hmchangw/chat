# notification-fanout scenario (Phase 3 §3.4)

## Status

**§3.4a (in-app) SKELETON — unconditional.** The scenario is registered and
the `notifLagTracker` + `loadgen_notification_lag_seconds{channel="inapp"}`
metric are wired. The tick loop is a stub pending NATS subscriber wire-up
(see TODO in `scenario_notif.go`).

**§3.4b (push/email) DEFERRED-UNTIL-SUT-READY.** This sub-scenario ships as
a skeleton behind the `notif_routing_ready` build tag (added 2026-05-18).
The sub-scenario will fully activate once the SUT defines per-channel
notification routing builders in `pkg/subject/`.

## SUT subject contract

### §3.4a — in-app (already exists)

- `chat.user.{account}.notification` — per-user in-app notification subject.
  Builder: `subject.Notification(account string) string` (exists in
  `pkg/subject/subject.go`).

### §3.4b — push/email (not yet defined)

The scenario expects these builders to appear in `pkg/subject/`:

- `subject.PushNotification(account string) string` — per-user push channel.
- `subject.EmailNotification(account string) string` — per-user email channel.

## Activation steps — §3.4a (in-app full wire-up)

When the messaging pipeline exposes a `publishOne`-compatible function that
returns the published message ID:

1. In `tools/loadgen/scenario_notif.go`, replace the STUB tick comment with:
   - Call `publishOne` (or a trimmed equivalent) to publish a message with
     mention-injected payload when `rand.Float64() < preset.MentionRate`.
   - Call `g.tracker.RecordPublished(msgID, time.Now().UTC())` after publish.
   - For every fixture user, subscribe to
     `subject.Notification(user.Account)` via
     `g.deps.Subscribers().Subscribe(subj, handler)`.
   - In the handler: extract `messageID` from payload, call
     `g.tracker.LagFor(messageID, time.Now().UTC())`, observe into the
     `inapp` histogram if `ok`.
2. Remove the `_ = inapp` no-op from `Run`.
3. Update this document's status to "ACTIVE".

## Activation steps — §3.4b (push/email)

When the SUT lands `subject.PushNotification` and `subject.EmailNotification`:

1. Remove the `//go:build notif_routing_ready` tag from
   `tools/loadgen/scenario_notif_routing.go`.
2. Add `OfflineUserFraction` + `DNDUserFraction` preset fields to the
   `realistic` preset (suggested defaults: 0.3 and 0.1 respectively).
3. In the scenario's `Run` method:
   - Mark `round(len(users) * preset.OfflineUserFraction)` users as offline
     (exclude from in-app subscriber set).
   - Mark `round(len(users) * preset.DNDUserFraction)` users as DND
     (suppress push/email delivery expectation).
   - For each offline non-DND user, subscribe to
     `subject.PushNotification(user.Account)` and record receipt lag with
     `channel="push"`.
   - Optionally subscribe to `subject.EmailNotification(user.Account)` and
     record with `channel="email"`.
4. Append `|presence-typing` to the `--scenario` flag help in `flags.go` if
   not already present; regenerate the golden file.
5. Update this document's status to "ACTIVE".

## Algorithm summary

### §3.4a tick loop

```
for each tick:
    sender  = pick sender from fixture users (SenderDist)
    room    = pick room that sender is subscribed to
    mention = (rand.Float64() < preset.MentionRate) → inject @mention in body
    msgID, publishedAt = publishOne(sender, room, body)
    tracker.RecordPublished(msgID, publishedAt)
```

### §3.4a subscriber handler (per fixture user)

```
on receive(subject.Notification(user.Account)):
    msgID = payload.MessageID
    if lag, ok := tracker.LagFor(msgID, time.Now().UTC()); ok {
        notificationLag.WithLabelValues("inapp").Observe(lag.Seconds())
    }
```

### §3.4b extensions

Same as §3.4a but per-channel:
- Online users → `channel="inapp"` histogram.
- Offline, non-DND users → `channel="push"` histogram.
- Optional: email path → `channel="email"` histogram.
- DND users → no observation (delivery suppressed).

## Metrics

- `loadgen_notification_lag_seconds{channel="inapp"}` — in-app publish→receipt
  latency histogram. Buckets: exponential from 1 ms (×2 factor, 14 buckets →
  max ~16 s).
- `loadgen_notification_lag_seconds{channel="push"}` — push publish→receipt
  lag (§3.4b, gated on `notif_routing_ready`).
- `loadgen_notification_lag_seconds{channel="email"}` — email publish→receipt
  lag (§3.4b, gated on `notif_routing_ready`).

## Preset fields

| Field | Default | Description |
|---|---|---|
| `MentionRate` | 0.0 (0.10 for `realistic`) | Fraction of messages that carry an @mention, triggering notification fanout |
| `OfflineUserFraction` | 0.0 | Fraction of users treated as offline (§3.4b only) |
| `DNDUserFraction` | 0.0 | Fraction of users in Do-Not-Disturb (§3.4b only) |
