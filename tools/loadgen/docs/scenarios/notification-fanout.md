# notification-fanout scenario (Phase 3 §3.4)

## Status

**§3.4a (in-app) IMPLEMENTED — unconditional.** The scenario subscribes to
`subject.Notification(account)` for every unique fixture account, publishes
user messages through the frontdoor at the configured rate, and observes
publish→notification lag into
`loadgen_notification_lag_seconds{channel="inapp"}`. Each published message
fans out to N recipients (every room member except the sender) — each
recipient's notification yields a separate lag observation, so the
histogram cardinality is N × tick-count, not just tick-count.

**§3.4b (push/email) DEFERRED-UNTIL-SUT-READY.** This sub-scenario ships as
a skeleton behind the `notif_routing_ready` build tag (added 2026-05-18).
The sub-scenario will fully activate once the SUT defines per-channel
notification routing builders in `pkg/subject/`.

## SUT subject contract

### §3.4a — in-app (already exists)

- **Frontdoor publish (loadgen):** `subject.MsgSend(account, roomID, siteID)`
  → `chat.user.{account}.room.{roomID}.{siteID}.msg.send`. Loadgen publishes
  a `model.SendMessageRequest` with a fresh `idgen.GenerateMessageID()`.
- **SUT consumes the canonical event** and produces one
  `model.NotificationEvent` per room subscriber (excluding the sender),
  publishing each on:
- **Per-account notification (SUT → loadgen):**
  `subject.Notification(account)` → `chat.user.{account}.notification`.
  Loadgen subscribes to this subject for every fixture account.
- **Notification payload:** `model.NotificationEvent{Type, RoomID, Message,
  Timestamp}` — loadgen reads `Message.ID` to correlate with the publish
  side.

### §3.4b — push/email (not yet defined)

The scenario expects these builders to appear in `pkg/subject/`:

- `subject.PushNotification(account string) string` — per-user push channel.
- `subject.EmailNotification(account string) string` — per-user email channel.

## Algorithm

### §3.4a tick loop (current implementation)

```
on startup:
    for each unique account in fixtures.Users:
        Subscribers().Subscribe(subject.Notification(account), handler)

per tick:
    sub = pickSubscription()
    msgID, reqID = idgen.GenerateMessageID(), idgen.GenerateRequestID()
    body    = "loadgen notification-fanout probe"
    if rand.Float64() < preset.MentionRate:
        body = "hey @<other> " + body
    publishedAt = time.Now().UTC()
    tracker.RecordPublished(msgID, publishedAt)  # record BEFORE publish
    Publisher.Publish(subject.MsgSend(sub.User.Account, sub.RoomID, siteID), req)

handler (per recipient, invoked once per notification on the subject):
    receivedAt = time.Now().UTC()
    evt = decode(NotificationEvent, msg.Data)
    if lag, ok := tracker.LagFor(evt.Message.ID, receivedAt); ok:
        notificationLag.WithLabelValues("inapp").Observe(lag.Seconds())
```

Notes:

- `RecordPublished` runs BEFORE the actual publish so a fast SUT cannot
  deliver before the tracker entry exists.
- `LagFor` is read-only — it does NOT consume the tracker entry — so the
  SAME message ID can be observed once per recipient. A publish with N
  fan-out recipients yields N histogram observations.
- The tracker is FIFO-bounded at 4096 entries to keep memory bounded on
  long runs; oldest entries are evicted when full.

### §3.4b extensions

Same as §3.4a but per-channel:

- Online users → `channel="inapp"` histogram.
- Offline, non-DND users → `channel="push"` histogram.
- Optional: email path → `channel="email"` histogram.
- DND users → no observation (delivery suppressed).

## Metrics

- `loadgen_notification_lag_seconds{channel="inapp"}` — in-app publish→receipt
  latency histogram. Buckets: exponential from 1 ms (×2 factor, 14 buckets →
  max ~16 s). **Cardinality: N × tick-count** where N is the per-room
  recipient count for each published message.
- `loadgen_notification_lag_seconds{channel="push"}` — push publish→receipt
  lag (§3.4b, gated on `notif_routing_ready`).
- `loadgen_notification_lag_seconds{channel="email"}` — email publish→receipt
  lag (§3.4b, gated on `notif_routing_ready`).

## Preset fields

| Field | Default | Description |
|---|---|---|
| `MentionRate` | 0.0 (0.10 for `realistic`) | Fraction of messages that carry an @mention; notification-worker fans out to all room members regardless, but the mention payload exercises mention-extraction code paths |
| `OfflineUserFraction` | 0.0 | Fraction of users treated as offline (§3.4b only) |
| `DNDUserFraction` | 0.0 | Fraction of users in Do-Not-Disturb (§3.4b only) |

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
4. Update this document's status to "ACTIVE".

## Operational notes

- The scenario requires fixture users to already be subscribed to rooms in
  Mongo so `notification-worker` can derive recipients via
  `ListSubscriptions(roomID)`. The standard `loadgen seed` for any
  messages-workload preset already provisions these — no new seed flag.
- Use the `realistic` preset (default) for representative mention rate and
  room shapes. Smaller presets (`small`, `tiny`) are fine for smoke runs
  but yield fewer recipients per publish, reducing histogram volume.
