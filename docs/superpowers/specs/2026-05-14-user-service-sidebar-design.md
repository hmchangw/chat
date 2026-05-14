# User-Service Sidebar — Design

Reorganize the chat-frontend sidebar into three sections (Favorite, Apps, Channels and DMs) populated by three new NATS subjects served by `mock-user-service`.

## Goals

- Render the sidebar as three sections, in this fixed order: **Favorite**, **Apps**, **Channels and DMs**.
- Source bucket membership from three new user-service RPCs:
  - `chat.user.{account}.request.user.{siteID}.subscription.getCurrent` with payload `{ "favorite": true }` → Favorite
  - `chat.user.{account}.request.user.{siteID}.subscription.getApps` → Apps
  - `chat.user.{account}.request.user.{siteID}.subscription.getRooms` → Channels and DMs
- Preserve every existing live behavior of the current sidebar: unread counts, mention badges, recency ordering inside the section, member-count badges, DM display names.

## Non-goals

- No backend changes. The three subjects already exist (`pkg/subject/subject.go`) and are wired in `mock-user-service`.
- No new `Favorite` field on `model.Subscription`. The `favorite: true` flag is only a request payload, not a response field.
- No client-side favoriting UI (can't toggle favorite from the sidebar).
- No persistence of section-expanded state across reloads.
- No refresh button or background refetch of the three RPCs. They are called exactly once per login.
- No replacement of the existing `roomsList` seed. The new RPCs are additive.

## Architecture

The change is entirely in `chat-frontend/`. Three layers are touched:

1. **Subjects (`src/lib/subjects.js`)** — add three new builders mirroring the Go definitions.
2. **State (`src/lib/roomEventsReducer.js` + `src/context/RoomEventsContext.jsx`)** — extend the reducer with three roomId sets (`favoriteIds`, `appIds`, `otherIds`), and fire the three RPCs in parallel with the existing `roomsList` call on login. Augment `ROOM_ADDED`/`ROOM_REMOVED` to maintain bucket membership from realtime events.
3. **Rendering (`src/components/RoomList.jsx`)** — partition `summaries` against the three sets and render three collapsible sections.

`RoomEventsContext` remains the single source of truth for live room state (unread, mention, last-message-at, name, userCount). The three new RPCs do not seed room metadata — they only decide bucket assignment.

### Bucket assignment rule

Each room is rendered in exactly one section. Favorite wins over Apps wins over Channels and DMs. The partition is driven from the three ordered roomId arrays in the reducer:

```
section('Favorite')         = favoriteIds                                (in order)
section('Apps')              = appIds        minus  favoriteIds          (preserving appIds order)
section('Channels and DMs')  = otherIds      minus  favoriteIds, appIds  (preserving otherIds order)
```

For each roomId in a section, look up the matching entry in `summaries`. If the roomId is not yet in `summaries` (e.g. `BUCKETS_LOADED` resolved before `ROOMS_LOADED`), skip it — it will appear once the room arrives.

A room present in `summaries` but in none of the three arrays does not render in the sidebar. This should not occur in practice (`getRooms ∪ getApps` is expected to cover all subscriptions, and `ROOM_ADDED` augmentation routes new rooms by `roomType`), but is the deliberate consequence of "the three arrays are the source of bucket truth."

### App discriminator

For realtime re-bucketing on `subscription.update` events, an "app" is identified by `roomType === 'botDM'`. All other room types (`channel`, `dm`, `discussion`) go to Channels and DMs.

### Bootstrap flow (login)

In the existing `RoomEventsContext` `useEffect` that runs on `user` becoming non-null, in addition to the current `roomsList` call, fire the three new RPCs in parallel:

```
const [favResp, appResp, roomResp] = await Promise.all([
  request(userSubscriptionGetCurrent(user.account, user.siteId), { favorite: true }),
  request(userSubscriptionGetApps(user.account, user.siteId), {}),
  request(userSubscriptionGetRooms(user.account, user.siteId), {}),
])
dispatch({
  type: 'BUCKETS_LOADED',
  favoriteIds: favResp.subscriptions.map(s => s.roomId),
  appIds:      appResp.subscriptions.map(s => s.roomId),
  otherIds:    roomResp.subscriptions.map(s => s.roomId),
})
```

The three calls and the existing `roomsList` call run concurrently. The reducer accepts the buckets even before `ROOMS_LOADED` fires — the rendering tolerates partial state (summaries empty until rooms load; buckets empty until BUCKETS_LOADED).

Each RPC failure is independent and silently leaves its set empty (no error dispatch). A failed bucket call only degrades sidebar grouping; it does not block rooms from rendering (they fall through to Channels and DMs). This matches the "don't block login on user-service" posture.

### Realtime updates

- `subscription.update` with `action: 'added'`, after the existing `roomsGet` enrichment, dispatch `ROOM_ADDED` (existing action). Reducer augmentation: insert `room.id` into `appIds` if `room.type === 'botDM'`, else `otherIds`. Never inserts into `favoriteIds`.
- `subscription.update` with `action: 'removed'`, dispatch `ROOM_REMOVED` (existing action). Reducer augmentation: evict the roomId from whichever set holds it.
- `favoriteIds` is **frozen at login**. A subscription becoming favorited (or un-favorited) on the server after login is not reflected until the next reload. This is an explicit non-goal (see Non-goals).

### Rendering rules

- Sections render in fixed order: Favorite, Apps, Channels and DMs.
- **Within each section, items appear in the order the RPC returned them.** This is a deliberate departure from the existing flat `RoomList`, which orders rooms by recency from the reducer. To honor this, we store ordered roomId arrays (not just sets) in the reducer — see "Data model details" below.
- For rooms added at runtime via `ROOM_ADDED`, the roomId is appended to the end of its section's order array. For rooms removed via `ROOM_REMOVED`, the roomId is removed from its array.
- Sections are collapsible. Expanded state is local `useState` in `RoomList`, defaulting to expanded, not persisted across reloads.
- Empty sections (zero matching rooms) are hidden entirely — no header, no placeholder.
- Selected-room highlighting, mention badge, unread badge, and userCount badge behave exactly as today.

## Data model details

### `chat-frontend/src/lib/subjects.js`

Add three new exports:

```js
export function userSubscriptionGetCurrent(account, siteId) {
  return `chat.user.${account}.request.user.${siteId}.subscription.getCurrent`
}
export function userSubscriptionGetApps(account, siteId) {
  return `chat.user.${account}.request.user.${siteId}.subscription.getApps`
}
export function userSubscriptionGetRooms(account, siteId) {
  return `chat.user.${account}.request.user.${siteId}.subscription.getRooms`
}
```

Mirrors the Go builders at `pkg/subject/subject.go:465`, `:493`, `:472`.

### `chat-frontend/src/lib/roomEventsReducer.js`

Extend `initialState` with three **ordered roomId arrays** (not sets, because order matters per the rendering rules):

```js
favoriteIds: [],   // RPC order, frozen at login
appIds:      [],   // RPC order + append on ROOM_ADDED
otherIds:    [],   // RPC order + append on ROOM_ADDED
```

We use arrays even though membership lookup becomes `O(n)` per render. The arrays are bounded by the user's subscription count (small relative to message volume), and storing both an array and a derived Set is more state to keep in sync than is worth it.

New reducer action:

- `BUCKETS_LOADED { favoriteIds: string[], appIds: string[], otherIds: string[] }` — replaces all three arrays. Payloads are roomId arrays in the order returned by the RPC.

Augmentations to existing actions:

- `ROOM_ADDED { room }` — in addition to existing behavior: if `room.id` is not already in any of the three arrays, append it to `appIds` when `room.type === 'botDM'`, else append it to `otherIds`. Never appends to `favoriteIds`. The "already in any array" guard avoids double-add when an `added` event races with the initial bucket fetch.
- `ROOM_REMOVED { roomId }` — in addition to existing behavior, remove `roomId` from whichever of the three arrays contains it.
- `RESET` — also resets the three arrays to `[]`.

The reducer must always produce new array instances on mutation (do not mutate in place) so React detects the change.

### `chat-frontend/src/context/RoomEventsContext.jsx`

In the bootstrap `useEffect`:

- After the existing `dmSub`, `subUpdate`, `metaUpdate` subscriptions are registered, fire the three new RPCs (in parallel with the existing `roomsList` call, not sequenced after it).
- On resolution, dispatch `BUCKETS_LOADED`. Wrap with the existing `cancelledRef` / `safeDispatch` guard.
- On rejection, log and continue (no `ROOMS_FAILED`-equivalent for buckets).

Expose a new hook `useSidebarSections()` in `RoomEventsContext.jsx` that returns `[{ key, title, rooms }]` in fixed section order — Favorite, Apps, Channels and DMs. The hook does the partition by iterating each ordered roomId array and looking up the corresponding entry in `summaries` (skipping any roomId not yet in `summaries`, e.g. between `BUCKETS_LOADED` and `ROOMS_LOADED` resolving). Favorite-wins exclusivity is enforced by excluding any roomId already in `favoriteIds` from the Apps and Other passes, and likewise excluding `appIds` from the Other pass. The existing `useRoomSummaries` hook is left unchanged so unrelated callers (e.g. `ChatPage.jsx`'s `summaries.find(...)`) keep working.

### `chat-frontend/src/components/RoomList.jsx`

Replace the current flat `summaries.map` with a partition + three-section render. Implementation outline:

```jsx
const sections = [
  { key: 'favorite', title: 'Favorite',           rooms: favRooms   },
  { key: 'apps',     title: 'Apps',               rooms: appRooms   },
  { key: 'other',    title: 'Channels and DMs',   rooms: otherRooms },
]
// hide empty sections, render header + collapse toggle + rooms
```

Each section header is clickable to toggle expand/collapse. Use CSS class names consistent with the existing `room-list-*` classes; add `room-list-section`, `room-list-section-header`, `room-list-section-collapsed`.

## Files touched

- `chat-frontend/src/lib/subjects.js` — three new builders.
- `chat-frontend/src/lib/subjects.test.js` — tests for the three new builders.
- `chat-frontend/src/lib/roomEventsReducer.js` — initialState extension, `BUCKETS_LOADED` action, `ROOM_ADDED`/`ROOM_REMOVED`/`RESET` augmentations.
- `chat-frontend/src/lib/roomEventsReducer.test.js` — tests for the new action and the augmentations.
- `chat-frontend/src/context/RoomEventsContext.jsx` — fire three new RPCs on login, expose bucket sets (or a section hook).
- `chat-frontend/src/context/RoomEventsContext.test.jsx` — test that bootstrap fires the three RPCs and dispatches `BUCKETS_LOADED`.
- `chat-frontend/src/components/RoomList.jsx` — partition + three-section render.
- `chat-frontend/src/components/RoomList.test.jsx` — tests for section partitioning, exclusivity, collapse toggle, hide-empty.
- `chat-frontend/src/styles/index.css` — add `.room-list-section`, `.room-list-section-header`, `.room-list-section-collapsed` rules alongside the existing `.room-list-*` rules.

No backend files are touched. `docs/client-api.md` is **not** updated as part of this PR: the three subjects were introduced server-side in PR #175 (mock-user-service) and any client-API documentation belongs with that PR, not with a frontend consumer change.

## Testing

All tests are unit tests, using the existing Vitest setup.

- **`subjects.test.js`** — for each new builder, assert the exact subject string for a sample `(account, siteId)`.
- **`roomEventsReducer.test.js`** —
  - `BUCKETS_LOADED` populates the three arrays in the supplied order and replaces any previous content.
  - `ROOM_ADDED` with `room.type === 'botDM'` appends to `appIds`, not `otherIds`.
  - `ROOM_ADDED` with `room.type` in `{channel, dm, discussion}` appends to `otherIds`, not `appIds`.
  - `ROOM_ADDED` never appends to `favoriteIds`.
  - `ROOM_ADDED` for a roomId already present in any of the three arrays is a no-op for those arrays (no duplicate append).
  - `ROOM_REMOVED` removes from whichever of the three arrays holds the roomId; no-ops on the others.
  - `RESET` resets all three arrays to `[]`.
- **`RoomEventsContext.test.jsx`** — mount the provider with a mocked `request`, assert that on login the three new subjects are requested with the documented payloads (`{ favorite: true }`, `{}`, `{}`), and that `BUCKETS_LOADED` is dispatched with the response roomIds. Failure of any one RPC leaves the others' sets populated.
- **`RoomList.test.jsx`** — with seeded `summaries` and bucket arrays:
  - Three sections render in the fixed order.
  - A favorited app appears under Favorite only.
  - A favorited channel appears under Favorite only.
  - An app appears under Apps only.
  - A channel/DM appears under Channels and DMs only.
  - A room not in any set falls through to Channels and DMs.
  - Empty sections are hidden.
  - Clicking a section header toggles collapse; collapsed state hides items but keeps the header.
  - Existing room-item behavior (selected, unread, mention badge, userCount) still renders.

Coverage target: 90% for the touched files (matches the project's "core business logic" standard).

## Risks and trade-offs

- **Stale favorites:** `favoriteIds` is frozen at login. If a user favorites a room from another client, the sidebar does not reflect it until the next reload. Accepted per scoping. A future improvement would be a new `subscription.favorite.update` server event.
- **Four parallel RPCs on login:** `roomsList` + three user-service calls. All run in parallel and none block the rendering of the existing room list, so login latency is bounded by the slowest of the four. Acceptable.
- **Bucket-vs-summaries disagreement:** if `roomsList` and the three RPCs return different roomId sets at login (e.g., a subscription created between the two snapshots), a room may appear in `summaries` but not in any bucket array. It will not render in the sidebar until the next reload (or until a `subscription.update added` for that room arrives and `ROOM_ADDED` appends it to the appropriate bucket). Self-healing.
- **No recency reordering within a section:** today's flat `RoomList` orders rooms by recency (a new message floats the room to the top). The new sidebar uses RPC order + append-on-add and does **not** reorder on recency. This is the explicit Q6 choice. Unread/mention badges still work, so an unread room remains visible — it just doesn't jump position.
- **`getRooms ∪ getApps` assumption:** if the server ever returns overlapping or partial slices (e.g., `getRooms` returns apps too), the exclusivity rule still works because Favorite wins over Apps wins over Channels and DMs. Worst case: a room is bucketed slightly differently than expected on first paint; realtime re-bucketing converges to roomType-based assignment.

## Open questions

None. All clarifying points were resolved during brainstorming:
- Hybrid approach (keep `RoomEventsContext` live state, add bucket sets) — confirmed.
- Fetch-once-on-login, no refresh button — confirmed.
- Apps treated as rooms (clickable, open `MessageArea`) — confirmed.
- Bucket exclusivity, Favorite-wins — confirmed.
- App discriminator: `roomType === 'botDM'` — confirmed.
- Sections collapsible, RPC order within section, hide-when-empty — confirmed.
- Keep `roomsList` alongside the three new RPCs — confirmed.
- Client-side bucket state is in-memory only; no server writes — confirmed.
