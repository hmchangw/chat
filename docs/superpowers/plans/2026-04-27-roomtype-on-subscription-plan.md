# RoomType on Subscription Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `RoomType` field to `model.Subscription`, add the new `RoomTypeBotDM` and `RoomTypeDiscussion` constants, and populate `RoomType` at every subscription creation site and on the partial Subscription payloads carried by removed `SubscriptionUpdateEvent`s.

**Architecture:** Denormalise the room kind onto the subscription document so downstream consumers (frontend room-list categorization, notification rules, future per-type handling) can route on `Subscription.RoomType` without an extra room lookup. Each producer either reads the type from a fetched room or hardcodes `RoomTypeChannel` when only that type is reachable.

**Tech Stack:** Go 1.25, MongoDB driver v2 (`go.mongodb.org/mongo-driver/v2`), `go.uber.org/mock` (mockgen), `stretchr/testify` (assertions), NATS JetStream consumers via `pkg/idgen` and `pkg/subject`.

**Spec:** `docs/superpowers/specs/2026-04-23-roomtype-on-subscription-design.md`.

---

## Prerequisites

- Branch off `origin/main` after PR #118 ("Remove-member / role-update hardening") has merged. PR #118 already removed `RoomTypeGroup`, deleted `processInvite`, renamed `broadcast-worker`'s `publishGroupEvent` → `publishChannelEvent`, and updated `room-service`'s role-update guard. Do not redo any of that.
- All commands assume the repo root `/home/user/chat`. Use the `Makefile` targets — never raw `go` commands.
- Pre-commit hook runs lint + tests; commits fail if either fails.

## File Structure

| File | Responsibility | Touched in |
|---|---|---|
| `pkg/model/room.go` | Add new RoomType constants. | Task 1 |
| `pkg/model/subscription.go` | Add `RoomType` field on `Subscription`. | Task 1 |
| `pkg/model/model_test.go` | Assert new constants; round-trip the new field. | Task 1 |
| `room-service/handler.go` | Stamp `req.Type` onto the auto-created owner sub. | Task 2 |
| `room-service/handler_test.go` | Assert captured sub carries `RoomType`. | Task 2 |
| `room-worker/handler.go` | Hardcode `RoomTypeChannel` on `processAddMembers` subs; fetch room and stamp `RoomType` on the partial subs in `processRemoveIndividual` and `processRemoveOrg` event payloads. | Task 3 |
| `room-worker/handler_test.go` | Add `GetRoom` mocks + `RoomType` assertions on five existing tests. | Task 3 |
| `inbox-worker/handler.go` | Hardcode `RoomTypeChannel` on cross-site `member_added` sub. | Task 4 |
| `inbox-worker/handler_test.go` | Assert created sub has `RoomType: RoomTypeChannel`. | Task 4 |

No store interfaces change. `room-worker`'s `SubscriptionStore` already has `GetRoom`; no `make generate` needed.

## Tasks

1. **`pkg/model` foundation** — new constants + new `Subscription.RoomType` field + model tests. Backward-compatible: existing call sites keep compiling because the new field defaults to `""`.
2. **`room-service` CreateRoom** — owner subscription carries `req.Type`.
3. **`room-worker`** — three sub literals updated:
   - `processAddMembers`: hardcode `RoomTypeChannel`.
   - `processRemoveIndividual`: fetch the room, stamp `RoomType` on the partial sub literal carried by the "removed" `SubscriptionUpdateEvent`.
   - `processRemoveOrg`: same, with one room fetch reused across the per-account event loop.
4. **`inbox-worker`** — cross-site `handleMemberAdded` hardcodes `RoomTypeChannel` (only channel/discussion-style rooms ever produce cross-site `member_added` events, never DM/botDM).
