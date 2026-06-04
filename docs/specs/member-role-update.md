# Spec: Member Role Update — Synchronous in `room-service`

> **Status:** Design approved, pending implementation on branch `claude/hopeful-newton-8Y8ln`.

*Collapse the asynchronous member-role-update flow (validate in `room-service` → `ROOMS` canonical stream → apply in `room-worker`) into a single synchronous request/reply handled entirely in `room-service`, structurally identical to the existing mute-toggle RPC. Role updates are infrequent and cheap, so the JetStream durability hop buys little and costs a round-trip plus a duplicate code path.*

---

## 1. Goal

The Update-Member-Role RPC is currently split across two services:

- `room-service.handleUpdateRole` validates the request, publishes a canonical message to the `ROOMS` stream, and replies `{"status":"accepted"}`.
- `room-worker.processRoleUpdate` later consumes that message, mutates Mongo (`AddRole`/`RemoveRole`), publishes the `subscription.update` event, and publishes the cross-site outbox event.

Because role changes are rare and cheap, the async machinery adds latency and a second code path without meaningful benefit. This spec makes the mutation **synchronous in `room-service`**, mirroring `handleMuteToggle` (`room-service/handler.go:1491`) — the existing precedent for "atomic subscription mutation → `subscription.update` event → cross-site outbox → reply" in the same service.

## 2. Data flow: before → after

```text
BEFORE
client ─▶ room-service.handleUpdateRole
            ├─ validate
            ├─ publish canonical → ROOMS (chat.room.canonical.{site}.member.role-update)
            └─ reply {"status":"accepted"}
                     │ (async)
                     ▼
         room-worker.processRoleUpdate
            ├─ AddRole / RemoveRole (Mongo)
            ├─ publish subscription.update (role_updated) → target
            └─ publish cross-site outbox (role_updated) [if target home site ≠ local]

AFTER
client ─▶ room-service.handleUpdateRole
            ├─ validate                                 (unchanged)
            ├─ SetOwnerRole (Mongo, atomic)             (new store method)
            ├─ publish subscription.update (role_updated) → target   (best-effort)
            ├─ publish cross-site outbox (role_updated) [if target home site ≠ local]
            └─ reply {"status":"ok"}
```

`room-worker` no longer participates in role updates.

## 3. `room-service` — `store.go` + `store_mongo.go`

### 3.1 New interface method

Add to the `RoomStore` interface, alongside `ToggleSubscriptionMute`:

```go
// SetOwnerRole atomically grants (makeOwner=true) or revokes (makeOwner=false)
// the owner role on the subscription keyed by (roomID, account) via a single
// FindOneAndUpdate. Other roles (e.g. member) are retained. Returns the
// updated subscription, or model.ErrSubscriptionNotFound (wrapped) when no match.
SetOwnerRole(ctx context.Context, roomID, account string, makeOwner bool) (*model.Subscription, error)
```

### 3.2 Mongo implementation

Mirror `ToggleSubscriptionMute`: aggregation-pipeline `FindOneAndUpdate`, `ReturnDocument: After`, filter `{roomId, u.account}`. The `$set` recomputes `roles` from `$roles` with **order-preserving** expressions so tests observe a deterministic array:

```go
// promote (makeOwner=true): append "owner" only if absent
{$cond: {if: {$in: ["owner", roles]}, then: roles, else: {$concatArrays: [roles, ["owner"]]}}}
// demote (makeOwner=false): drop "owner", then re-add "member" if absent
withoutOwner = {$filter: {input: roles, cond: {$ne: ["$$this", "owner"]}}}
{$cond: {if: {$in: ["member", withoutOwner]}, then: withoutOwner, else: {$concatArrays: [withoutOwner, ["member"]]}}}
```

The demote branch re-adds `"member"` exactly like the worker's old `AddRole(member)` before `RemoveRole(owner)`: a **channel creator is seeded `roles: ["owner"]` with no `"member"`** (`room-worker` `processCreateRoom`), so a plain `$filter` would demote it to `[]`. The `$cond` guard guarantees `["member"]` instead. `mongo.ErrNoDocuments` is wrapped as `model.ErrSubscriptionNotFound`.

## 4. `room-service` — `handler.go` (`handleUpdateRole`)

