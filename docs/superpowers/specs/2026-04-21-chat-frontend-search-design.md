# Design: Chat Frontend — Global Search UX

**Status:** Draft
**Depends on:** `2026-04-21-search-service-design.md` (backend NATS endpoints + wire schemas)

## Context

The backend `search-service` exposes two NATS request/reply endpoints — `chat.user.{account}.request.search.rooms` and `chat.user.{account}.request.search.messages` — with typed JSON request/response bodies. The chat frontend (React 19 + Vite + `nats.ws`) needs a search UX that surfaces both endpoints.

Inspiration: Microsoft Teams' top-header global search bar plus an in-room Ctrl+F quick-search.

This spec covers the frontend work only. The backend wire shape is already locked by the companion spec and consumed here verbatim.

---

## Goals

1. **Global search bar** in the top header (center), reachable from any view.
2. **Search-as-you-type** on rooms, with debounce + dropdown of top 5 matches.
3. **Enter** on the search bar expands to a full results view with tabs: **Rooms** (paginated) and **Messages** (triggered on tab click).
4. **Ctrl+F** within a room view opens an in-room message search scoped to that room's `roomId`.
5. Match existing frontend conventions (function components, hook-based state, `useNats` / `useRoomSummaries` contexts, kebab-case CSS).
6. Vitest coverage for new components and hooks.

## Non-Goals

- Server-side rendering / SSR — frontend is a SPA served by Vite.
- Keyboard navigation of dropdown (arrow keys, Enter to select) — basic click-only for MVP.
- Result highlighting — backend doesn't return highlight fragments (MVP gap); clients don't synthesize them either in MVP.
- Virtualized scrolling for very large result sets — plain scroll + "Load more" for MVP.
- Persistence of recent searches — ephemeral per session.
- Infinite-scroll on the dropdown — dropdown is capped at 5 items, full results only via Enter.

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Default search kind | Rooms | Fastest, local-only, most frequent UX. Messages tab opt-in. |
| Debounce | 250ms on input | Low enough to feel instant, high enough to avoid per-keystroke NATS requests. |
| Dropdown cap | 5 items | Matches user's ask; reinforces "this is a preview, press Enter for full results". |
| Enter behavior | Navigate to `SearchResultsPage` (routeless — modal/overlay) | Avoid URL routing churn; overlay dismissable with Esc. |
| Tab behavior | Rooms pre-loaded from the dropdown query; Messages fetched lazily on tab click | Don't spend user bandwidth on Messages until they ask. |
| Pagination | Scroll-to-bottom triggers `size + offset` load-more | Matches backend's offset+size pagination. |
| Local Ctrl+F | Modal overlay, scoped to current room via `roomIds: [selectedRoom.id]` | Keeps Ctrl+F familiar; uses same `search.messages` endpoint with request-side filter. |
| Ctrl+F binding | Intercept when a room is selected; fall back to browser default outside chat | Prevents hijacking browser find on login screen. |
| Error handling | Toast-style banner above results; empty-state text otherwise | Matches existing error patterns in `MessageArea`. |
| Request timeout | 5s NATS request timeout | Matches `history-service` convention; surfaces backend hangs quickly. |
| Result shape | Raw backend fields rendered as-is | No reshaping layer; trivial to update when backend adds fields. |

---

## UX Flows

### Flow A — Global room search-as-you-type

```
[type "des"]          [type "design"]         [200ms idle]
   │                      │                        │
   ▼                      ▼                        ▼
debounce                debounce                fires request
timer reset            timer reset            search.rooms
                                              { searchText: "design", size: 5, offset: 0 }
                                                     │
                                                     ▼
                                            dropdown shows up to 5 rooms
                                                     │
                                       ┌─────────────┴──────────────┐
                                       │                            │
                                click a room                press Enter
                                       │                            │
                                       ▼                            ▼
                             selectRoom in chat              open SearchResultsPage
```

### Flow B — Enter to full results

```
[Enter pressed with searchText="design"]
   │
   ▼
open SearchResultsPage (modal overlay)
   │
   ├─ Active tab: "Rooms" (pre-filled from dropdown query; offset=0; paginated)
   │      │
   │      ▼
   │  scroll to bottom → load more (offset += 25)
   │
   └─ Click "Messages" tab
          │
          ▼
   fire search.messages { searchText: "design", size: 25, offset: 0 }
          │
          ▼
   render message results; scroll to bottom → load more
```

### Flow C — Ctrl+F in-room search

```
[within ChatPage, selectedRoom != null, user presses Ctrl+F]
   │
   ▼
preventDefault() on browser Ctrl+F
open LocalMessageSearch modal (room-scoped)
   │
   ▼
user types; debounce; fires search.messages
   { searchText, roomIds: [selectedRoom.id], size: 25, offset: 0 }
   │
   ▼
render message results inline; Esc closes
```

---

## Component & File Layout

