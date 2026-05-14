# User-Service Sidebar — Design

Reorganize the chat-frontend sidebar into three sections (Favorite, Apps, Channels and DMs) populated by two new NATS subjects served by `mock-user-service`. A third subject (`UserSubscriptionGetRooms`) is intentionally not wired — see "Open questions."

## Goals

- Render the sidebar as three sections, in this fixed order: **Favorite**, **Apps**, **Channels and DMs**.
- Source bucket membership from two new user-service RPCs:
  - `chat.user.{account}.request.user.{siteID}.subscription.getCurrent` with payload `{ "favorite": true }` → Favorite section
  - `chat.user.{account}.request.user.{siteID}.subscription.getApps` → Apps section
  - The **Channels and DMs** section is derived by elimination: every room in `summaries` that is neither favorited nor an app.
- Preserve every existing live behavior of the current sidebar: unread counts, mention badges, member-count badges, DM display names, and the reducer's recency ordering — applied per-section.

## Non-goals

- No backend changes. The subjects already exist (`pkg/subject/subject.go`) and are wired in `mock-user-service`.
- No new `Favorite` field on `model.Subscription`. The `favorite: true` flag is only a request payload, not a response field.
- No client-side favoriting UI (can't toggle favorite from the sidebar).
- No persistence of section-expanded state across reloads.
- No refresh button or background refetch. The two RPCs are called exactly once per login.
- No replacement of the existing `roomsList` seed. The new RPCs are additive.

## Architecture

The change is entirely in `chat-frontend/`. Three layers are touched:

1. **Subjects (`src/lib/subjects.js`)** — add three new builders mirroring the Go definitions (all three for completeness, even though only two are called by the bootstrap today).
2. **State (`src/lib/roomEventsReducer.js` + `src/context/RoomEventsContext.jsx`)** — extend the reducer with `favoriteIds: Set<string>` (frozen at login) and `appIds: Set<string>` (mutated on realtime events). Fire the two new RPCs in parallel with the existing `roomsList` call on login. Augment `ROOM_ADDED`/`ROOM_REMOVED` to maintain `appIds` (and to evict on remove).
3. **Rendering (`src/components/RoomList.jsx`)** — partition `summaries` into three sections and render three collapsible blocks.

`RoomEventsContext` remains the single source of truth for live room state (unread, mention, last-message-at, name, userCount). The new RPCs do not seed room metadata — they only decide bucket assignment.

### Bucket assignment rule

Each room is rendered in exactly one section. Favorite wins over Apps wins over Channels and DMs. All three sections iterate `summaries` in its existing recency order and filter by membership:

```
section('Favorite')         = summaries  filter (favoriteIds.has(id))
section('Apps')             = summaries  filter (appIds.has(id) AND NOT favoriteIds.has(id))
section('Channels and DMs') = summaries  filter (NOT favoriteIds.has(id) AND NOT appIds.has(id))
```

A room present in `summaries` but in neither bucket falls through naturally to Channels and DMs.

### App discriminator

For realtime re-bucketing on `subscription.update added` events, an "app" is identified by `roomType === 'botDM'`. All other room types (`channel`, `dm`, `discussion`) are regular rooms and need no tagging — the rendering elimination handles them.

### Bootstrap flow (login)

In the existing `RoomEventsContext` `useEffect` that runs on `user` becoming non-null, in addition to the current `roomsList` call, fire the two new RPCs in parallel:

```
const [favResp, appResp] = await Promise.all([
  request(userSubscriptionGetCurrent(user.account, user.siteId), { favorite: true }),
  request(userSubscriptionGetApps(user.account, user.siteId), {}),
])
dispatch({
  type: 'BUCKETS_LOADED',
  favoriteIds: favResp.subscriptions.map(s => s.roomId),
  appIds:      appResp.subscriptions.map(s => s.roomId),
})
```

The two calls and the existing `roomsList` call run concurrently. Rendering tolerates partial state (sections empty until rooms load; Favorite/Apps empty until `BUCKETS_LOADED`).

Each RPC failure is independent and silently leaves its set empty (no error dispatch). A failed bucket call only degrades sidebar grouping; rooms still render under Channels and DMs (the default bucket).

### Realtime updates

- `subscription.update` with `action: 'added'`, after the existing `roomsGet` enrichment, dispatch `ROOM_ADDED` (existing action). Reducer augmentation: if `room.type === 'botDM'`, add `room.id` to `appIds`. Otherwise no bucket change is needed — the room will appear in Channels and DMs via elimination. Never adds to `favoriteIds`.
- `subscription.update` with `action: 'removed'`, dispatch `ROOM_REMOVED` (existing action). Reducer augmentation: remove the roomId from both `favoriteIds` and `appIds`. (Removal from `summaries` is the existing behavior.)
- `favoriteIds` is **frozen at login**. A subscription becoming favorited (or un-favorited) on the server after login is not reflected until the next reload. This is an explicit non-goal.

### Rendering rules

- Sections render in fixed order: Favorite, Apps, Channels and DMs.
- **All three sections** iterate `summaries` in its existing recency order. A new message floats the room to the top of its section, exactly as today's flat `RoomList` does.
- Sections are collapsible. Expanded state is local `useState` in `RoomList`, defaulting to expanded, not persisted across reloads.
- Empty sections (zero matching rooms) are hidden entirely — no header, no placeholder.
- Selected-room highlighting, mention badge, unread badge, and userCount badge behave exactly as today, on a per-room basis regardless of section.

## Data model details

### `chat-frontend/src/lib/subjects.js`

Add three new exports (all three for parity with the Go side, even though `userSubscriptionGetRooms` is not called by the bootstrap):

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

Extend `initialState`:

```js
favoriteIds: new Set(),  // frozen at login
appIds:      new Set(),  // mutated on realtime events
```

Both are Sets because only membership is needed (rendering iterates `summaries` and filters).

New reducer action:

- `BUCKETS_LOADED { favoriteIds: string[], appIds: string[] }` — replaces `favoriteIds` and `appIds` with new Sets built from the supplied arrays.

Augmentations to existing actions:

- `ROOM_ADDED { room }` — in addition to existing behavior: if `room.type === 'botDM'` and `room.id` is not already in `appIds`, return a new `appIds` Set with `room.id` added. Never modifies `favoriteIds`.
- `ROOM_REMOVED { roomId }` — in addition to existing behavior: if `roomId` is in `favoriteIds`, return a new `favoriteIds` Set without it; same for `appIds`.
- `RESET` — also resets both Sets to new empty instances.

The reducer must always produce new Set instances on mutation (do not mutate in place) so React detects the change.

### `chat-frontend/src/context/RoomEventsContext.jsx`

In the bootstrap `useEffect`:

- After the existing `dmSub`, `subUpdate`, `metaUpdate` subscriptions are registered, fire the two new RPCs (in parallel with the existing `roomsList` call, not sequenced after it).
- On resolution, dispatch `BUCKETS_LOADED`. Wrap with the existing `cancelledRef` / `safeDispatch` guard.
- On rejection, log and continue. No `ROOMS_FAILED`-equivalent for buckets.

Expose a new hook `useSidebarSections()` from `RoomEventsContext.jsx` that returns `[{ key, title, rooms }]` in fixed section order — Favorite, Apps, Channels and DMs. The hook does the partition in a single pass over `summaries`:

```js
for (const room of summaries) {
  if (favoriteIds.has(room.id))      favorite.push(room)
  else if (appIds.has(room.id))      apps.push(room)
  else                               other.push(room)
}
return [
  { key: 'favorite', title: 'Favorite',          rooms: favorite },
  { key: 'apps',     title: 'Apps',              rooms: apps     },
  { key: 'other',    title: 'Channels and DMs',  rooms: other    },
]
```

Memoize on `[summaries, favoriteIds, appIds]` so the partition only re-runs when one of the three changes.

The existing `useRoomSummaries` hook is left unchanged so unrelated callers (e.g. `ChatPage.jsx`'s `summaries.find(...)` and `summaries.some(...)`) keep working.

### `chat-frontend/src/components/RoomList.jsx`

Replace the current flat `summaries.map` with a partition + three-section render driven by `useSidebarSections`:

```jsx
const sections = useSidebarSections()
const [collapsed, setCollapsed] = useState({})  // { [sectionKey]: boolean }
// for each section: skip if rooms.length === 0; otherwise render header + room rows
```

Each section header is clickable to toggle expand/collapse. CSS class names consistent with existing `room-list-*`: add `room-list-section`, `room-list-section-header`, `room-list-section-collapsed`.

## Files touched

- `chat-frontend/src/lib/subjects.js` — three new builders.
- `chat-frontend/src/lib/subjects.test.js` — tests for the three new builders.
- `chat-frontend/src/lib/roomEventsReducer.js` — `initialState` extension, `BUCKETS_LOADED` action, `ROOM_ADDED`/`ROOM_REMOVED`/`RESET` augmentations.
- `chat-frontend/src/lib/roomEventsReducer.test.js` — tests for the new action and augmentations.
- `chat-frontend/src/context/RoomEventsContext.jsx` — fire the two new RPCs on login, expose `useSidebarSections`.
- `chat-frontend/src/context/RoomEventsContext.test.jsx` — test that bootstrap fires the two subjects and dispatches `BUCKETS_LOADED`.
- `chat-frontend/src/components/RoomList.jsx` — partition + three-section render.
- `chat-frontend/src/components/RoomList.test.jsx` — tests for section partitioning, exclusivity, collapse toggle, hide-empty, recency ordering inside sections.
- `chat-frontend/src/styles/index.css` — add `.room-list-section`, `.room-list-section-header`, `.room-list-section-collapsed` rules alongside existing `.room-list-*` rules.

No backend files are touched. `docs/client-api.md` is **not** updated as part of this PR: the subjects were introduced server-side in PR #175 (mock-user-service) and any client-API documentation belongs with that PR, not with a frontend consumer change.

## Testing

All tests are unit tests, using the existing Vitest setup.

- **`subjects.test.js`** — for each new builder, assert the exact subject string for a sample `(account, siteId)`.
- **`roomEventsReducer.test.js`** —
  - `BUCKETS_LOADED` populates `favoriteIds` and `appIds` Sets with the supplied roomIds.
  - `BUCKETS_LOADED` replaces previous content (subsequent dispatch wins).
  - `ROOM_ADDED` with `room.type === 'botDM'` adds `room.id` to `appIds`.
  - `ROOM_ADDED` with `room.type` in `{channel, dm, discussion}` leaves both Sets unchanged.
  - `ROOM_ADDED` never modifies `favoriteIds`.
  - `ROOM_ADDED` for a roomId already in `appIds` is a no-op for the bucket.
  - `ROOM_REMOVED` removes the roomId from `favoriteIds` if present.
  - `ROOM_REMOVED` removes the roomId from `appIds` if present.
  - `ROOM_REMOVED` for a roomId in neither is a no-op for the buckets.
  - `RESET` empties both Sets.
- **`RoomEventsContext.test.jsx`** — mount the provider with a mocked `request`, assert that on login the two new subjects are requested with the documented payloads (`{ favorite: true }` and `{}`), and that `BUCKETS_LOADED` is dispatched with the response roomIds. Assert that the `getRooms` subject is **not** called. Failure of one RPC leaves the other's set populated.
- **`RoomList.test.jsx`** — with seeded `summaries`, `favoriteIds`, and `appIds`:
  - Three sections render in the fixed order when each has at least one room.
  - A favorited app appears under Favorite only.
  - A favorited channel appears under Favorite only.
  - An app appears under Apps only.
  - A channel/DM appears under Channels and DMs only.
  - A room in `summaries` but in neither bucket renders under Channels and DMs.
  - Empty sections are hidden (no header).
  - Clicking a section header toggles collapse; collapsed state hides items but keeps the header visible.
  - All sections preserve `summaries` recency order (verified by reordering `summaries` between renders and asserting each section reorders accordingly).
  - Existing room-item behavior (selected, unread, mention badge, userCount) renders correctly inside a section.

Coverage target: 90% for the touched files (matches the project's "core business logic" standard).

## Risks and trade-offs

- **Stale favorites:** `favoriteIds` is frozen at login. If a user favorites a room from another client, the sidebar does not reflect it until the next reload. Accepted per scoping. A future improvement would be a new `subscription.favorite.update` server event.
- **Three parallel RPCs on login:** `roomsList` + `getCurrent` + `getApps`. All run in parallel and none block rendering, so login latency is bounded by the slowest of the three. Acceptable.
- **App membership not maintained for non-botDM roomtype changes:** if a room's `roomType` could change at runtime (e.g. from `channel` to `botDM`), `appIds` would not catch the change. There is no event today for "roomType changed," and the assumption is that roomType is immutable post-creation.

## Open questions

- **Should `UserSubscriptionGetRooms` be called even though we don't use its response?** The current spec calls only `getCurrent` and `getApps` because the Channels-and-DMs section is derived by elimination from `summaries`. Calling `getRooms` would be a no-op roundtrip. The subject builder is exported for parity with Go, but no caller wires it. If the maintainer prefers calling all three explicitly (so the sidebar's data sources match the spoken "three subjects" contract), the bootstrap can add it back with a discarded response — please indicate during spec review.

All other clarifying points were resolved during brainstorming:

- Hybrid approach (keep `RoomEventsContext` live state, add bucket membership) — confirmed.
- Fetch-once-on-login, no refresh button — confirmed.
- Apps treated as rooms (clickable, open `MessageArea`) — confirmed.
- Bucket exclusivity, Favorite-wins — confirmed.
- App discriminator: `roomType === 'botDM'` — confirmed.
- Sections collapsible, hide-when-empty — confirmed.
- All sections use existing recency order — confirmed.
- Keep `roomsList` alongside the new RPCs — confirmed.
- Client-side bucket state is in-memory only; no server writes — confirmed.
