# Room Key Sender Library Design

**Date:** 2026-03-31
**Status:** Approved

## Summary

A `pkg/roomkeysender` library that publishes a room's versioned encryption key pair to a specific user's NATS subject. Used when a user is added to a room (they need the current key to decrypt messages) and when a room key is rotated (existing members need the new key).

## Scope

The library is a thin publisher wrapper. It:

- Takes a username and a room key event, publishes it to the user's NATS subject
- Returns errors to the caller (no built-in retry)
- Does **not** determine recipients — the caller decides who receives the key

## Event Model

New struct added to `pkg/model/event.go`:

```go
type RoomKeyEvent struct {
    RoomID     string `json:"roomId"`
    VersionID  string `json:"versionId"`
    PublicKey  []byte `json:"publicKey"`  // 65-byte uncompressed P-256 point
    PrivateKey []byte `json:"privateKey"` // 32-byte scalar
}
```

`[]byte` fields automatically base64-encode when marshaled to JSON, consistent with the existing `roomcrypto.EncryptedMessage` pattern.

## NATS Subject

New builder in `pkg/subject/subject.go`:

```go
func RoomKeyUpdate(username string) string {
    return fmt.Sprintf("chat.user.%s.event.room.key", username)
}
```

Follows the existing `chat.user.{username}.event.*` convention. Clients already have subscribe permissions for `chat.user.{theirUsername}.>` via auth-service JWT scoping.

## Sender

### Publisher interface

Defined in the consumer package following the project convention of "define interfaces in the consumer, not the implementer":

```go
type Publisher interface {
    Publish(subject string, data []byte) error
}
```

This matches the identical `Publisher` interface used by `broadcast-worker`, `inbox-worker`, and `notification-worker`. In production, callers pass a `natsPublisher` adapter wrapping `*nats.Conn`.

### Sender struct

```go
type Sender struct {
    pub Publisher
}

func NewSender(pub Publisher) *Sender {
    return &Sender{pub: pub}
}

func (s *Sender) Send(username string, evt model.RoomKeyEvent) error {
    data, err := json.Marshal(evt)
    if err != nil {
        return fmt.Errorf("marshal room key event: %w", err)
    }
    subj := subject.RoomKeyUpdate(username)
    if err := s.pub.Publish(subj, data); err != nil {
        return fmt.Errorf("publish room key event: %w", err)
    }
    return nil
}
```

## File Layout

```
pkg/roomkeysender/
    roomkeysender.go        # Publisher interface, Sender struct, Send method
    roomkeysender_test.go   # Unit tests with mock publisher
```

## Testing Strategy

Unit tests use a mock publisher that captures the published subject and data:

- **Valid send:** Verify correct subject (`chat.user.{username}.event.room.key`) and payload round-trips through JSON correctly (roomID, versionID, public key, private key all preserved)
- **Publish error:** Verify the error from the publisher is wrapped and returned
- **Multiple users:** Verify each call produces a distinct, correctly-addressed subject

Table-driven tests with `t.Run` subtests, using `stretchr/testify/assert` and `testify/require` for assertions.

## Callers

This library will be consumed by:

1. **`inbox-worker`** — when processing a member invite, after creating the subscription, send the room key to the new member
2. **A future key rotation flow** — after rotating a room key in `roomkeystore`, iterate room members and send the new key to each

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Separate package from `roomkeystore` | Storage (Valkey) and distribution (NATS) are distinct concerns |
| No retry logic | Caller owns retry strategy; keeps library simple |
| No recipient lookup | Single-responsibility; caller determines who receives keys |
| `[]byte` for keys (not base64 string) | Matches `roomkeystore.RoomKeyPair`; JSON encoding handles base64 automatically |
| Subject uses `username` not `userID` | Consistent with system-wide migration to username-based subjects |
