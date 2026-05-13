# Chat Frontend — Thread Side-Panel

**Status:** Design proposed, awaiting review
**Date:** 2026-05-13
**Scope:** `chat-frontend` only (no Go service changes)

## Summary

Add a right-hand thread panel to the chat frontend. Users open the panel
via a hover-revealed Thread icon on any message, or via a "{tcount}
replies" badge rendered on parent messages that already have replies.
The panel shows the parent message and its replies (oldest-first),
lets the user post new replies, and optionally also broadcasts the
reply into the main channel via the backend's `tshow` flag from a
checkbox above the thread input.

A second hover action, Reply (quote), stages the hovered message as the
`quotedParentMessageId` for the next send. Quote-replies render
in-bubble (one shared message background containing both the quoted
excerpt and the new reply content) and click-jump to the original.

Two more hover actions, Edit and Delete, expose the existing edit /
delete RPCs from a single discoverable menu — usable in the main feed,
on the parent inside the thread panel, and on thread replies.

Layout is restructured: a global `AppHeader` and a left `Sidebar` are
extracted out of today's `ChatPage`, leaving `ChatPage` as the middle
column of a three-column row (sidebar / chat / thread).

## Motivation

Backend support for threads is already in place — `history-service`
exposes `chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread`,
`message-gatekeeper` accepts `threadParentMessageId` +
`threadParentMessageCreatedAt` on `msg.send`, and writes thread replies
with `tshow: true` into both the main and thread Cassandra tables. The
frontend currently has no UI to open or post in a thread, no UI to
quote-reply, and no Edit/Delete entry points after the layout split —
this PR closes all three gaps.

A side-panel is the right surface for threads because it keeps the
parent's main-feed context visible, matches user expectations from
Slack/Teams, and lets the panel and main feed scroll independently.

## Goals

- Open a thread panel from each message via a hover Thread icon.
- Open a thread panel from a "{tcount} replies" badge on any parent
  message with `tcount > 0` (no `lastReplyAt` timestamp — see Out of
  scope).
- Show parent + replies (oldest-first) loaded via the `msg.thread` RPC.
- Send replies through the existing `msg.send` subject with thread
  parent fields populated. Optionally also send a copy into the main
  channel via the backend's `tshow` flag, controlled by an "Also send
  to channel" checkbox above the thread input. Checkbox defaults off
  and **resets after every successful send**.
- Optimistically append the user's own replies; if publish fails, keep
  the row tagged as failed with a ⟳ retry button.
- Quote-reply from the hover Reply icon. Routing rules:
  - top-level message → stage in main input,
  - reply inside open thread → stage in thread input,
  - `tshow: true` thread reply in main feed with thread closed →
    Reply icon **disabled with tooltip** (gatekeeper would reject),
  - parent rendered inside its own open thread → Reply icon hidden.
- Render `message.quotedParentMessage` as a `QuotedBlock` inside the
  same bubble as the reply content; click-jump to original via the
  existing `focusMessageId` plumbing.
- Edit and Delete hover actions on own messages, everywhere (main
  feed, parent inside thread panel, thread replies). Inline edit
  (Enter saves, Esc cancels). Delete confirm dialog.
- Cross-context: when a `tshow: true` reply arrives in the main feed
  for the active parent, append it to the open thread panel in real
  time (via `TSHOW_OBSERVED`) and bump the parent's `tcount` (via
  `THREAD_REPLY_OBSERVED`, deduped).
- Refactor the layout so `Sidebar`, `ChatPage`, and `ThreadRightBar`
  are independent columns under a new `MainApp`.
- Keep `MessageList` / `MessageInputForm` reusable across both
  contexts via thin containers.

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
  messages: [],             // replies, oldest-first; each may have { _status: 'failed', _retryable: true }
  hasLoadedHistory: false,
  historyError: null,
  nextCursor: null,         // reserved for future pagination
  hasNext: false,
  sendError: null,
}

actions:
  OPEN_THREAD       { parent }
  CLOSE_THREAD      {}
  HISTORY_LOADED    { parentId, resp }            // ignored if parentId !== activeParent.messageId
  HISTORY_FAILED    { parentId, error }
  REPLY_SENT_LOCAL  { message }                   // optimistic append after our own publish
  REPLY_SEND_FAILED { messageId, error }          // tag the optimistic message as failed (retry-able)
  REPLY_RETRIED     { messageId }                 // clear failed marker before re-publish attempt
  TSHOW_OBSERVED    { message }                   // tshow:true reply observed in main feed for active parent → append into thread
  RESET             {}                            // on logout / provider unmount
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
- **No NATS subscription of its own** in v1. Backend live broadcasts
  for arbitrary thread replies are deferred (see Non-Goals).
