# Chat Frontend — Thread Side-Panel

**Status:** Design proposed, awaiting review
**Date:** 2026-05-13
**Scope:** `chat-frontend` only (no Go service changes)

## Summary

Add a right-hand thread panel to the chat frontend. Users open the panel
from a hover-revealed action on any message ("Thread" or "Reply" icon) or
from a "{tcount} replies" badge rendered on parent messages that already
have replies. The panel shows the parent message and its replies, lets the
user post new replies (which the backend already routes back as
broadcasts), and updates the parent's reply count in real time. Thread
replies are filtered out of the main feed.

The change also restructures the top-level layout: a new global `AppHeader`
and `Sidebar` are extracted out of `ChatPage`, leaving `ChatPage` as the
middle column of a three-column layout (sidebar / chat / thread).

## Motivation

Backend support for threads is already in place — `history-service`
exposes `chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread`,
`message-gatekeeper` accepts `threadParentMessageId` +
`threadParentMessageCreatedAt` on `msg.send`, and `broadcast-worker`
delivers thread replies on the existing `chat.room.{roomID}.event`
subject. The frontend currently ignores all of this: replies fall into the
main feed inline, `tcount` and `lastReplyAt` on parent messages go
unrendered, and there is no UI to start or read a thread.

A side-panel is the right surface because it keeps the parent's main-feed
context visible, matches user expectations from Slack/Teams, and lets the
panel and the main feed scroll independently.

## Goals

- Open a thread panel from each message via a hover action menu.
- Open a thread panel from a "{tcount} replies · {lastReplyAt}" badge on
  any parent message with `tcount > 0`.
- Show parent + replies (oldest-first) loaded via the `msg.thread` RPC.
- Send replies through the existing `msg.send` subject with thread
  parent fields populated. Optionally also send a copy of the reply
  into the main channel via the backend's `tshow` flag, controlled by
  an "Also send to channel" checkbox above the thread input.
- Quote-reply: from the message hover menu, a "Reply" action stages
  the hovered message as the quoted parent for the next send in the
  current input (main input when no thread is open or hovered
  message is a top-level message; thread input when hovering a reply
  inside an open thread). Uses the existing
  `quotedParentMessageId` field already supported by
  `message-gatekeeper`.
- Bump the parent message's `tcount` and `lastReplyAt` client-side
  whenever a `tshow: true` reply arrives in the main feed (the only
  thread reply the main subscription will ever see — backend drops the
  rest at the boundary).
- Refactor the layout so `Sidebar`, `ChatPage`, and `ThreadRightBar` are
  independent columns under a new `MainApp`.
- Keep `MessageArea` / `MessageInput` reusable across the main feed and
  the thread panel.

## Non-Goals

- A "Threads" tab that lists every thread the user participates in
  (would use `history-service` `msg.thread.parent` — explicitly out of
  scope; subject builder is **not** added in this change).
- Pagination beyond the first 50 replies (cursor plumbing is reserved
  but not wired to UI yet).
- Mark-as-read for threads (no read-receipt RPC for threads yet).
- Notifications / unread-count badges on the thread icon.
- Real-time delivery of other users' thread replies into an open
  thread panel — deferred to a separate "thread events" ticket. v1
  loads the thread via the `msg.thread` RPC on open and **optimistically
  appends the local user's own replies** after a successful publish.
  Other users' replies become visible the next time the thread is
  reopened.
- Encrypted-channel thread handling beyond the existing
  "skip-when-empty" path used by the main feed.

## Architecture

### Layout

```
<App>
  <NatsProvider>
    <AppContent>
      ├─ /oidc-callback  → <OidcCallback>
      ├─ !connected      → <LoginPage>
      └─ connected       → <RoomEventsProvider>
                            └─ <ThreadEventsProvider>          NEW
                                 └─ <MainApp>                  NEW
                                      ├─ <AppHeader>           NEW (global bar)
                                      └─ <main-row>            flex row
                                           ├─ <Sidebar>        NEW (room list + create)
                                           ├─ <ChatPage>       slimmed: middle column
                                           └─ <ThreadRightBar> NEW (mounted only when open)
```

