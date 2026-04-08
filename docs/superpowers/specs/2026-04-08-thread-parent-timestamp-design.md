# Thread Parent Timestamp in SendMessageRequest

**Date:** 2026-04-08
**Status:** Approved

## Context

Thread replies already carry `ThreadParentMessageID` through the message pipeline. The Cassandra read model (`history-service/internal/models/message.go`) already has a `ThreadParentCreatedAt` field, but the write side never populates it. To efficiently look up a parent message in Cassandra (partition key: `room_id, created_at, id`), the parent's timestamp must travel with the message from the client.

## Scope

Changes are limited to:
- `pkg/model/message.go` — model structs
- `message-gatekeeper/handler.go` — validation and message construction
- `pkg/model/model_test.go` — model round-trip tests
- `message-gatekeeper/handler_test.go` — handler unit tests

`message-worker` Cassandra persistence is explicitly out of scope for this change.

## Design

### Approach

Add `ThreadParentMessageCreatedAt *time.Time` to both `SendMessageRequest` and `Message`. The client provides the value; the gatekeeper validates and propagates it. Use `*time.Time` (pointer) consistent with all other optional timestamp fields in `pkg/model` (`HistorySharedSince`, `EditedAt`, etc.).

### Model (`pkg/model/message.go`)

Add to `Message`:
```go
ThreadParentMessageCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty" bson:"threadParentMessageCreatedAt,omitempty"`
```

Add to `SendMessageRequest`:
```go
ThreadParentMessageCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty"`
```

Both fields are optional (`omitempty`) — absent for non-thread messages.

### Validation (`message-gatekeeper/handler.go`)

After content size validation, before subscription lookup:

```go
if req.ThreadParentMessageID != "" && req.ThreadParentMessageCreatedAt == nil {
    return nil, fmt.Errorf("threadParentMessageCreatedAt is required when threadParentMessageId is set")
}
```

`ThreadParentMessageCreatedAt` set without `ThreadParentMessageID` is ignored (no error) — the field has no meaning without an ID, but rejecting it would be overly strict.

### Message Construction (`message-gatekeeper/handler.go`)

Copy both thread fields from request to message:

```go
msg := model.Message{
    // ... existing fields ...
    ThreadParentMessageID:        req.ThreadParentMessageID,
    ThreadParentMessageCreatedAt: req.ThreadParentMessageCreatedAt,
}
```

Both fields flow through `MessageEvent` to `MESSAGES_CANONICAL` unchanged.

## Tests

### `pkg/model/model_test.go`
- `TestMessageJSON/with threadParentMessageCreatedAt` — round-trip with both thread fields set
- `TestSendMessageRequestJSON/with threadParentMessageCreatedAt` — round-trip with both thread fields set; verify omitted when nil

### `message-gatekeeper/handler_test.go`
- `"thread reply with ID and timestamp"` — happy path; verify both fields appear in returned `Message` and published `MessageEvent`
- `"thread parent ID without timestamp"` — expect non-infra validation error

## Not In Scope

- Persisting `ThreadParentMessageCreatedAt` in `message-worker` (separate task)
- Validating that the referenced parent message actually exists
- Changes to `history-service` read models (already have the field)