```
chat-frontend/src/
  components/
    GlobalSearchBar.jsx            NEW — header search input + dropdown
    GlobalSearchBar.test.jsx       NEW
    SearchResultsPage.jsx          NEW — modal overlay with Rooms/Messages tabs
    SearchResultsPage.test.jsx     NEW
    RoomResultsList.jsx            NEW — rooms tab content (paginated)
    RoomResultsList.test.jsx       NEW
    MessageResultsList.jsx         NEW — messages tab content (paginated)
    MessageResultsList.test.jsx    NEW
    LocalMessageSearch.jsx         NEW — Ctrl+F modal, room-scoped
    LocalMessageSearch.test.jsx    NEW
    (existing files unchanged except ChatPage + one style file)
  lib/
    subjects.js                    UPDATE — add searchRooms, searchMessages builders
    useDebounce.js                 NEW — generic debounce hook (400-ish LOC cap)
    useDebounce.test.js            NEW
    useSearch.js                   NEW — wraps NATS request/reply with timeout + cancellation
    useSearch.test.js              NEW
  pages/
    ChatPage.jsx                   UPDATE — mount GlobalSearchBar in header; bind Ctrl+F
  styles/
    search.css                     NEW — kebab-case class styles for all search components
```

---

## Component Specs

### `GlobalSearchBar.jsx`

**Props:** none (sources account from `useNats()`).

**Internal state:**
- `text` — current input value.
- `debouncedText` — debounced via `useDebounce(text, 250)`.
- `dropdownItems` — top-5 rooms from last fetch.
- `loading` — request in-flight indicator.
- `resultsOpen` — boolean; when true, renders `<SearchResultsPage>`.

**Behavior:**
- On `debouncedText` change (length >= 1): fire `search.rooms` with `size=5, offset=0`.
- On empty `text`: clear dropdown, close results.
- On Enter key: if `text` non-empty, set `resultsOpen=true` and initialize `SearchResultsPage` with the current `text`.
- On Esc key: close dropdown.
- On click outside: close dropdown.
- On dropdown item click: `useRoomSummaries().setActiveRoom(item.roomId)`, clear `text`, close dropdown.

**Accessibility:**
- `role="search"` on the container, `aria-label="Search chat"` on the input.
- Dropdown is a `role="listbox"`; items are `role="option"`.

---

### `SearchResultsPage.jsx`

**Props:**
- `initialSearchText: string`
- `onClose: () => void`

**Internal state:**
- `activeTab` — `"rooms"` | `"messages"`. Defaults to `"rooms"`.
- Per-tab: `items`, `total`, `offset`, `loading`, `error`.

**Behavior:**
- On mount: fetch Rooms tab with `{searchText: initialSearchText, size: 25, offset: 0}`.
- On Messages tab click (first time): fetch Messages with same shape.
- On scroll to bottom of tab content: fetch next page (`offset += 25`), append.
- On Esc: `onClose()`.

**Layout:** modal overlay covering the chat area; close button at top-right.

---

### `RoomResultsList.jsx` / `MessageResultsList.jsx`

Tab-content components, purely presentational. Props:
- `items`, `total`, `loading`, `error`, `onLoadMore`.

Renders a scrollable list; `IntersectionObserver` on a sentinel at the bottom triggers `onLoadMore` when visible and not already loading.

**Message result row** shows: sender account, room name (requires a lookup — see "Open Questions" below), snippet of `content` truncated at 200 chars, `createdAt` relative time.

**Room result row** shows: `roomName`, `roomType` badge (DM/Channel), `joinedAt` relative time.

---

### `LocalMessageSearch.jsx`

**Props:**
- `roomId: string`
- `roomName: string` (for header)
- `onClose: () => void`

**Internal state:** same as `GlobalSearchBar` minus `resultsOpen` — it's already a modal.

**Behavior:**
- Debounced `search.messages` with `roomIds: [roomId]`.
- Results rendered inline in the modal (not a separate tab — single-list view).
- Scroll-to-bottom load-more with `size + offset`.
- Esc / click-outside closes.

---

### `ChatPage.jsx` updates

1. Replace header `<div className="chat-header">` title with a three-column layout: logo on left, `<GlobalSearchBar />` in center, user + logout on right.
2. Add a `useEffect` that registers a `keydown` listener on `document`:
   ```js
   useEffect(() => {
     const onKey = (e) => {
       if ((e.ctrlKey || e.metaKey) && e.key === 'f' && selectedRoom) {
         e.preventDefault();
         setLocalSearchOpen(true);
       }
     };
     document.addEventListener('keydown', onKey);
     return () => document.removeEventListener('keydown', onKey);
   }, [selectedRoom]);
   ```
3. Mount `<LocalMessageSearch />` conditionally on `localSearchOpen`.

---

## Hook Specs

### `useDebounce(value, delayMs)`

Returns the latest `value` only after `delayMs` of no change. Standard pattern; cancels prior timer on every change; cleans up on unmount.

### `useSearch()`

Returns two async functions backed by `useNats()`'s NATS connection:

