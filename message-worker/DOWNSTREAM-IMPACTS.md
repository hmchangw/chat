# Downstream Impacts: `model.Message` Schema Expansion

## Context

Expanding `model.Message` from 6 fields to 22 fields replaces `UserID`/`Username` with `Sender` (`Participant`). This breaks services that construct or read those fields. These services are owned by other team members and will be updated separately.

---

## message-gatekeeper

**File:** `message-gatekeeper/handler.go:149-156`

Constructs `model.Message` when a validated message is published to MESSAGES_CANONICAL:
```go
msg := model.Message{
    ID:        req.ID,
    RoomID:    roomID,
    UserID:    sub.User.ID,        // ← breaks
    Username:  sub.User.Username,  // ← breaks
    Content:   req.Content,
    CreatedAt: now,
}
```

**Required change:** Construct `Sender: model.Participant{ID: sub.User.ID, UserName: sub.User.Username}` instead.

**Tests affected:** `message-gatekeeper/handler_test.go:89-91` — assertions on `msg.UserID` and `msg.Username`.

---

## notification-worker

**File:** `notification-worker/handler.go:57-60`

Reads `UserID` to skip notifying the sender:
```go
senderID := evt.Message.UserID           // ← breaks
for i := range subs {
    if subs[i].User.ID == senderID {     // skip sender
        continue
    }
}
```

**Required change:** Read `evt.Message.Sender.ID` instead of `evt.Message.UserID`.

**Tests affected:** `notification-worker/handler_test.go:69` — `UserID: "alice"` in test message construction.

---

## broadcast-worker

**File:** `broadcast-worker/handler.go:97`

Embeds `*model.Message` in `RoomEvent` for delivery:
```go
evt.Message = msg  // full Message struct serialized to subscribers
```

No direct field access to `UserID`/`Username` in handler logic — but the serialized JSON shape changes for all consumers of `RoomEvent.Message`.

**Tests affected:**
- `broadcast-worker/handler_test.go:58-59` — `UserID: "user-1"` in test message
- `broadcast-worker/integration_test.go:85-88, 120-121, 154-155, 191-193` — multiple test messages with `UserID`

---

## Summary

| Service | What breaks | Effort |
|---------|------------|--------|
| message-gatekeeper | Message construction (`UserID`/`Username` → `Sender`) | Small — 1 struct change + test updates |
| notification-worker | Sender ID read (`UserID` → `Sender.ID`) | Small — 1 line change + test updates |
| broadcast-worker | Test fixtures only (no handler logic change) | Small — test data updates |

All three are compile errors — no runtime surprises. The compiler will flag every location.
