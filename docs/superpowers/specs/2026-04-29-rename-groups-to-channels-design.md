# Rename groups to channels in chat-frontend

## Goal

Eliminate the obsolete `'group'` term from chat-frontend by renaming all
literals, identifiers, fixtures, and test descriptions to `'channel'`, matching
the backend's authoritative `RoomTypeChannel = "channel"` (defined in
`pkg/model/room.go`).

A side effect — actually the user-visible motivation — is that the **Members**
and **Leave** buttons in the `ChatPage` header become live again. They are
currently dead because they gate on `room.type === 'group'`, but the backend
only ever sends `type: 'channel'`.

## Background

`pkg/model/room.go` defines exactly two room types:

```go
RoomTypeChannel RoomType = "channel"
RoomTypeDM      RoomType = "dm"
```

There is no `"group"` room type on the wire. Most of chat-frontend already uses
`'channel'` correctly (`CreateRoomDialog`, `RoomEventsContext`), but a handful
of stale references to `'group'` remain in two production files plus several
test fixtures and a smoke-test mock. "Group" is an obsolete concept.

## Scope

### Production code (2 files)

- `chat-frontend/src/components/LeaveRoomButton.jsx:7`
  `room.type !== 'group'` → `room.type !== 'channel'`
- `chat-frontend/src/pages/ChatPage.jsx`
  - line 33: `const isGroup = selectedRoom?.type === 'group'` →
    `const isChannel = selectedRoom?.type === 'channel'`
  - line 39: `{isGroup && (` → `{isChannel && (`

### Test code (4 files)

- `chat-frontend/src/components/LeaveRoomButton.test.jsx`
  - rename local `groupRoom` → `channelRoom` (declaration on line 11 plus three
    use sites on lines 42, 53, 62)
  - fixture `type: 'group'` → `type: 'channel'` (line 11)
- `chat-frontend/src/components/ManageMembersDialog.test.jsx:11`
  fixture `type: 'group'` → `type: 'channel'`
- `chat-frontend/src/pages/ChatPage.test.jsx`
  - fixtures `type: 'group'` → `type: 'channel'` (lines 15, 41)
  - test ID `pick-group` → `pick-channel` (lines 15–16, 65, 72, 80)
  - test name `'shows Members and Leave on a group room'` →
    `'shows Members and Leave on a channel room'` (line 63)
- `chat-frontend/src/context/RoomEventsContext.test.jsx:134`
  fixture display `name: 'group'` → `name: 'general-channel'` (cosmetic only;
  the `type` field on this fixture was already correct as `'channel'`)

### Smoke test (1 file)

- `chat-frontend/smoke-test.mjs:135`
  mock NATS reply payload `type: 'group'` → `type: 'channel'`

### Out of scope

- Backend changes — `pkg/model/room.go` and all Go services already use
  `RoomTypeChannel`.
- CSS class renames — none of the styles use the term "group".
- User-facing copy changes — there is no visible "Group" string in the UI.
- Introducing a `ROOM_TYPE_CHANNEL` constant — YAGNI; `'channel'` is a stable
  wire value owned by the backend, and the literal already appears in several
  files without issue.

## Implementation Approach

Two-stage edit, two commits:

1. **Stage 1 — bugfix.** Update the two production literals
   (`LeaveRoomButton.jsx`, `ChatPage.jsx`) plus the test fixtures whose
   `type` value is *load-bearing* for those tests:
   - `LeaveRoomButton.test.jsx` fixture (the component now returns null
     unless `type === 'channel'`)
   - `ChatPage.test.jsx` fixture lines 15 and 41 (the test on line 63
     asserts the Members/Leave buttons render)

   After this stage, the previously dead header buttons exercise live code
   paths and tests still pass. `isGroup` is renamed to `isChannel` here as
   well, since it is touched in the same edit.
2. **Stage 2 — cosmetic rename.** All remaining stale `'group'` references
   that have no behavioral effect:
   - `LeaveRoomButton.test.jsx` identifier `groupRoom` → `channelRoom`
   - `ChatPage.test.jsx` test ID `pick-group` → `pick-channel` and test
     name `'... on a group room'` → `'... on a channel room'`
   - `ManageMembersDialog.test.jsx` fixture `type: 'group'` → `type: 'channel'`
     (component does not gate on `type`, so this is purely cleanup)
   - `RoomEventsContext.test.jsx` fixture display `name: 'group'` →
     `name: 'general-channel'`
   - `smoke-test.mjs` mock payload `type: 'group'` → `type: 'channel'`

Splitting the work this way keeps the behavioral fix bisectable and isolated
from the identifier-rename noise.

## Verification

Per the operating environment's constraints:

- `npm run lint` clean
- `npm test` (vitest) all pass after each commit

No manual browser / dev-server verification is performed in this environment.
The behavioral change (Members/Leave buttons rendering for channel rooms) is
covered by the existing tests in `ChatPage.test.jsx` and `LeaveRoomButton.test.jsx`,
which now exercise the live branches once their fixtures use `type: 'channel'`.

## Risk and Rollback

Risk is minimal:

- Only two production lines change behavior. Both un-break currently-dead UI
  paths; neither can break a path that currently works.
- All other edits are renames within test files or string literals in mock
  payloads.

If the Members/Leave buttons cause unexpected behavior in some downstream
environment, revert is a single commit (the Stage 1 commit).
