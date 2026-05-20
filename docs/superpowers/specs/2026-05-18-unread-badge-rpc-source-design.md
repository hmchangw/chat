# Unread Badge: RPC source

## Problem

`UnreadBadge` derives its number from `state.summaries` via `useUnreadTotal`
/ `selectUnreadTotal`. We want the badge fed by a backend RPC instead.

The real user-service is not in this repo, but this repo already has the
established "real backend not available" pattern: `mock-user-service`, a
dev-only Go service that answers user-service RPC subjects with hardcoded
responses (it already backs `subscription.getCurrent/getRooms/getApps`).
So the unread count follows that pattern — a real sync RPC op on the
frontend, served by a new `subscription.count` route on
`mock-user-service` — rather than a hardcoded stub inside the frontend op.

## RPC contract

- **Subject:** `chat.user.{account}.request.user.{siteId}.subscription.count`
- **Request:** `{ "unread": true }` (`unread` accepted and ignored by the mock)
- **Response:** `{ "count": 42 }` (mock returns a fixed `42`)

## Changes

### Backend (Go)

1. **Subject builders** — `pkg/subject/subject.go`:
   `UserSubscriptionCount(account, siteID)` and
   `UserSubscriptionCountPattern(siteID)`, mirroring
   `UserSubscriptionGetApps` (wildcard-token panic guard included).
   Added to all three `subject_test.go` tables.

2. **mock-user-service** — `mock-user-service/handler.go`: private
   `subscriptionCountReq { Unread bool }` / `subscriptionCountResp
   { Count int }` types, a `subscriptionCount` handler that site-checks
   and returns `{ Count: mockUnreadCount }` (`mockUnreadCount = 42`),
   registered as route #13 via `UserSubscriptionCountPattern`. Handler
   test covers happy path, ignored `unread` flag, and site mismatch.

3. **Docs** — `docs/client-api.md` gains route #13; the
   `mock-user-service` design doc's "rejected" note is superseded.

### Frontend

4. **Subject builder** — `src/api/_transport/subjects.ts`:
   `userSubscriptionCount(account, siteId)`, next to the other
   `userSubscription*` builders.

5. **api op** — `src/api/getUnreadCount/index.ts`, re-exported from
   `src/api/index.ts`. A normal sync request/reply op:
   `getUnreadCount({ user, request }: Nats): Promise<UnreadCountResponse>`
   → `request<UnreadCountResponse>(userSubscriptionCount(user.account,
   user.siteId), { unread: true })`. No hardcoded value in the frontend.

   `markRoomRead` now **returns a `Promise<boolean>`** and never
   rejects: `true` when the `message.read` reply was received (so the
   server-side `lastSeenAt` write has committed), `false` on transport
   error (nothing committed). Callers sequence post-read work only on
   `true`; `false` must NOT be treated as a read. Still safe to ignore
   for fire-and-forget callers.

6. **Reducer triggers** — `reducer.js` / `RoomEventsState`, two
   monotonic counters (init `0`), pure triggers, not derived data:
   - `msgRecvSeq` — incremented on every accepted `MESSAGE_RECEIVED`
     (any room; no-op paths — dup, thread reply, undecryptable —
     don't bump).
   - `readSeq` — incremented by a new `ROOM_READ_SYNCED` action,
     dispatched **only after a successful** `markRoomRead`
     (`resolve(true)` — server reply received, `lastSeenAt`
     committed). A failed mark-read (`false`) does not bump it.
     Bumped from both
     `setActiveRoom` (open-room read) and the active-room trailing
     mark-read in `useRoomSubscriptions`.

7. **Self-send no longer skipped** — `scheduleMarkActiveRead` used to
   `return` when the active-room message was sent by the current user.
   Removed: unread is server-derived as `lastMsgAt > lastSeenAt`, and
   sending advances `Room.lastMsgAt` but not the sender's `lastSeenAt`,
   so an own message in the active room must still mark it read or the
   badge counts the room you're sitting in. (Still trailing-debounced,
   so a chatty self-send is one RPC per burst.)