**Keep all validation unchanged** (`handler.go:622–680`): subject parse, request unmarshal + room-ID match, `errInvalidRole`, `errRoomTypeGuard` (channel-only), requester-is-owner (`errOnlyOwners`), `errTargetNotMember`, `errAlreadyOwner`, `errNotOwner`, `errPromoteRequiresIndividual`, and the last-owner self-demote guard (`errCannotDemoteLast`).

**Replace the publish-to-stream tail** (`handler.go:681–690`) with a mute-toggle-shaped tail that reuses the existing injected helpers (`publishCore`, `publishToStream`) and builders (`subject.SubscriptionUpdate`, `subject.Outbox`, `model.SubscriptionUpdateEvent`, `model.OutboxEvent`, `GetUserSiteID`):

1. `sub := h.store.SetOwnerRole(ctx, roomID, req.Account, req.NewRole == model.RoleOwner)`; map a wrapped `ErrSubscriptionNotFound` to `errTargetNotMember` (defensive TOCTOU).
2. Build `SubscriptionUpdateEvent{UserID: sub.User.ID, Subscription: *sub, Action: "role_updated", Timestamp: now}` and `publishCore` it to `subject.SubscriptionUpdate(req.Account)` — **best-effort** (log on failure; the DB write is the source of truth).
3. `GetUserSiteID(req.Account)`; if it differs from `h.siteID`, publish `OutboxEvent{Type: "role_updated", Payload: <the marshaled SubscriptionUpdateEvent>}` to `subject.Outbox(...)` via `publishToStream`.
4. Reply by reusing the existing `json.Marshal(map[string]string{...})` line, changing only `"accepted"` → `"ok"`. No new `model` response type — keeps the reply lean.

## 5. Federation — outbox contract & `inbox-worker`

- **Payload stays byte-compatible:** the `role_updated` outbox `Payload` is the marshaled `SubscriptionUpdateEvent`. `inbox-worker.handleRoleUpdated` (`handler.go:178`) decodes exactly this and calls `UpdateSubscriptionRoles(account, roomID, roles)`. Only the publish *site* moves (worker → service); the remote side is untouched.
- **Dedup ID dropped (deliberate):** the worker passed a per-message `natsutil.OutboxDedupID` to guard against JetStream redelivery of the canonical `ROOMS` message. That redelivery no longer exists — the handler runs once per request, and a client retry re-runs validation and stops at `errAlreadyOwner`/`errNotOwner` before any publish. Cross-site duplicate delivery stays harmless because `UpdateSubscriptionRoles` is an idempotent absolute `$set`. This matches the mute-toggle outbox path, which already publishes via `publishToStream` (signature `func(ctx, subj, data) error`, no dedup parameter). Reuse over reinvention: use it as-is rather than introducing `OutboxDedupID` into `room-service`.

## 6. `room-worker` — cutover

- `handler.go` — delete `processRoleUpdate` (lines 224–299) and the `case strings.HasSuffix(subj, ".member.role-update")` branch in `HandleJetStreamMsg`. A stray role-update message (e.g. a redelivery during the deploy window) then falls through to the existing `default` branch — logged and `Ack`'d. Rollout ordering (§11) prevents this in practice.
- `store.go` / `store_mongo.go` — remove the now-dead `AddRole` (only `processRoleUpdate` called it). **Keep `RemoveRole`** — still used by the remove-member dual-membership path (`handler.go:388`, stripping the owner role when an individual membership is removed but the user stays via org).

## 7. Error handling

- Reuse the existing sentinels and `sanitizeError` — **no new error types**.
- `SetOwnerRole` → `model.ErrSubscriptionNotFound` maps to `errTargetNotMember` (defensive against a TOCTOU removal between the validation read and the mutate).
- `publishCore` failure is **non-fatal** — log and continue (mute-toggle parity).
- `GetUserSiteID` / outbox `publishToStream` failure **returns an error** (mute-toggle parity).
- Accepted caveat: role-update is not idempotent (the guards reject re-promote/re-demote), so a client retry after a successful write returns `errAlreadyOwner`/`errNotOwner` rather than a second success — identical observable behavior to today's validation.

## 8. Client API — `docs/client-api.md`