- **Cross-context tshow pickup:** the provider subscribes (via a tiny
  observer registered on `RoomEventsContext`) to every
  `MESSAGE_RECEIVED` the main reducer processes. When the message has
  `threadParentMessageId === activeParent?.messageId` AND
  `tshow === true`, dispatch `TSHOW_OBSERVED` so the open thread panel
  reflects the new reply in real time. (The same message stays in the
  main feed; `mergeById` prevents duplicates if it later resurfaces via
  history reload.)
- On `sendReply` publish: append the optimistic reply via
  `REPLY_SENT_LOCAL` immediately (before awaiting NATS), then publish.
  On publish error, dispatch `REPLY_SEND_FAILED { messageId }` to tag
  the row as failed. `retryReply(messageId)` clears the failed marker
  (`REPLY_RETRIED`) and re-publishes.
- On unmount / `RESET` / `CLOSE_THREAD`: cancel in-flight requests and
  unregister the cross-context observer.
- Exposes:

  ```js
  {
    activeParent, messages, hasLoadedHistory, historyError,
    openThread({ roomId, siteId, messageId, createdAtMs }),
    closeThread(),
    sendReply(content, { tshow, quotedParentMessageId }),
    retryReply(messageId),
  }
  ```

  `sendReply(content, { tshow, quotedParentMessageId })` publishes
  `msgSend(account, roomId, siteId)` with the standard fields plus
  `threadParentMessageId`, `threadParentMessageCreatedAt`, and the
  optional `tshow` / `quotedParentMessageId` when set.

  `ThreadMessageInput` owns two pieces of local state above the
  textarea:
  - `alsoSendToChannel: boolean` — bound to a checkbox labelled "Also
    send to channel" rendered just above the textarea. **Defaults to
    `false` and resets to `false` after every successful publish**.
  - `quotedTarget: { id, content, sender } | null` — rendered as a
    dismissible chip above the textarea when set. Cleared on publish
    success or ✕.

  On submit: call `sendReply(content, { tshow: alsoSendToChannel,
  quotedParentMessageId: quotedTarget?.id })`. Gatekeeper note:
  `message-gatekeeper/handler.go:249` rejects quote-replies whose
  quoted message's `ThreadParentID` differs from the new message's
  `ThreadParentID`. The `MessageActions` routing rule prevents the UI
  from staging a quote that violates this; if a publish nonetheless
  fails (e.g. parent thread deleted out from under us), tag the
  optimistic row as failed (see retry flow) and surface the error via
  the failed-message UI.

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
  `evt.message.threadParentMessageId` is set (i.e. this is a
  `tshow: true` thread reply), dispatch
  `THREAD_REPLY_OBSERVED { roomId, parentId, replyId }`, which finds
  the parent in `roomState[roomId].messages` (and `focusBuffer` if
  present) and increments its `tcount` by 1, **deduped against a
  per-parent `Set` of observed reply IDs** so refreshes / re-renders
  don't double-bump. If the parent isn't in the buffer, the action is
  a no-op (the authoritative value comes from the next history reload).
- `HISTORY_LOADED` does **not** filter anything — the backend already
  returns only the messages eligible for the main feed. On hydration
  it also resets the per-parent observed-set for every loaded message.

Limitation: thread replies sent with `tshow: false` never reach the
main feed, so the parent's `tcount` cannot be updated in real time for
those. The badge will lag until the next history reload of the room.
This is acceptable for v1 (live thread events are a follow-up ticket).

The parent badge does **not** render a "last reply at" timestamp —
backend Message payload doesn't carry one and we explicitly chose to
drop it from v1 (see Out of scope).

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
  MessageList.jsx          pure: messages, focusMessageId, onJumpToMessage, onAction
  MessageRow.jsx           NEW: single message render — quoted-block bubble, content, MessageActions
  MessageActions.jsx       NEW: hover row of Thread + Reply + Edit + Delete buttons
  MessageInputForm.jsx     pure: value, onChange, onSubmit, quotedTarget, onClearQuote,
                                  extraTopRow? (used by thread input to render the checkbox)
  QuotedBlock.jsx          NEW: shared "sender + one-line ellipsized content" render,
                                  used both in-bubble (above reply body) and as the staging chip

