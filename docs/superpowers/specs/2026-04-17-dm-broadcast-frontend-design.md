# DM Broadcast Support in the Frontend — Design Spec

## Purpose

Make DM `new_message` events visible in the frontend and lift room-event handling out of individual components into a single app-level dispatcher. DM events flow on `chat.user.{account}.event.room` (per-recipient), while group events flow on `chat.room.{roomID}.event`. Today `MessageArea` subscribes only to the group subject, so DM rooms receive no live messages. The same dispatcher also drives unread counts and mention indicators in the room list.

## Scope

### In scope

1. App-level dispatcher that subscribes to both DM and group room events.
2. Bounded in-memory message cache per room (200 messages), populated by live events and by `msg.history`.
3. Unread count, `hasMention`, and `mentionAll` tracking per room.
4. Room list UI: unread dot, bold room name, unread count badge, mention pills.
5. Consolidating the rooms list fetch and subscription-update subscription into the new provider (removed from `RoomList`).

### Out of scope

- Theme / visual refresh (tracked separately).
- Persistence across reloads (`sessionStorage`, `localStorage`).
- Unread counts across backend restarts or across devices (server-side read receipts).
- Thread reply broadcast handling.
- Room-key events (`chat.user.{account}.event.room.key`).
- Backend changes — the broadcast-worker, subject builders, and event shapes stay as they are.

## Architecture

A new React context, `RoomEventsProvider`, is mounted between `NatsProvider` and `AppContent`.

```
<NatsProvider>
  <RoomEventsProvider>            <-- new
    <AppContent />
      LoginPage | ChatPage
        RoomList       (useRoomSummaries)
        MessageArea    (useRoomEvents)
  </RoomEventsProvider>
</NatsProvider>
```

Responsibilities of `RoomEventsProvider`:

- Open one DM subscription (`chat.user.{account}.event.room`) after login.
- Maintain a `Map<roomId, Subscription>` of group subscriptions, one per joined group room.
- Hold the rooms list (previously held by `RoomList`) and reconcile group subscriptions against it.
- Dispatch every incoming `new_message` event into a bounded message cache keyed by `roomId`.
- Track unread state per room and clear it when the room becomes active.
- Expose `useRoomEvents(roomId)` for `MessageArea` and `useRoomSummaries()` for `RoomList`.

`NatsContext` stays a pure transport layer (`connect`, `request`, `publish`, `subscribe`, `disconnect`). No behavior changes there.

## Data model

```text
roomState: {
  [roomId]: {
    messages: ClientMessage[],     // oldest → newest, bounded to MAX_CACHED = 200
    hasLoadedHistory: boolean,     // true after msg.history succeeds
    historyError: string | null,   // surfaced via useRoomEvents
    unreadCount: number,           // events received while roomId !== activeRoomId
    hasMention: boolean,           // sticky until markViewed
    mentionAll: boolean,           // sticky until markViewed
    lastMsgAt: string,             // ISO timestamp of newest message
    lastMsgId: string,
  }
}

summaries: Array<{
  id, name, type, siteId, userCount,
  lastMsgAt,
  unreadCount, hasMention, mentionAll,
}>   // sorted by lastMsgAt desc

activeRoomId: string | null
```

Invariants:

- Messages are deduplicated by `message.id`.
- When `messages.length > MAX_CACHED`, oldest entries are dropped.
- For the active room, incoming events do not increment `unreadCount` and do not set `hasMention` / `mentionAll`.
- `markViewed(roomId)` zeros `unreadCount` and clears the mention flags.
- `hasLoadedHistory` is set to `true` only on a successful history fetch; a failed fetch sets `historyError` but does not toggle `hasLoadedHistory`.
- Events for an unknown `roomId` still populate `roomState[roomId]` so transient gaps during join do not lose messages.

### Mention derivation

- **Group rooms:** the room event carries `mentionAll` and `mentions[]` (array of `Participant`). The current user `hasMention` iff `mentions[].account` contains the logged-in account. `mentionAll` is copied verbatim.
- **DM rooms:** the per-recipient event carries `hasMention` directly. `mentionAll` is always `false` for DMs.

## Subscription lifecycle

### DM subscription

Opened once inside `RoomEventsProvider` when `user` becomes non-null. Subject: `chat.user.{account}.event.room`. Every delivered event is treated as a `RoomEvent`; the dispatcher looks at `event.type === 'new_message'` and routes it to `roomState[event.roomId]`. Closed on logout / disconnect.