- `AppHeader` holds global controls: global search, theme toggle, user
  chip, logout. Room-specific buttons (`Members`, `Leave room`) move out
  of the current `chat-header` into a new strip inside `ChatPage`.
- `Sidebar` is extracted from today's `.chat-sidebar` block in
  `ChatPage` (room list + "+ Create" + dialog mount). It is a fixed-width
  left rail.
- `ChatPage` becomes the middle column only: room-header strip,
  `RoomMessageArea`, `RoomMessageInput`, `InRoomSearch`.
- `ThreadRightBar` is a fixed-width right rail (~360–420 px). It mounts
  only while `activeThreadParent !== null`; when closed, the component
  is unmounted (no `display:none`) so the flex math is clean.
- `InRoomSearch` and `ThreadRightBar` are mutually exclusive right-rail
  occupants. Opening the thread closes in-room search; opening in-room
  search closes the thread.

### State management — new files

`src/lib/threadEventsReducer.js` (mirrors `roomEventsReducer.js` shape,
scoped to one open thread at a time):

```js
initialState = {
  activeParent: null,       // { roomId, siteId, messageId, createdAtMs } | null
  messages: [],             // replies, oldest-first
  hasLoadedHistory: false,
  historyError: null,
  nextCursor: null,         // reserved for future pagination
  hasNext: false,
  sendError: null,
  focusKey: 0,              // incremented on "Reply" click → input autofocus signal
}

actions:
  OPEN_THREAD       { parent, autoFocus }
  CLOSE_THREAD      {}
  HISTORY_LOADED    { parentId, resp }       // ignored if parentId !== activeParent.messageId
  HISTORY_FAILED    { parentId, error }
  REPLY_SENT_LOCAL  { message }              // optimistic append after our own publish
  SEND_FAILED       { error }
  RESET             {}                       // on logout / provider unmount
```

`appendBounded` and `mergeById` helpers are reused verbatim from the
existing reducer (extract them into a shared module under `src/lib/` if
they aren't already, or duplicate — implementer's call).

`src/context/ThreadEventsContext.jsx`:

- Wraps the reducer.
- On `activeParent` change: cancellable `request(msgThread(account,
  roomId, siteId), { threadMessageId, limit: 50 })` with the same
  `cancelledRef` + `generationRef` race-discard pattern used in
  `RoomEventsContext`.
- **No live subscription** in v1 — backend currently broadcasts thread
  replies only to subscribers of `tshow: true`-eligible main-feed
  events. Real-time delivery of other users' thread replies into an
  open panel is deferred (see Non-Goals).
- On successful `sendReply` publish: dispatch `REPLY_SENT_LOCAL` to
  optimistically append our own reply to the thread state, so the
  user sees what they just sent without reopening the thread.
- On unmount / `RESET` / `CLOSE_THREAD`: cancel in-flight requests.
- Exposes:

  ```js
  {
    activeParent, messages, hasLoadedHistory, historyError, focusKey,
    openThread({ roomId, siteId, messageId, createdAtMs }, { autoFocus }?),
    closeThread(),
    sendReply(content),
  }
  ```

  `sendReply(content, { tshow, quotedParentMessageId })` publishes
  `msgSend(account, roomId, siteId)` with the standard fields plus
  `threadParentMessageId`, `threadParentMessageCreatedAt`, and the
  optional `tshow` / `quotedParentMessageId` when set. The reply is
  **not** echoed locally — it round-trips through the broadcast
  subscription, same as the main feed today.

  `ThreadMessageInput` owns two pieces of local state above the
  textarea:
  - `alsoSendToChannel: boolean` — bound to a checkbox labelled "Also
    send to channel" rendered just above the textarea. Default `false`,
    reset to `false` after each successful publish, persisted across
    sends within the same thread open-session.
  - `quotedTarget: { id, content, sender } | null` — rendered as a
    dismissible chip above the textarea when set. Cleared on publish
    success or ✕.

  On submit: call `sendReply(content, { tshow: alsoSendToChannel,
  quotedParentMessageId: quotedTarget?.id })`. Gatekeeper note:
  `message-gatekeeper/handler.go:251` rejects quote-replies that
  cross thread boundaries (e.g. quoting a main-feed message from
  inside a thread that doesn't match the quoted message's thread). The
  UI prevents this by sourcing the quote target only from messages
  already in the same context (the routing rule under
  `MessageActions`); surface gatekeeper errors via `sendError` if they
  still occur.