src/components/
  RoomMessageArea.jsx      container: useRoomEvents(roomId)     → <MessageList/>
  RoomMessageInput.jsx     container: useNats() + msgSend; owns local quotedTarget
  ThreadMessageArea.jsx    container: useThreadEvents()         → <MessageList/>
  ThreadMessageInput.jsx   container: useThreadEvents().sendReply; owns local
                                  quotedTarget + alsoSendToChannel
  ThreadRightBar.jsx       header strip + ThreadMessageArea + ThreadMessageInput
  AppHeader.jsx            global bar — global search, theme toggle, user chip, logout
  Sidebar.jsx              left rail — RoomList + "+ Create room" at top
  MainApp.jsx              app shell (header + flex row + providers)
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
`.message-reply-badge`, `.quoted-block`, `.message-failed`. No new
stylesheet files. Thread panel width: **380 px fixed**.

### Hover action menu (`MessageActions`)

Absolutely positioned top-right of each `MessageRow`, revealed on the
row's `:hover` (CSS-only — no JS hover state). Buttons:

| Button | Visibility | Behaviour |
|---|---|---|
| **Thread** | Always | `openThread({ roomId, siteId, messageId, createdAtMs })`. Hidden when the row is the parent rendered inside its own open thread panel (you're already in it). |
| **Reply** (quote) | Conditional, see routing rule below | Stages the hovered message as the quoted target in the appropriate input. |
| **Edit** | Own messages only (`message.sender.account === currentUser.account`) | Enters inline-edit mode on the row: replace content with a textarea (Enter → publish edit RPC, Esc → cancel). Available everywhere: main feed, parent inside thread panel, thread replies. |
| **Delete** | Own messages only | Pops a small "Delete this message?" confirm dialog. On confirm, publish the existing delete RPC. Available everywhere: main feed, parent inside thread panel, thread replies. |

Reply routing rule:

1. Hovered message is **top-level** in main feed (`threadParentMessageId` empty): stage in `RoomMessageInput`. Always enabled.
2. Hovered message is a **thread reply** inside the currently-open thread (`threadParentMessageId === activeParent.messageId`): stage in `ThreadMessageInput`. Always enabled.
3. Hovered message is a **`tshow: true` thread reply** rendered in the main feed but **its thread is closed**: the gatekeeper rejects quoting it from the main input (`message-gatekeeper/handler.go:249`). Render the Reply icon **disabled with tooltip** "Open the thread to quote this reply." No click action.
4. Hovered message is the **parent** at the top of its open thread panel: Reply icon hidden (you'd quote the parent from inside its own thread, which is already the implicit context — quoting a parent from inside its own thread is itself a thread-boundary mismatch).

Staging means: the target input's container holds a `quotedTarget`
piece of state (`{ id, content, sender }`) and renders a `<QuotedBlock>`
chip above the textarea. Submit includes `quotedParentMessageId:
quotedTarget.id`. The chip clears on successful publish or via its ✕
button.

### Quoted-message rendering (`QuotedBlock`)

Single shared component used in two places, both with the same content
shape (sender top-line, single-line ellipsized content beneath):

**As staging chip above an input (`MessageInputForm`):**

```
┌─ (chip background) ────────────────────────┐
│  alice                                     │
│  this is the quoted message excerpt…    ✕  │
└────────────────────────────────────────────┘
```

Row 1: sender. Row 2: content left-justified, one line, ellipsized;
✕ right-justified clears `quotedTarget`. No bubble, no message
background — the chip sits in the input frame.

**As an in-bubble quoted block above a delivered reply (`MessageRow`):**

```
┌─ one message bubble ─────────────────────┐
│  alice                                   │
│  this is the quoted message excerpt…     │
│  ────────────────────────────────────────│
│  bob's reply content goes here,          │
│  spanning as many lines as it needs.     │
└──────────────────────────────────────────┘
```

The quoted block and the reply content share one message background —
they're part of the same bubble. Source: `message.quotedParentMessage`
(snapshot embedded by `message-gatekeeper` —
`pkg/model/message.go:22`).

**Click-to-jump:** clicking the in-bubble quoted block scrolls the main
feed to the original message. Reuses the existing `focusMessageId`
plumbing in `RoomEventsContext`. If the quoted message lives in a
different room or has scrolled out of the buffer beyond what we have
loaded, the click does nothing (no error, just a no-op) — fetching
arbitrary messages by ID for jump-target is deferred.

### Parent-message reply badge

`MessageRow` renders an inline pill beneath any message with
`tcount > 0`:

```
[💬  {tcount} {tcount === 1 ? 'reply' : 'replies'}]
```

Click → `openThread(...)`. No timestamp — backend Message payload
doesn't carry `lastReplyAt` (the only "last activity" field lives on
`ThreadRoom.LastMsgAt` and isn't fetched by this PR).

### Inline edit / delete UX

- **Inline edit** (Edit hover button): the `MessageRow` swaps its
  content area for a `MessageInputForm` pre-populated with the
  message's content. `Enter` publishes via the existing edit RPC and
  the row optimistically shows the new content; `Esc` cancels and
  restores the original. While editing, the hover menu is hidden on
  that row. Edit applies in the main feed, on the parent inside the
  thread panel, and on thread replies.
- **Delete confirm dialog** (Delete hover button): a small modal —
  "Delete this message? This cannot be undone." with `Delete` /
  `Cancel`. On confirm, publish the existing delete RPC. The local
  row is marked deleted optimistically and re-renders as a "Message
  deleted" placeholder; the authoritative broadcast eventually
  confirms.

The exact edit / delete subject builders and payload shapes are
inherited from today's chat-frontend implementation — the Red phase
verifies them in code, no new subjects added.

### Thread panel layout (`ThreadRightBar`)

```
┌─ "Thread"                                       ✕ ─┐  ← header (close button)
│                                                    │
│  alice  10:42       [hover menu: Thread Reply E D] │  ← parent message (rendered
│  the parent message body                           │     by MessageRow, same as
│                                                    │     anywhere else)
│                                                    │
│  bob    10:44                          [R E D]     │  ← reply 1 (oldest first)
│  …                                                 │
│                                                    │
│  alice  10:46                          [R E D]     │  ← reply 2
│  …                                                 │
│  ┌──────────────────────────────────────────────┐  │
│  │ ☐ Also send to channel                       │  │  ← checkbox, default off,
│  ├──────────────────────────────────────────────┤  │     resets after every send
│  │ Reply…                                       │  │  ← textarea; placeholder "Reply…"
│  └──────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────┘
```

- **No sticky parent.** The parent renders as the first item in the
  scrolling list. Rendered identically to other messages — no
  background tint, no "Parent" label — only the position differs.
- **Empty state** (parent has no replies yet): after the parent, render
  the line `"No replies yet — be the first to reply"` in muted text.
- **Auto-scroll.** On open, scroll to the bottom (newest reply or the
  parent if no replies). On every own optimistic append and every live
  `TSHOW_OBSERVED` append, scroll to bottom **only if the user is
  already pinned to bottom** (within ~20 px). Otherwise leave their
  scroll position alone.
- **Failed-reply UI.** A reply tagged `_status: 'failed'` renders with
  muted-error styling and a small ⟳ retry button. Clicking ⟳ calls
  `retryReply(messageId)`.
- **Hover actions on the parent** *inside* the thread panel: Thread
  hidden (you're already in it), Reply hidden (parent quote inside its
  own thread is a boundary mismatch), Edit + Delete present when
  parent is own message.

### AppHeader / room-header / Sidebar split

Locked in for v1:

- **AppHeader** (full-width, top): global search, theme toggle, user
  chip, logout button.
- **ChatPage room-header** (strip above `MessageList`): current room
  name, Members button, Leave-room button.
- **Sidebar** (left rail): `RoomList` + `+ Create room` button at the
  top.

Mobile / narrow viewports are explicitly out of scope.

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
- **Edit / delete of own thread replies:** the Edit/Delete hover
  actions work the same way on thread replies as on main-feed messages.
  Other users' edits/deletes don't propagate into the open thread panel
  in v1 (no live subscription); deferred to the events ticket.
- **Send failure (publish error):** the optimistic reply remains in
  the thread list, tagged `_status: 'failed'`, with a ⟳ retry button.
  Retry clears the marker and re-publishes. Closing the thread
  silently drops failed messages.
- **Live tshow pickup duplication:** when a `tshow: true` reply arrives
  for the active parent, both the main reducer (via `MESSAGE_RECEIVED`)
  and the thread reducer (via `TSHOW_OBSERVED`) append it — to their
  respective stores. The thread reducer dedupes by `message.id` so a
  later thread-history reload doesn't duplicate. The main reducer's
  `THREAD_REPLY_OBSERVED` dedup-set prevents double tcount bumps.
- **Disabled Reply tooltip:** `MessageActions` renders the Reply icon
  greyed with `title="Open the thread to quote this reply."` only when
  the routing rule resolves to case 3 (tshow reply in main feed with
  thread closed).

## Testing strategy

TDD per `CLAUDE.md`: Red tests for every new module before the
implementation. Coverage targets ≥ 80 % overall, ≥ 90 % on contexts and
reducers.

| New file | Tests |
|---|---|
| `lib/threadEventsReducer.js` | every action; race-discard via generation/parentId mismatch; dedupe via `mergeById`; `REPLY_SENT_LOCAL` optimistic append; `REPLY_SEND_FAILED` tags `_status='failed'` on the matching id; `REPLY_RETRIED` clears it; `TSHOW_OBSERVED` appends only for active parent and dedupes; `RESET` on logout |
| `context/ThreadEventsContext.jsx` | RPC fires once per `openThread`; same-parent re-open short-circuits; cancels on close / unmount / logout; `sendReply` optimistically appends, then publishes, then tags on error; `retryReply` re-publishes; cross-context observer registered against `RoomEventsContext` and unregistered on close |
| `lib/roomEventsReducer.js` | extended — `MESSAGE_RECEIVED` appends every message unconditionally; when `message.threadParentMessageId` is set, additionally dispatches `THREAD_REPLY_OBSERVED { roomId, parentId, replyId }` which bumps `tcount` on the parent and is deduped by per-parent observed-id Set; no-op when parent isn't buffered; `HISTORY_LOADED` resets the dedup Set |
| `components/messages/MessageList.jsx` | pure render; reply-count badge renders only when `tcount > 0`; badge click fires `onOpenThread`; empty-thread placeholder rendered when caller passes `isEmptyThread` |
| `components/messages/MessageRow.jsx` | renders `QuotedBlock` when `message.quotedParentMessage` set; in-bubble quote click fires `onJumpToMessage(originalId)`; failed-status row shows ⟳ retry; inline-edit mode swap on Edit action |
| `components/messages/MessageActions.jsx` | hover-reveal CSS; Thread always shown except on parent-in-its-own-thread; Reply routing rule cases 1–4 (incl. disabled+tooltip case 3); Edit/Delete only on own messages; `aria-label`s on every button |
| `components/messages/QuotedBlock.jsx` | renders sender + one-line ellipsized content; chip variant exposes `onClear` (✕); bubble variant exposes `onClick` for jump-to-original |
| `components/messages/MessageInputForm.jsx` | controlled form; submit on Enter; Shift+Enter newline; quote-chip rendered when `quotedTarget` set; `extraTopRow` slot used for thread checkbox; disabled state |
| `components/RoomMessageArea.jsx` / `RoomMessageInput.jsx` | wired to `RoomEventsContext` / `useNats()`; quote chip cleared on send success; main-input quote staged via `MessageActions` Reply case 1 |
| `components/ThreadMessageArea.jsx` / `ThreadMessageInput.jsx` | wired to `ThreadEventsContext`; "Also send to channel" checkbox defaults off and **resets after every successful send**; `tshow` and `quotedParentMessageId` passed through to `sendReply`; failed reply shows ⟳; ⟳ triggers `retryReply` |
| `components/ThreadRightBar.jsx` | header with ✕ that dispatches `CLOSE_THREAD`; ThreadRightBar unmounts entirely when no active parent; mutual exclusion with `InRoomSearch`; auto-scroll on open and on append-while-pinned |
| `components/AppHeader.jsx`, `Sidebar.jsx`, `MainApp.jsx` | render-and-wire: AppHeader holds search/theme/user/logout only; Sidebar holds RoomList + Create at top; MainApp wires the provider stack |
| `pages/ChatPage.jsx` | room-header strip renders room name + Members + Leave; room-switch closes thread; logout resets state; opening in-room search closes thread and vice versa; existing tests adapt to slimmed layout |
| Inline edit / delete | per-row tests: Edit swaps row → input; Enter publishes edit RPC and exits edit mode; Esc cancels; Delete opens confirm dialog; confirm publishes delete RPC and renders "Message deleted" placeholder optimistically |

Existing `MessageArea.test.jsx` and `MessageInput.test.jsx` are split
across the new presentational + container test files. No backend Go
tests — no backend changes.

## Out of scope / future work

- "Threads" tab listing all threads (would consume
  `chat.user.{account}.request.msg.thread.parent`).
- Reply pagination beyond the initial 50.
- Mark-as-read for threads + unread badges.
- Thread notifications (per-mention / per-subscription).
- Real-time delivery of **non-`tshow`** thread replies from other users
  into an open thread panel — own ticket, owns the broadcast subject /
  consumer.
- Edits and deletes of other users' thread replies propagating into an
  open thread panel — paired with the events ticket.
- "Last reply at" timestamp on the reply-count badge — the field isn't
  on the Message payload today; would require a per-room ThreadRoom
  hydration RPC. Not in v1.
- Jump-to-original for quoted messages that are out of the loaded
  buffer or in another room — needs a per-message lookup fallback;
  out of v1 (no-op for now).

## Open questions

None — all decisions captured above. Implementer to verify in Red phase
that the existing chat-frontend edit and delete RPC subject builders /
payload shapes are reused rather than redefined.