Update the Update-Member-Role section (~471–541) in the same PR (required by CLAUDE.md for client-facing handler changes): from "async-job RPC, reply `accepted`, work happens in `room-worker`, no `AsyncJobResult`" to "synchronous RPC: `room-service` validates and applies the role change atomically, replies `{"status":"ok"}`, emits `subscription.update` (`role_updated`) to the target and a cross-site outbox event for remote targets." The event schema is unchanged.

## 9. Tests

### 9.1 Unit (`room-service/handler_test.go`)

Rewrite the role-update tests using the injected `publishCore`/`publishToStream` closures to capture published data (existing pattern). The mocked `RoomStore` gains `SetOwnerRole`.

- **Promote happy path** — `SetOwnerRole(makeOwner=true)`; `subscription.update` (`role_updated`) captured on `subject.SubscriptionUpdate(account)`; reply `{"status":"ok"}`; `publishToStream` guarded as "must not fire" (same-site → no canonical, no outbox).
- **Demote happy path** — `SetOwnerRole(makeOwner=false)`; same event/reply assertions.
- **Cross-site outbox** — `GetUserSiteID` returns a different site → `role_updated` outbox captured on `subject.Outbox(...)` with `Payload` decoding to a `SubscriptionUpdateEvent`.
- **All validation error paths preserved** — `errInvalidRole`, `errRoomTypeGuard`, `errOnlyOwners`, `errTargetNotMember`, `errAlreadyOwner`, `errNotOwner`, `errPromoteRequiresIndividual`, `errCannotDemoteLast`.
- **Store error** — `SetOwnerRole` → `ErrSubscriptionNotFound` ⇒ `errTargetNotMember`.
- **`publishCore` failure is non-fatal** — reply still `{"status":"ok"}`.

### 9.2 Integration (`room-service/integration_test.go`)

A real-Mongo promote-then-demote test asserting the resulting `roles` document state and the emitted `subscription.update` event.

### 9.3 `room-worker`

Remove the `processRoleUpdate` unit + integration tests and any `AddRole` mock expectations; confirm the remaining member-operation handlers still pass.

### Acceptance criteria

- `make fmt`, `make lint`, `make test`, `make sast` all green.
- `make generate` produces no mock drift (room-service gains `SetOwnerRole`; room-worker drops `AddRole`).
- `handleUpdateRole` no longer publishes to `subject.RoomCanonical(..., "member.role-update")`.
- Federation byte-compatibility: the `role_updated` outbox payload remains a marshaled `SubscriptionUpdateEvent`; `inbox-worker` is unchanged.
- `room-worker.RemoveRole` retained; `AddRole` and `processRoleUpdate` removed.
- `docs/client-api.md` updated; coverage ≥80% (target ≥90% on `handleUpdateRole`).

## 10. Mocks

`make generate SERVICE=room-service` (add `SetOwnerRole`) and `make generate SERVICE=room-worker` (drop `AddRole`). Never hand-edit `mock_store_test.go`.

## 11. Rollout

No stream or IaC changes — `ROOMS` still carries create/add/remove. Deploy order:

1. **`room-service` first** — stops producing the canonical role-update message; begins handling the RPC synchronously.
2. **`room-worker` second** — with the role-update case removed.

This guarantees no in-flight role-update message is dropped during the deploy window.

## 12. Out of scope

- No change to the request subject, request schema (`model.UpdateRoleRequest`), or validation rules.
- No change to the `subscription.update` event schema or the cross-site `role_updated` contract — only the publish site moves.
- `model.AsyncJobOpRoomMemberRoleUpdate` is left untouched — role-update never emitted an `AsyncJobResult`, so the constant is orthogonal to this change.

## 13. Decisions (for posterity)

- **Sync over async.** Role updates are low-frequency and cheap; the durability/retry of the JetStream hop is not worth the extra latency and the second code path. The mute-toggle RPC already established the synchronous-mutation pattern in this service.
- **Minimal reply `{"status":"ok"}`.** The requester is usually not the target (an owner changes someone else's role), so returning the target's full `Subscription` would expose its private fields (mute, `lastSeenAt`, `threadUnread`). A bare status avoids that and keeps the change small.
- **Dedup ID dropped** — justified in §5: no canonical-stream redelivery remains, and the remote apply is idempotent.
