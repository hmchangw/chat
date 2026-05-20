# Frontend Read-Receipt Kebab Menu — Design

**Date:** 2026-05-13
**Status:** Draft
**Scope:** `chat-frontend/` only. No backend changes.

## 1. Problem

The backend already exposes a sender-only `message.read-receipt` RPC (PR #167; see
`docs/client-api.md` §Read Message Receipts). The web client currently has no UI to
invoke it. Users want to know who has read a message they sent.

## 2. Goal

When a room member opens a room and hovers a message **they sent**, surface a
vertical-3-dots ("kebab") menu. Clicking it opens a popover that contains a
`Read by X of Y` row, where:

- **X** = number of room members whose `lastSeenAt` is at or after the
  message's `createdAt`, **excluding the sender**. This is exactly
  `readers.length` from the RPC response. The count spans all sites: although
  `room-service` queries the local Mongo's `subscriptions` collection,
  `inbox-worker` mirrors remote `subscriptionRead` events into that collection
  (see `inbox-worker/handler.go:32`), so the result reflects readers across
  every site that hosts a room member.
- **Y** = total members in the room, **excluding the sender** —
  `max(0, room.userCount - 1)`. `room.userCount` is also a global count.

Hovering (or keyboard-focusing) the `Read by X of Y` row opens a sub-tooltip
listing each reader as `EngName ChineseName` (the Chinese name is appended only
when present).

The kebab is the first item of a future generic message-action menu; future
actions (Edit, Delete, Reply…) will be added as additional items inside the same
popover. This spec covers only the read-receipt item.

## 3. Non-goals

- No backend or `pkg/` changes — both the `message.read-receipt` and
  `message.read` RPCs ship as-is.
- No edit / delete / reply actions in this spec — only the menu shell that will
  host them later.
- No mobile/touch UX redesign. Hover semantics target desktop browsers; keyboard
  focus is supported for accessibility.

## 4. UX flow

1. Mouse over a message row in `MessageArea`. If `msg.sender.account ===
   user.account`, the kebab button (rendered top-right of the row) becomes
   visible. The kebab is also visible when keyboard-focused.
2. Click the kebab → popover opens beneath/next to it. Popover starts in a
   `Loading…` state.
3. The component fires the read-receipt RPC. On success the popover renders
   `Read by X of Y`. On error it renders the server error message.
4. Hovering or keyboard-focusing `Read by X of Y` (when X > 0) opens a
   sub-tooltip listing each reader. When X = 0 the row is non-interactive and no
   sub-tooltip appears.
5. Dismissal: click outside the menu container, press Escape, or click the
   kebab again. Closing resets `readers/error/loading` so the next open is a
   fresh fetch.
6. Pending/unsent messages: not applicable. `MessageInput` does not insert
   optimistic rows — it publishes and waits for the broadcast event. Every
   message in the buffer therefore has a server-confirmed id already.

The read-receipt query only returns a reader after that reader's
`subscription.lastSeenAt` has been advanced past the target message's
`createdAt`. To make that happen, `RoomEventsProvider` fires the
`message.read` RPC whenever the user activates a room and whenever a new
message lands in the currently-active room from a non-self sender (see §6.7).
Without this client-side wiring `X` would stay at 0 forever — the backend's
`message.read` handler exists precisely so the frontend can advance
`lastSeenAt`.

## 5. Architecture

### 5.1 New files

- `chat-frontend/src/components/MessageActionMenu.jsx` — exports
  `<MessageActionMenu message={msg} room={room} />`. Owns:
  - the kebab `<button aria-haspopup="menu" aria-expanded={open}>`,
  - popover open/close state,
  - read-receipt RPC lifecycle (`loading`, `error`, `readers`),
  - reader sub-tooltip,
  - click-outside / Escape dismissal.
- `chat-frontend/src/components/MessageActionMenu.test.jsx` — Vitest +
  `@testing-library/react` test suite (see §7).