### Group subscriptions

The provider keeps `groupSubs: Map<roomId, Subscription>`. It reconciles this map with the user's room membership:

1. On login, after the DM subscription is live, the provider fetches the user's rooms via `chat.user.{account}.request.rooms.list`. For each room with `type === 'group'`, it opens a subscription to `chat.room.{roomID}.event` and adds it to `groupSubs`. DM rooms are not subscribed individually; they are covered by the DM subscription.
2. The provider subscribes to `chat.user.{account}.event.subscription.update`:
   - `added` + `type === 'group'` → open a group subscription for the new room; add the room to `summaries`.
   - `added` + `type === 'dm'` → add the room to `summaries` only.
   - `removed` → close and delete the matching entry in `groupSubs` (if any); delete `roomState[roomId]` and remove it from `summaries`.
3. The provider also subscribes to `chat.user.{account}.event.room.metadata.update` to keep `name`, `userCount`, and `lastMsgAt` in the summary fresh (same behavior `RoomList` has today).
4. On logout, iterate `groupSubs`, unsubscribe each, then clear the map along with `roomState`, `summaries`, and `activeRoomId`.

### Ownership of the rooms list

`RoomList` no longer fetches rooms or subscribes to subscription/metadata updates. It reads `summaries` from the provider. This eliminates the duplicate fetch between `RoomList` and the provider and keeps the room list and the message cache in agreement about which rooms exist.

## Component contracts

### `useRoomEvents(roomId)`

Consumed by `MessageArea`.

```js
{
  messages: ClientMessage[],          // oldest → newest
  hasLoadedHistory: boolean,
  historyError: string | null,
  loadHistory: () => Promise<void>,   // idempotent; no-op after first success
}
```

`MessageArea`'s existing direct calls to `subscribe()` and `request(msgHistory...)` are removed. On mount / when `room` changes, it calls `loadHistory()` and renders `messages`. Unread/mention clearing is driven by `setActiveRoom` from `ChatPage`, not by `MessageArea`. Auto-scroll-on-new-message behavior is unchanged.

### `useRoomSummaries()`

Consumed by `RoomList` and `ChatPage`.

```js
{
  summaries: Summary[],                        // sorted by lastMsgAt desc
  setActiveRoom: (roomId: string | null) => void,
  error: string | null,                        // surfaced from the initial rooms.list fetch
}
```

### `ChatPage` change

```js
const { setActiveRoom } = useRoomSummaries()
const handleSelectRoom = (room) => {
  setSelectedRoom(room)
  setActiveRoom(room?.id ?? null)
}
```

If the selected room disappears from `summaries` (removed subscription), `ChatPage` clears `selectedRoom` in a small effect.

## `loadHistory` behavior

- Issues `chat.user.{account}.request.room.{roomId}.{siteId}.msg.history` with `{ limit: 50 }` (unchanged).
- Reverses the response to ascending order.
- Merges into `roomState[roomId].messages` with dedup by `message.id`. Cached real-time messages received during the fetch are preserved.
- On success, sets `hasLoadedHistory = true` and clears `historyError`.
- On failure, sets `historyError` to the error message; `hasLoadedHistory` stays `false` so a later retry can succeed.
- Subsequent calls are no-ops when `hasLoadedHistory` is already `true`.

If `loadHistory` fails, the user can still send messages and see messages received since login (group subscriptions opened at login have been populating the cache). Only history older than the provider's cache is missing until history succeeds.

## Room list visual changes

```text
+--------------------------+
| Rooms                    |
|--------------------------|
| # general           2  3 |   <- unread count badge (hidden if 0)
| @ alice  (bold)     •    |   <- bold room name + dot for unread
| # backend  [@]      5  1 |   <- [@] pill when hasMention
| # all-hands [!]     88 12|   <- [!] pill when mentionAll
|--------------------------|
| [+ Create Room]          |
+--------------------------+
```

- Room name is bold and a dot is shown when `unreadCount > 0`.
- A numeric badge appears to the right of `userCount` when `unreadCount > 0`.
- A `[@]` pill appears next to the name when `hasMention` is true.
- A `[!]` pill appears next to the name when `mentionAll` is true; `[!]` takes precedence over `[@]` if both are set.
- All indicators clear when the room becomes active.