8. **Hook** — `src/context/RoomEventsContext/useUnreadCount.js`
   (context-local per frontend conventions). Signature
   `useUnreadCount(nats, readSeq, msgRecvSeq)`; the public
   `useUnreadCount()` wrapper feeds it `state.readSeq` /
   `state.msgRecvSeq`.
   - **Immediate** fetch on mount/reconnect (`nats` identity) and on
     `readSeq` bumps — i.e. **after** a read commits, not racing it.
   - **Debounced** refetch (trailing `500ms`) whenever `msgRecvSeq`
     bumps — a burst of incoming messages collapses to one
     `subscription.count` RPC. The seed value (`0`) is skipped so it
     doesn't double-fire on mount.
   - A monotonic request id makes the latest fetch win (slow earlier
     request, or one resolving post-unmount, is dropped).
   - Returns a plain `number` (`0` until the first fetch resolves).

9. **`UnreadBadge.jsx`** — consume `useUnreadCount()` instead of
   `useUnreadTotal()`. Drop `hasMention` and the `unread-badge--mention`
   variant (the RPC carries no mention info). Keep: hide when `<= 0`,
   `99+` cap, `aria-label`/`title`. `.chat-header-badge:empty` collapses
   the superscript wrapper at zero unread.

10. **Delete dead code** — `useUnreadTotal` (RoomEventsContext.tsx) and
    `selectUnreadTotal` (reducer.js), plus their tests.

## Bugs fixed (vs. the `lastMsgAt > lastSeenAt` rule)

- **Read-race / no resync.** The refetch used to fire on `activeRoomId`
  change, concurrently with `markRoomRead` — it could read the count
  before `lastSeenAt` committed and never re-pull. Now the refetch is
  keyed off `readSeq`, bumped only **after** the `markRoomRead` reply,
  so it's sequenced after the read.
- **Self-send inflation.** An own message in the active room never
  advanced `lastSeenAt` (backend doesn't on send; frontend skipped
  self-sender), so the badge counted the room you're in. Self-skip
  removed.
- **Residual (accepted):** an active-room message still bumps
  `msgRecvSeq` (debounced refetch) which may briefly show the count
  before that room's trailing mark-read lands; the subsequent `readSeq`
  refetch corrects it within ~the same second. Sub-second flicker, not
  persistent. The clean elimination is server-side (count handler
  treats the caller's open room as read, or send advances sender
  `lastSeenAt`) — out of repo.

## Testing

- `pkg/subject`: builder + pattern + wildcard-panic table entries.
- `mock-user-service`: `subscriptionCount` happy path, ignored flag,
  site mismatch.
- `api/getUnreadCount`: requests the `subscription.count` subject with
  `{ unread: true }` and returns the reply; propagates transport errors.
- `api/markRoomRead`: requests the `message.read` subject; resolves
  `true` on success and `false` (never rejects) on transport error.
- `reducer`: `msgRecvSeq` starts at 0; bumps on any accepted message;
  no-op on dup / thread-reply; preserved by non-message actions.
  `readSeq` starts at 0; `ROOM_READ_SYNCED` increments it; untouched by
  messages; preserved by other actions.
- `useUnreadCount`: fetches on mount; re-fetches on `readSeq` change;
  debounced 500ms refetch on `msgRecvSeq` bumps with bursts collapsed
  to one RPC; no refetch while `msgRecvSeq` stays 0; the debounce does
  not re-arm on reconnect when `msgRecvSeq` is unchanged; stale results
  dropped.
- `RoomEventsContext`: self-sent active-room message **does** fire
  `message.read` after the trailing debounce.
- `UnreadBadge.test.jsx`: hides at 0, renders count, caps at `99+`; no
  mention variant.
- Removed `selectUnreadTotal` reducer tests.

## Out of scope

- The real user-service (separate; `mock-user-service` stands in for dev).
- Mention-accent styling on the badge (dropped with this change).