### 5.2 Modified files

- `chat-frontend/src/lib/subjects.js` — add `readReceipt(account, roomId,
  siteId)` and `messageRead(account, roomId, siteId)` builders mirroring
  `pkg/subject/subject.go`:
  ```js
  export function readReceipt(account, roomId, siteId) {
    return `chat.user.${account}.request.room.${roomId}.${siteId}.message.read-receipt`
  }
  export function messageRead(account, roomId, siteId) {
    return `chat.user.${account}.request.room.${roomId}.${siteId}.message.read`
  }
  ```
- `chat-frontend/src/lib/subjects.test.js` — cases for the new builders.
- `chat-frontend/src/components/MessageArea.jsx` — for own-messages
  (`msg.sender?.account === user.account`), render `<MessageActionMenu>` inside
  the `.message` row. Pass `room` and the message in.
- `chat-frontend/src/components/MessageArea.test.jsx` — assert kebab presence
  for own messages and absence for others.
- `chat-frontend/src/context/RoomEventsContext.jsx` — wire the `message.read`
  RPC (see §6.7). `setActiveRoom(roomId)` and the DM/channel `new_message`
  subscribers call a `markRoomRead(roomId)` helper.
- `chat-frontend/src/context/RoomEventsContext.test.jsx` — cases for the
  `message.read` wiring (see §7.4).
- `chat-frontend/src/styles/index.css` — styles (see §5.4).

### 5.3 Data flow

```
MessageArea
  └─ for each msg
       └─ <div class="message">
            ├─ sender, time, content
            └─ MessageActionMenu  (own-messages only)
                 ├─ kebab button (CSS-hidden until row hover/focus)
                 └─ popover (open ⇒ rendered)
                      └─ read-receipt row
                           └─ reader sub-tooltip (hover/focus, X>0 only)
```

The component reads `user` and `request` from `useNats()`. RPC subject is
`readReceipt(user.account, room.id, room.siteId ?? user.siteId)`. Request body
is `{ messageId: msg.id ?? msg.messageId }` — the live `new_message` event
serialises the id as `id` (`pkg/model/Message`) but `msg.history` returns
`pkg/model/cassandra/Message` which uses `messageId`; both shapes coexist in
the message buffer (live tail vs. history). Response shape per
`docs/client-api.md` §Read Message Receipts:

```ts
type ReadReceiptEntry = {
  userId: string
  account: string
  chineseName: string
  engName: string
}
type ReadReceiptResponse = { readers: ReadReceiptEntry[] }
```

### 5.4 Styling

New CSS classes in `chat-frontend/src/styles/index.css`, using existing tokens
from `styles/tokens.css`:

- `.message-action-menu` — relative-positioned wrapper inside `.message`.
- `.message-action-kebab` — top-right of the row, `opacity: 0` until
  `.message:hover` or `.message-action-kebab:focus-visible` (and `:focus-within`
  on the menu so it stays visible while open).
- `.message-action-popover` — absolute-positioned panel anchored to the kebab.
- `.read-receipt-row` — text row inside the popover; `cursor: default` when
  X=0, `cursor: help` (or similar) when X>0.
- `.read-receipt-tooltip` — sub-tooltip; absolute-positioned next to the row;
  visible while the row is hovered/focused.

