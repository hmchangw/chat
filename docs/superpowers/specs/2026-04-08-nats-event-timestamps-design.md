# Add Timestamp Field to All NATS Events

**Date:** 2026-04-08
**Status:** Approved

## Problem

NATS event structs in `pkg/model/event.go` inconsistently include timestamps. Some events have timestamps indirectly via embedded domain structs (e.g., `MessageEvent` contains `Message.CreatedAt`), some have explicit `time.Time` fields (`RoomEvent.Timestamp`), and three have no timestamp at all (`InviteMemberRequest`, `OutboxEvent`, `RoomKeyEvent`). This makes event ordering, debugging, and observability harder than it should be.

## Decision

Every NATS event struct gets a top-level `Timestamp int64` field representing Unix milliseconds (set via `time.Now().UTC().UnixMilli()`). This is the **event-level timestamp** — when the event was published to NATS — distinct from domain-level timestamps inside embedded structs.

Additionally, a CLAUDE.md rule enforces this convention for all future events.

## Design

### 1. Event Struct Changes

All structs in `pkg/model/event.go`:

| Event Struct | Change |
|---|---|
| `MessageEvent` | **Add** `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |
| `RoomMetadataUpdateEvent` | **Add** `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |
| `SubscriptionUpdateEvent` | **Add** `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |
| `InviteMemberRequest` | **Add** `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |
| `NotificationEvent` | **Add** `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |
| `OutboxEvent` | **Add** `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |
| `RoomKeyEvent` | **Add** `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |
| `RoomEvent` | **Change** `Timestamp time.Time \`json:"timestamp"\`` to `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` |

### 2. Publisher Changes

Each service that publishes events sets the `Timestamp` field at publish time:

| Service | File | Event | Change |
|---|---|---|---|
| `message-gatekeeper` | `handler.go` | `MessageEvent` | Add `Timestamp: time.Now().UTC().UnixMilli()` |
| `broadcast-worker` | `handler.go` | `RoomEvent` | Change from `time.Time` to `time.Now().UTC().UnixMilli()` |
| `room-worker` | `handler.go` | `RoomMetadataUpdateEvent` | Add `Timestamp: time.Now().UTC().UnixMilli()` |
| `room-worker` | `handler.go` | `SubscriptionUpdateEvent` | Add `Timestamp: time.Now().UTC().UnixMilli()` |
| `room-worker` | `handler.go` | `OutboxEvent` | Add `Timestamp: time.Now().UTC().UnixMilli()` |
| `room-service` | `handler.go` | `InviteMemberRequest` | Add `Timestamp: time.Now().UTC().UnixMilli()` |
| `inbox-worker` | `handler.go` | `SubscriptionUpdateEvent` | Add `Timestamp: time.Now().UTC().UnixMilli()` |
| `notification-worker` | `handler.go` | `NotificationEvent` | Add `Timestamp: time.Now().UTC().UnixMilli()` |
| `pkg/roomkeysender` | `roomkeysender.go` | `RoomKeyEvent` | Add `Timestamp: time.Now().UTC().UnixMilli()` |

Where a handler already has `now := time.Now().UTC()`, reuse it: `Timestamp: now.UnixMilli()`.

### 3. CLAUDE.md Rule

Add under **Section 6: NATS & Messaging**:

> **Event Timestamps**
> - Every NATS event struct in `pkg/model` must include a `Timestamp int64 \`json:"timestamp" bson:"timestamp"\`` field
> - Set the timestamp at the publish site using `time.Now().UTC().UnixMilli()`
> - This is the event-level timestamp (when the event was published), distinct from any domain-level timestamps in embedded structs (e.g., `Message.CreatedAt`)

### 4. Test Changes

- Update existing handler tests in each service's `handler_test.go` to assert `Timestamp` is non-zero on published events
- Update `RoomEvent` tests from `time.Time` assertions to `int64` Unix millis assertions
- Update `pkg/model/model_test.go` round-trip fixtures to include `Timestamp` as `int64`
- No new test files — all changes in existing `_test.go` files

## Scope

- 8 event structs modified in `pkg/model/event.go`
- 9 publish sites across 7 services updated
- 1 CLAUDE.md rule added
- Existing tests updated, new assertions added