### Cross-context: parent reply-count bumping

Backend rules (assumed contract, **no frontend filtering required**):

- `loadHistory`, `msg.show`, and the live broadcast for the main feed
  return only top-level messages plus thread replies whose `tshow ===
  true`. Non-`tshow` thread replies are not exposed via any main-feed
  API.
- `msg.thread` and per-message lookups by ID are the only paths that
  return arbitrary thread replies.

Given this contract, the main `roomEventsReducer.js` extension is
narrow:

- In `MESSAGE_RECEIVED`, append every message that arrives — no
  filtering, the backend has already done it. After the append, if
  `evt.message.threadParentMessageId` is set (which means this is a
  `tshow: true` thread reply), dispatch
  `THREAD_REPLY_OBSERVED { roomId, parentId, replyTimestampMs }`,
  which finds the parent in `roomState[roomId].messages` (and
  `focusBuffer` if present) and:
  - increments its `tcount` by 1,
  - updates `lastReplyAt` if `replyTimestampMs` is greater.
  If the parent isn't in the buffer, the action is a no-op (the
  authoritative value comes from the next history reload).
- `HISTORY_LOADED` does **not** filter anything — the backend already
  returns only the messages eligible for the main feed.

Limitation: thread replies sent with `tshow: false` never reach the
main feed, so the parent's `tcount` cannot be updated in real time for
those. The badge will lag until the next history reload of the room.
This is acceptable for v1 (live thread events are a follow-up ticket).

### NATS subject builders — additions to `src/lib/subjects.js`

```js
export function msgThread(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.msg.thread`
}
```

Reserved for later (not added now): `msgThreadParent`.

### Payload shapes

| Direction | Payload |
|---|---|
| Open thread (request) | `{ threadMessageId: <parentId>, limit: 50 }` |
| Open thread (response) | `{ messages: Message[], nextCursor?, hasNext }` — oldest-first |
| Send reply (publish) | existing `msg.send` payload + `threadParentMessageId` (20-char base62) and `threadParentMessageCreatedAt` (UTC ms). Optional `tshow: true` when the "Also send to channel" checkbox is set. Optional `quotedParentMessageId` when the user staged a quote-reply. |
| Main-feed send with quote (publish) | existing `msg.send` payload + `quotedParentMessageId` when a quote-reply was staged in the main input |
| Reply broadcast (subscribe) | Backend currently broadcasts on `chat.room.{roomId}.event` only top-level messages and thread replies with `tshow: true`. Non-`tshow` thread replies are not broadcast to the main subscription. Real-time delivery of arbitrary thread replies is a separate ticket. |

`threadParentMessageCreatedAt` derives from the parent's `createdAt` via
`new Date(parent.createdAt).getTime()`. Gatekeeper rejects either field
missing — never publish without both.

### Component refactor (presentational pattern — Approach A)

Today `MessageArea.jsx` calls `useRoomEvents(roomId)` internally and
`MessageInput.jsx` publishes via `useNats()` internally. Split each:

```
src/components/messages/
  MessageList.jsx          pure: messages, focusMessageId, onScrollReady
  MessageActions.jsx       NEW: hover row of "Thread" + "Reply" buttons
  MessageInputForm.jsx     pure: value, onChange, onSubmit, placeholder, disabled

src/components/
  RoomMessageArea.jsx      container: useRoomEvents(roomId)     → <MessageList/>
  RoomMessageInput.jsx     container: useNats() + msgSend       → <MessageInputForm/>
  ThreadMessageArea.jsx    container: useThreadEvents()         → <MessageList/>
  ThreadMessageInput.jsx   container: useThreadEvents().sendReply → <MessageInputForm/>
  ThreadRightBar.jsx       header strip + the two thread containers
  AppHeader.jsx            global bar (search/theme/user/logout)
  Sidebar.jsx              room list column
  MainApp.jsx              app shell (header + main row + providers)