```js
export function useSearch() {
  const { nc, user } = useNats();
  const requestTimeout = 5000;

  async function searchRooms({ searchText, scope = 'all', size = 25, offset = 0 }) {
    const subject = subjects.searchRooms(user.account);
    const body = new TextEncoder().encode(JSON.stringify({ searchText, scope, size, offset }));
    const resp = await nc.request(subject, body, { timeout: requestTimeout });
    return JSON.parse(new TextDecoder().decode(resp.data));
  }

  async function searchMessages({ searchText, roomIds, size = 25, offset = 0 }) {
    const subject = subjects.searchMessages(user.account);
    const body = new TextEncoder().encode(JSON.stringify({ searchText, roomIds, size, offset }));
    const resp = await nc.request(subject, body, { timeout: requestTimeout });
    return JSON.parse(new TextDecoder().decode(resp.data));
  }

  return { searchRooms, searchMessages };
}
```

Error handling: NATS request timeout or error code surfaces as a thrown Error with `{code, message}` extracted from the backend `RouteError` JSON when possible; otherwise wrapped.

---

## `subjects.js` additions

```js
export function searchRooms(account) {
  return `chat.user.${account}.request.search.rooms`;
}

export function searchMessages(account) {
  return `chat.user.${account}.request.search.messages`;
}
```

Keep in sync with Go `pkg/subject` additions per the companion backend spec.

---

## Testing (Vitest + React Testing Library)

**Unit tests:**
- `useDebounce.test.js` — delays emissions correctly, cancels on rapid change, cleans up on unmount.
- `useSearch.test.js` — builds subject + body correctly; parses response; throws on timeout; throws typed errors on backend error codes.
- `subjects.test.js` — existing file; add cases for `searchRooms` / `searchMessages`.

**Component tests:**
- `GlobalSearchBar.test.jsx` — debounced fetch fires once per pause; dropdown renders items; click-outside closes; Enter opens results; Esc closes dropdown.
- `SearchResultsPage.test.jsx` — initial Rooms fetch happens on mount; clicking Messages triggers a fetch; scroll-to-bottom triggers load-more; error state renders.
- `RoomResultsList.test.jsx` / `MessageResultsList.test.jsx` — render items; loading state; error state; empty state.
- `LocalMessageSearch.test.jsx` — debounced fetch uses `roomIds: [roomId]`; Esc closes.

Mock `useNats().nc.request` with a stub that resolves/rejects on configured inputs — same pattern as existing `MessageArea.test.jsx`.

**Smoke test update:**
- Extend `smoke-test.mjs` to exercise one `search.rooms` call end-to-end against a live local stack with search-service running.

---

## Accessibility

- All input elements have `<label>` or `aria-label`.
- Modals (`SearchResultsPage`, `LocalMessageSearch`) use `role="dialog"` + `aria-modal="true"`; focus trapped while open; focus returns to the trigger on close.
- Dropdown uses `role="listbox"` + `aria-activedescendant` when a row is focused.
- Keyboard: Enter submits; Esc closes; Tab walks form controls in order. Arrow-key navigation of dropdown is deferred (non-goal).

---

## Known Gaps / Follow-Ups

- **Room name lookup for message results** — backend `MessageSearchHit` returns `roomId` but not `roomName`. Frontend currently has `useRoomSummaries()` which maps known rooms → names; for rooms the user isn't subscribed to (cross-site federated, stale, etc.) the fallback is to render the raw `roomId`. Long-term fix: extend backend hit to include `roomName` (joined at index-time via an additional field on the messages-* schema, or resolved by search-service on the return path).
- **Highlight fragments** — backend MVP doesn't return them; frontend doesn't synthesize (non-goal).
- **Keyboard navigation of dropdown** — arrow-key + Enter selection deferred.
- **Recent searches / history** — no persistence.
- **Push invalidation of UI-cached search results** — not applicable; results are per-session.
- **Cross-tab dedup** — results from Messages tab that happen to be in rooms also in the Rooms tab aren't grouped; each tab is independent.

---

## Decision Log

- **Why a modal overlay for full results, not a route?** Avoids URL routing churn in a small SPA. Matches CreateRoomDialog pattern already in the codebase. Trivially convertible to a route later.
- **Why default to Rooms, not Messages?** Rooms-as-you-type is the fastest, most common intent ("I want to go to the `design-team` room"). Messages is a directed verb ("I want to find that message from last week") and benefits from a more deliberate click.
- **Why 250ms debounce?** Empirically feels instant for fast typers, avoids per-keystroke churn on longer queries. Adjust if the search round-trip exceeds ~150ms consistently.
- **Why 5-item dropdown cap?** Matches user spec; reinforces the "preview" nature of as-you-type.
- **Why Ctrl+F over Cmd+F?** Use `ctrlKey || metaKey` to cover both platforms.
- **Why scoped search in Ctrl+F (`roomIds: [roomId]`) instead of a new endpoint?** The backend already supports `roomIds` filter; no new subject needed.
- **Why no virtualized list?** MVP result sets are small (max page = 100, typical query returns tens). Revisit only if profiles show slowness with long result histories.
