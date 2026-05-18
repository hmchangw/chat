# Unread Badge: RPC source (hardcoded mock)

## Problem

`UnreadBadge` derives its number from `state.summaries` via `useUnreadTotal`
/ `selectUnreadTotal`. We want the badge fed by a backend RPC instead. The
real RPC lives in a local service that is not in this repo, so we ship a
hardcoded mock now and swap it for the live call later with a one-line edit.

## RPC contract

- **Subject:** `chat.user.{account}.request.user.{siteId}.subscription.count`
- **Request:** `{ "unread": true }`
- **Response:** `{ "count": 42 }`

## Changes

1. **Subject builder** — `src/api/_transport/subjects.ts`:
   `userSubscriptionCount(account, siteId)` →
   `chat.user.${account}.request.user.${siteId}.subscription.count`.
   Placed next to the other `userSubscription*` builders.

2. **api op** — `src/api/getUnreadCount/index.ts`, re-exported from
   `src/api/index.ts`.
   - `export interface UnreadCountResponse { count: number }`
   - `getUnreadCount({ user }: Nats): Promise<UnreadCountResponse>`
   - Builds the subject + `{ unread: true }` payload, but **returns a
     hardcoded `{ count: 42 }`**. The real call —
     `return request<UnreadCountResponse>(userSubscriptionCount(user.account, user.siteId), { unread: true })`
     — sits directly above, commented, so the swap is delete-mock /
     uncomment-one-line.

3. **Hook** — `src/context/RoomEventsContext/useUnreadCount.js`
   (context-local per frontend conventions).
   - Fetches `getUnreadCount(nats)` once when connected, and re-fetches
     when the active room changes (`state.activeRoomId`) — the same moment
     the old summaries count used to decrement on read. With the mock this
     re-fetch is a harmless no-op (always 42); with the real RPC it
     reflects the new count.
   - Returns a plain `number` (the unread count; `0` until the first
     fetch resolves). Guarded by the existing stale-cycle
     generation-counter pattern.

4. **`UnreadBadge.jsx`** — consume `useUnreadCount()` instead of
   `useUnreadTotal()`. Drop `hasMention` and the `unread-badge--mention`
   variant (the mock carries no mention info). Keep: hide when `<= 0`,
   `99+` cap, `aria-label`/`title`. Remove the now-unused
   `unread-badge--mention` rule from `style.css`.

5. **Delete dead code** — `useUnreadTotal` (RoomEventsContext.tsx) and
   `selectUnreadTotal` (reducer.js), plus their tests. Update
   `UnreadBadge.test.jsx` to mock `@/api`'s `getUnreadCount`.

## Testing

- `api/getUnreadCount` test: returns `{ count: 42 }` (mock behavior).
- `useUnreadCount` test: fetches on connect; re-fetches on
  `activeRoomId` change; stale cycles dropped.
- `UnreadBadge.test.jsx`: hides at 0, renders count, caps at `99+`; no
  mention variant.
- Remove `selectUnreadTotal` reducer tests.

## Out of scope

- The real backend RPC (separate local service, not this repo).
- Mention-accent styling on the badge (dropped with the mock).