```

`MessageList` keeps the existing render and focus-message scroll
behaviour but no longer owns load-history. The owning container drives
that effect with whatever state it has (`hasLoadedHistory`,
`onLoadHistory`).

`MessageInputForm` is the visual form. The container owns submit logic
so the same form can `msgSend` for the main feed or call `sendReply`
for the thread.

CSS lives in `src/styles/index.css` alongside the existing component
rules, using the same CSS-variable tokens already defined under
`src/styles/tokens.css`. New selectors: `.app-header`, `.app-sidebar`,
`.thread-rightbar`, `.thread-header`, `.message-actions`,
`.message-reply-badge`. No new stylesheet files.

`MessageActions` renders inside each message row in `MessageList`,
absolutely positioned top-right, revealed on row `:hover`:

- **Thread icon:** `openThread({ roomId, siteId, messageId,
  createdAtMs })`.
- **Reply icon:** stages the hovered message as the quoted parent for
  the next send in the *current* input. Routing rule:
  - If the message lives in the main feed (no `threadParentMessageId`
    on the message), stage it in the main `RoomMessageInput`.
  - If the message lives inside the currently-open thread (its
    `threadParentMessageId` equals `activeParent.messageId`), stage it
    in the `ThreadMessageInput`.
  - If the message is a thread reply but its thread isn't open, the
    Reply icon opens that thread first and then stages the quote in
    its input.

  Staging means: the target input's container holds a `quotedTarget`
  piece of state (`{ id, content, sender }`), renders a small dismissible
  chip above the textarea showing the quoted excerpt, and on submit
  includes `quotedParentMessageId: quotedTarget.id` in the `msg.send`
  payload. The chip is cleared after a successful publish or via its
  ✕ button.

### Parent-message reply badge

`MessageList` renders an inline pill beneath any message with
`tcount > 0`:

```
[💬  {tcount} {tcount === 1 ? 'reply' : 'replies'}  ·  {formatTime(lastReplyAt)}]
```

Click → `openThread(...)` (no auto-focus).

Field availability: the message Cassandra row carries `tcount`; whether
`lastReplyAt` is serialised to the client is verified during the Red
phase. If missing, the implementation falls back to client-side
maintenance only — the reducer keeps a per-message `lastReplyAt` derived
from observed `tshow: true` reply timestamps.

### Edge cases & teardown

- **Room switched while thread open:** if `selectedRoom.id` changes and
  `activeParent.roomId !== selectedRoom.id`, close the thread.
- **Selected room removed (kicked / left):** existing `ChatPage`
  cleanup also dispatches `CLOSE_THREAD`.
- **User logout:** `ThreadEventsContext` watches `user` from
  `useNats()`; when it goes null, dispatch `RESET` and cancel any
  in-flight RPC.
- **Stale request races:** same `generationRef` pattern as
  `RoomEventsContext` — drop responses from prior generations.
- **Duplicate replies:** thread reducer dedupes by `message.id` via the
  same `mergeById` semantics as the main feed (`REPLY_SENT_LOCAL`
  goes through the same merge so a duplicate cannot appear if a
  future events-ticket ever delivers the same reply back over a
  subscription).
- **Empty / encrypted broadcasts:** main reducer keeps the existing
  "skip when `evt.message` is empty" path; no thread-specific change.
- **InRoomSearch ↔ ThreadRightBar:** mutual exclusion enforced in
  `ChatPage` — opening one calls the closer of the other. Pressing
  Ctrl-F while a thread is open closes the thread first.
- **Reply arrives in the main feed for a closed thread:** because it
  is `tshow: true` (the only kind the main feed ever sees), it is
  appended to the main feed AND triggers the parent `tcount` bump.
- **Parent message edited / deleted while thread is open:** the thread
  panel renders the parent by looking it up by ID in the main
  `RoomEventsContext` buffer (live), and falls back to the
  `activeParent` snapshot only when the parent has scrolled out of
  the buffer. Existing `MESSAGE_EDITED` / `MESSAGE_DELETED` handlers in
  the main reducer keep the parent up to date; no extra wiring needed.
- **Re-clicking Thread on the already-open parent:** the
  `OPEN_THREAD` reducer branch detects `activeParent.messageId ===
  parent.messageId` and short-circuits — no new RPC, no buffer reset.
- **ChatPage ↔ ThreadEventsContext coupling:** `ChatPage` watches
  `useThreadEvents().activeParent` via `useEffect` and closes its
  in-room search panel when the value transitions from `null` →
  non-null. The opposite direction (opening in-room search) calls
  `closeThread()` from `useThreadEvents()`.
- **Edit / delete of thread replies:** out of scope for v1. Edits and
  deletes for thread replies are part of the deferred "thread events"
  ticket.

## Testing strategy

TDD per `CLAUDE.md`: Red tests for every new module before the
implementation. Coverage targets ≥ 80 % overall, ≥ 90 % on contexts and
reducers.

| New file | Tests |
|---|---|
| `lib/threadEventsReducer.js` | all actions; race-discard via generation/parentId mismatch; dedupe; optimistic append on `REPLY_SENT_LOCAL`; reset on logout |
| `context/ThreadEventsContext.jsx` | RPC fires once per open; cancels on close / unmount / logout; `sendReply` dispatches `REPLY_SENT_LOCAL` on publish success |
| `lib/roomEventsReducer.js` | extended — `MESSAGE_RECEIVED` appends every message unconditionally (no filter, backend already filters); when `message.threadParentMessageId` is set, additionally dispatch `THREAD_REPLY_OBSERVED` which bumps `tcount` + `lastReplyAt` on the parent in `messages` / `focusBuffer`, no-op when parent isn't buffered |
| `components/messages/MessageList.jsx` | pure render; reply-count badge renders only when `tcount > 0`; badge click fires `onOpenThread` |
| `components/messages/MessageActions.jsx` | hover-reveal, Thread opens panel, Reply stages quote into the right input per routing rule, `aria-label`s |
| `components/messages/MessageInputForm.jsx` | controlled form, submit + Enter handling, disabled state, optional quote-chip rendering, optional "Also send to channel" checkbox rendering when passed |
| `components/RoomMessageArea.jsx` / `RoomMessageInput.jsx` | wire to `RoomEventsContext` / `useNats()`; main input renders quote chip when staged; quote chip cleared on send success |
| `components/ThreadMessageArea.jsx` / `ThreadMessageInput.jsx` | wire to `ThreadEventsContext`; renders "Also send to channel" checkbox; `tshow` flag passed to `sendReply`; quote chip cleared on send success |
| `components/ThreadRightBar.jsx` | unmounts when closed; close button dispatches `CLOSE_THREAD`; mutual exclusion with `InRoomSearch` |
| `components/AppHeader.jsx`, `Sidebar.jsx`, `MainApp.jsx` | render-and-wire tests |
| `pages/ChatPage.jsx` | room-switch closes thread; logout clears state; existing tests adapt to slimmed layout |

Existing `MessageArea.test.jsx` and `MessageInput.test.jsx` are split
across the new presentational + container test files. No backend Go
tests — no backend changes.

## Out of scope / future work

- "Threads" tab listing all threads (would consume
  `chat.user.{account}.request.msg.thread.parent`).
- Reply pagination beyond the initial 50.
- Mark-as-read for threads + unread badges.
- Thread notifications (per-mention / per-subscription).
- Real-time delivery of arbitrary thread replies into an open thread
  panel — own ticket, owns the broadcast subject / consumer.
- Edits and deletes of thread replies — paired with the events ticket.

## Open questions

1. **`lastReplyAt` over the wire.** Verify whether the message payload
   delivered to clients (broadcast + history) actually carries
   `lastReplyAt`. If yes, render directly; if no, maintain a derived
   value in the reducer. *Resolution: Red-phase verification.*
2. **Quote-reply rendering inside `MessageList`.** The backend
   delivers `quotedParentMessage` as an embedded snapshot (see
   `pkg/model/message.go:22`). The existing `MessageList` does not yet
   render this snapshot. Adding a small "quoted card" above the
   message body in this PR vs. deferring it. *Recommended: render a
   minimal one-line quoted-card here so the feature is complete
   end-to-end; full styling can iterate later.*