No new colour tokens; reuse the existing palette so the menu picks up
light/dark theme automatically (see PR #154).

## 6. Component behaviour spec

### 6.1 Reader name format

```js
function formatReaderName(r) {
  const eng = r.engName || r.account || ''
  return r.chineseName ? `${eng} ${r.chineseName}` : eng
}
```

`engName` is documented as always present, but we fall back to `account` so a
malformed entry can't render an empty `<li>`.

### 6.2 X / Y math

```js
const X = readers.length
const Y = Math.max(0, (room.userCount ?? 1) - 1)
```

`Math.max` guards the degenerate single-member room (notes-to-self) so we never
render `Read by 0 of -1`.

### 6.3 RPC lifecycle

- Triggered when `open` transitions `false → true` AND the message has a
  server-confirmed id.
- A new `AbortController`-style guard isn't needed because each open allocates a
  fresh promise; we discard results when the component is unmounted (tracked
  via a `mountedRef`) or when the menu has been closed before the response
  lands.
- Errors are surfaced verbatim from the RPC reply (`err.message`). They are
  already user-safe per backend convention (`pkg/model/ErrorResponse` via
  `natsutil.ReplyError`).

### 6.4 Optimistic / pending messages

Not applicable. `MessageInput.jsx` publishes via NATS and waits for the
`new_message` broadcast event to land in `roomEventsReducer`; no row is
inserted client-side before the server has confirmed the message. Every
message in `messages` carries a server-assigned `id`, so the RPC can always
be fired without a pending-state guard.

### 6.5 Dismissal

A `useEffect` registers a `mousedown` listener on `document` while `open` is
true. If the event target is not contained by the menu's root ref, set
`open=false`. A `keydown` listener closes on `Escape`. Toggling the kebab
closes when already open. Closing resets `loading/error/readers` to their
initial values.

### 6.6 Accessibility

- Kebab is a real `<button type="button">` with `aria-haspopup="menu"` and
  `aria-expanded`.
- Popover root has `role="menu"`.
- Read-receipt row is a `<button type="button" role="menuitem">` when X>0
  (so it's keyboard-focusable and triggers the sub-tooltip on focus). When
  X=0 it's a static `<div role="menuitem" aria-disabled="true">`.
- Reader list is rendered inside `.read-receipt-tooltip` as a `<ul>` of
  formatted names.

### 6.7 `message.read` wiring in `RoomEventsProvider`

This is cross-cutting state required for read-receipts to be meaningful — it
lives in `RoomEventsContext`, not the menu component.

A `markRoomRead(roomId)` helper inside the provider calls the `message.read`
RPC fire-and-forget:

```js
const markRoomRead = useCallback((roomId) => {
  if (!user || !roomId) return
  const summary = stateRef.current.summaries.find((r) => r.id === roomId)
  const siteId = summary?.siteId ?? user.siteId
  request(messageRead(user.account, roomId, siteId), {}).catch(() => {})
}, [user, request])
```

`markRoomRead` fires from two places:

1. `setActiveRoom(roomId)` — when `roomId` is non-null. Activating a room is
   the user's signal that they've seen everything up to "now"; the server
   sets `subscription.lastSeenAt = now`.
2. `new_message` subscribers (both the user-scoped DM subscription and each
   channel-scoped subscription) — when the event's `roomId` matches
   `stateRef.current.activeRoomId` **and** the sender is not the current user.
   This keeps `lastSeenAt` advancing while the user has the room open.

Self-read is filtered to avoid a meaningless extra round-trip: the sender's
own `subscription.lastSeenAt` is irrelevant to read-receipts (the sender is
always excluded from the result on the server side anyway).

Request body is `{}` — `docs/client-api.md` §Mark Messages Read says the
handler ignores any body content. Errors are swallowed; failure to update a
read receipt is not a user-facing problem.

The `useEffect` that owns the NATS subscriptions includes `markRoomRead` in
its dependency array because the maybeMarkActiveRead helper closes over it.

## 7. Test plan

### 7.1 `subjects.test.js`

- `readReceipt('alice', 'room1', 'site1')` returns
  `chat.user.alice.request.room.room1.site1.message.read-receipt`.
- `messageRead('alice', 'room1', 'site1')` returns
  `chat.user.alice.request.room.room1.site1.message.read`.

### 7.2 `MessageActionMenu.test.jsx`

Mock `useNats()` to inject a stub `{ user, request }`. Stub `request` returns
controllable promises so we can assert intermediate states.

Cases:

1. Kebab toggles popover open and closed.
2. Click outside the menu closes it; click on the menu does not.
3. Escape closes the menu.
4. Re-opening fires the RPC again (no caching across opens).
5. Loading state is rendered before the promise resolves.
6. RPC error message is rendered inline when the promise rejects.
7. `Read by X of Y` math:
   - 2 readers, `userCount=5` → `Read by 2 of 4`.
   - 0 readers, `userCount=3` → `Read by 0 of 2`.
   - 0 readers, `userCount=1` (sender alone) → `Read by 0 of 0`.
8. Empty readers (X=0): row present, but hovering/focusing it does **not**
   reveal the sub-tooltip.
9. Hover with X>0 reveals the sub-tooltip; mouse-leave hides it.
10. Keyboard focus on the row reveals the sub-tooltip; blur hides it.
11. Reader formatting:
    - both names present → `engName chineseName`
    - only engName → `engName`
    - only chineseName + account → `account chineseName` (fallback path)
12. Subject-builder integration: assert `request` is called with the subject
    string from `readReceipt(user.account, room.id, room.siteId)` and the
    payload `{ messageId: msg.id }`.
13. History-shape compatibility: a message with `messageId` but no `id`
    (the `pkg/model/cassandra/Message` JSON shape returned by `msg.history`)
    still produces `{ messageId: <id> }` in the RPC call.

### 7.3 `MessageArea.test.jsx`

- Own message → kebab is in the DOM (may be CSS-hidden; testing-library
  queries see it regardless of visibility).
- Other person's message → kebab is **not** rendered.
- Existing message-rendering tests continue to pass.

### 7.4 `RoomEventsContext.test.jsx`

Cases covering the `message.read` wiring described in §6.7:

1. `setActiveRoom('g1')` fires the `message.read` RPC on
   `chat.user.alice.request.room.g1.site-A.message.read` with body `{}`.
2. `setActiveRoom(null)` does **not** fire `message.read`.
3. A `new_message` event arriving in the active channel room fires
   `message.read`.
4. A `new_message` event arriving in a *non-active* room does **not** fire
   `message.read`.
5. A `new_message` event in the active room from the current user (self) does
   **not** fire `message.read` (self-read filter).

### 7.5 Out of scope

- No NATS integration test. The RPC itself is covered in `room-service`'s
  integration tests.

## 8. Known limitations

- **MAX_ROOM_SIZE cap.** Per `docs/client-api.md`, results are silently capped
  at `MAX_ROOM_SIZE`. No client-side mitigation.
- **No live updates.** Read receipts are point-in-time at popover open. If a
  user reads the message after the menu opened, the displayed count will not
  refresh until the user re-opens.
- **`docs/client-api.md` wording is stale.** That doc still calls the RPC
  "local-site only," but the implementation is global (see §2 X for the
  reason). The doc is not corrected here because `CLAUDE.md` only requires it
  to be updated by changes to a client-facing handler, which this PR does not
  touch.

## 9. Affected files summary

| File | Change |
|------|--------|
| `chat-frontend/src/components/MessageActionMenu.jsx` | New |
| `chat-frontend/src/components/MessageActionMenu.test.jsx` | New |
| `chat-frontend/src/components/MessageArea.jsx` | Render menu for own-messages |
| `chat-frontend/src/components/MessageArea.test.jsx` | Kebab visibility cases |
| `chat-frontend/src/context/RoomEventsContext.jsx` | Wire `message.read` on activate / new-msg-in-active |
| `chat-frontend/src/context/RoomEventsContext.test.jsx` | Cases for `message.read` wiring |
| `chat-frontend/src/lib/subjects.js` | Add `readReceipt` + `messageRead` builders |
| `chat-frontend/src/lib/subjects.test.js` | Cases for the new builders |
| `chat-frontend/src/styles/index.css` | Menu / popover / tooltip styles |

No backend, `pkg/`, or `docs/client-api.md` changes — both RPCs are already
documented (`message.read` in §Mark Messages Read; `message.read-receipt` in
§Read Message Receipts).
