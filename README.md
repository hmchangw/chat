# chat

A JetStream-to-NATS room router for a chat system. It consumes messages from a
durable JetStream stream and publishes them to per-room core NATS subjects for
real-time fan-out to connected clients.

## Architecture

```
                         JetStream (durable)              Core NATS (ephemeral)
                        ┌──────────────┐
  Producer A ──publish──▶              │              ┌──▶ chat.room.general ──▶ Subscribers
  Producer B ──publish──▶ chat.messages ├──room-router─┤
  Producer C ──publish──▶              │              └──▶ chat.room.lobby   ──▶ Subscribers
                        └──────────────┘
```

**Why two layers?**

| Layer | Purpose |
|-------|---------|
| JetStream (`chat.messages`) | Durable ingestion. Messages are persisted and can be replayed. Producers only need to know one subject. |
| Core NATS (`chat.room.<id>`) | Ephemeral fan-out. Subscribers (e.g. WebSocket gateways) listen to only the rooms they care about, with zero storage overhead. |

The router bridges the two: it reads from JetStream with at-least-once
delivery guarantees and publishes to the appropriate room subject on core NATS.

## Message Format

Messages are JSON on the `chat.messages` subject:

```json
{
  "room_id":    "general",
  "user_id":    "alice",
  "content":    "hello world",
  "timestamp":  "2026-03-16T12:00:00Z"
}
```

The `room_id` field determines the target core NATS subject: `chat.room.<room_id>`.

## Components

### Stream: `CHAT`

- **Subjects:** `chat.messages`
- Created automatically on startup via `CreateOrUpdateStream`.

### Consumer: `room-router`

- **Type:** Durable, pull-based (managed by the `jetstream.Consume` callback)
- **Ack policy:** Explicit
- Resumes from its last acknowledged position across restarts.

### Error Handling

| Scenario | Action | Effect |
|----------|--------|--------|
| Invalid JSON | `Term()` | Message is permanently discarded; will not be redelivered |
| Publish failure | `Nak()` | Message is redelivered by JetStream for retry |
| Successful route | `Ack()` | Message is marked as processed |

## Usage

### Prerequisites

- Go 1.24+
- A running NATS server with JetStream enabled

### Run

```sh
# Default: connects to nats://127.0.0.1:4222
go run .

# Custom NATS URL
NATS_URL=nats://my-nats:4222 go run .
```

### Test

Tests start an embedded NATS server — no external dependencies required.

```sh
go test -v ./...
```

### Test Cases

| Test | Verifies |
|------|----------|
| `TestRouterDeliversMessageToRoomSubject` | End-to-end: JetStream message arrives on the correct room subject with all fields intact |
| `TestRouterRoutesToCorrectRoom` | A message for `room2` is delivered only to `room2`, not `room1` |
| `TestRouterTerminatesInvalidJSON` | Malformed messages are terminated without blocking subsequent valid messages |