Styling stays in `src/styles/index.css`. New classes: `.room-item-unread`, `.room-badge-unread`, `.room-badge-mention`, `.room-badge-mention-all`. No new CSS libraries.

## File layout

```
chat-frontend/src/
  context/
    NatsContext.jsx            (unchanged)
    RoomEventsContext.jsx      <-- new provider + useRoomEvents + useRoomSummaries
  lib/
    roomEventsReducer.js       <-- new pure reducer (testable without React)
    subjects.js                <-- add userRoomEvent(account) builder
  components/
    RoomList.jsx               (refactored to consume useRoomSummaries)
    MessageArea.jsx            (refactored to consume useRoomEvents)
  pages/
    ChatPage.jsx               (calls setActiveRoom)
  App.jsx                      (wraps AppContent in RoomEventsProvider)
```

`src/lib/subjects.js` gains:

```js
export function userRoomEvent(account) {
  return `chat.user.${account}.event.room`
}
```

## Reducer shape (pure, testable)

`src/lib/roomEventsReducer.js` exports a pure reducer with these actions:

- `ROOMS_LOADED` — initial rooms list from `rooms.list`.
- `ROOM_ADDED`, `ROOM_REMOVED`, `ROOM_METADATA_UPDATED` — driven by `subscription.update` / `room.metadata.update`.
- `MESSAGE_RECEIVED` — a `RoomEvent` with `type: 'new_message'` (the reducer treats DM and group events the same; the dispatcher decides which action is `hasMention`).
- `HISTORY_LOADED` — merges a history response into `messages` with dedup.
- `HISTORY_FAILED` — records `historyError`.
- `SET_ACTIVE_ROOM` — updates `activeRoomId` and clears `unreadCount`, `hasMention`, `mentionAll` for the new active room.
- `RESET` — clears state on logout.

Action handlers are pure functions; the provider wires them to subscriptions and exposes `loadHistory` / `markViewed` / `setActiveRoom` as hooks.

## Error handling and edge cases

- `rooms.list` fetch failure → surfaced as `useRoomSummaries().error`; `summaries` stays empty until retry on next login.
- Group subscription open failure → log, skip that room's live updates; the room still appears in the list. Does not abort other subscriptions.
- `loadHistory` failure → `historyError` surfaces via `useRoomEvents`; `MessageArea` renders an inline error (same UI as today). Live events still flow.
- Duplicate message events (e.g., from reconnect replay) → dedup by `message.id`.
- Unknown `roomId` on an incoming event → still cached; renders only if a matching summary exists.
- Active room removed via `subscription.update` → `ChatPage` clears the selection.
- Cache overflow → drop oldest messages; `hasLoadedHistory` stays true so scrolling back doesn't retrigger history.
- Disconnect / reconnect → the NATS client auto-reconnects; subscriptions are re-established by `nats.ws`. Cached state is preserved across the blip.

## Testing

- **Reducer unit tests** (`src/lib/roomEventsReducer.test.js`) covering:
  - New group `new_message` with/without mention, with `mentionAll`.
  - New DM `new_message` with `hasMention` true/false.
  - Dedup by `message.id` when the same event is dispatched twice.
  - `unreadCount` increments only when `roomId !== activeRoomId`.
  - `SET_ACTIVE_ROOM` clears unread/mention for the new active room; leaves other rooms untouched.
  - `HISTORY_LOADED` merges ascending messages and preserves live-received ones.
  - `HISTORY_FAILED` sets `historyError` without flipping `hasLoadedHistory`.
  - Cache bound: more than `MAX_CACHED` messages drops oldest.
  - `ROOM_REMOVED` deletes `roomState[roomId]` and removes it from `summaries`.
- **Component test** for `RoomList` rendering summaries with combinations of `unreadCount`, `hasMention`, `mentionAll`; asserts classes and badges.
- **Component test** for `MessageArea` that stubs the provider and asserts it reads from `messages` without calling `subscribe` directly.
- **Smoke test update** (`smoke-test.mjs`): send a DM from user A; assert user B's client receives the event via the DM subscription (without opening the DM room).
- No changes to Go integration tests.

## Configuration

No new environment variables. `MAX_CACHED = 200` is a module-level constant in `roomEventsReducer.js`.

## Out-of-scope follow-ups

- Persistence of `lastViewedAt` to `sessionStorage`.
- Server-side read receipts / cross-device unread sync.
- Theme refresh (tracked as a separate brainstorm).
