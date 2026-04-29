# Rename groups to channels in chat-frontend — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the obsolete `'group'` room-type term from chat-frontend by renaming it to `'channel'` everywhere it still appears, restoring the dead **Members** and **Leave** header buttons on `ChatPage`.

**Architecture:** Two-stage edit. Stage 1 fixes the two production literals plus only the test fixtures whose `type` value gates assertions (so tests stay green commit-by-commit). Stage 2 cleans up remaining stale `'group'` references that have no behavioral effect (test identifiers, test names, an unrelated fixture, the smoke-test mock).

**Tech Stack:** React 19 + Vite 6, Vitest 2 for tests. No lint script is configured for `chat-frontend` (verified in `package.json`); verification is `npm test` only. Working directory for all commands is `chat-frontend/` unless stated otherwise.

**Spec:** `docs/superpowers/specs/2026-04-29-rename-groups-to-channels-design.md`

---

## Background — what's in the codebase right now

Read these once before starting; later tasks will reference them.

- `pkg/model/room.go` defines `RoomTypeChannel RoomType = "channel"` and `RoomTypeDM RoomType = "dm"`. There is no `"group"` room type on the wire.
- chat-frontend already uses `'channel'` correctly in `CreateRoomDialog.jsx`, `RoomEventsContext.jsx`, and most fixtures.
- Six files still contain stale `'group'`. They are listed exhaustively below.

The only **behavioral** change in this plan is to two production lines. Everything else is identifier and string-literal cleanup.

---

## Task 1: Stage 1 — Fix `LeaveRoomButton` (production + load-bearing fixture)

**Why first:** The component currently returns null whenever `room.type !== 'group'`. Since the backend never sends `'group'`, the button is dead. Flipping the fixture to `'channel'` first makes the existing tests fail in a meaningful way (they assert the button renders), then flipping the production literal makes them pass.

**Files:**
- Modify: `chat-frontend/src/components/LeaveRoomButton.test.jsx:11`
- Modify: `chat-frontend/src/components/LeaveRoomButton.jsx:7`

- [ ] **Step 1: Flip the fixture's room type from `'group'` to `'channel'`**

In `chat-frontend/src/components/LeaveRoomButton.test.jsx`, change line 11:

```javascript
const groupRoom = { id: 'r1', siteId: 'site-A', name: 'general', type: 'channel' }
```

(Identifier is still `groupRoom` for now — that gets renamed in Task 4.)

- [ ] **Step 2: Run the test file and confirm three tests fail**

```bash
cd chat-frontend && npx vitest run src/components/LeaveRoomButton.test.jsx
```

Expected: the three tests that pass `groupRoom` into `setup(...)` fail because production still does `room.type !== 'group'`, which is now true for the `'channel'` fixture, so the component returns null and `getByRole('button', { name: /Leave/i })` throws "Unable to find an accessible element with the role 'button' and name `/Leave/i`". The two tests that do not use `groupRoom` (`renders nothing when room type is dm`, `renders nothing when room is null`) still pass.

- [ ] **Step 3: Flip the production literal from `'group'` to `'channel'`**

In `chat-frontend/src/components/LeaveRoomButton.jsx`, change line 7:

```javascript
  if (!room || room.type !== 'channel') return null
```

- [ ] **Step 4: Run the test file and confirm all five tests pass**

```bash
cd chat-frontend && npx vitest run src/components/LeaveRoomButton.test.jsx
```

Expected: 5 passed.

- [ ] **Step 5: Commit (held until Task 3 — do NOT commit yet)**

Skip this step. Stage 1 commits as one unit at the end of Task 3 so the bugfix commit is atomic.

---

## Task 2: Stage 1 — Fix `ChatPage` (production + load-bearing fixtures)

**Why second:** Same Red-Green pattern as Task 1, but also renames `isGroup` → `isChannel` since it's touched in the same edit.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.test.jsx:15,41`
- Modify: `chat-frontend/src/pages/ChatPage.jsx:33,39`

- [ ] **Step 1: Flip the two fixture `type` values from `'group'` to `'channel'`**

In `chat-frontend/src/pages/ChatPage.test.jsx`:

Change line 15 (inside the `RoomList` mock):

```javascript
      <button onClick={() => onSelectRoom({ id: 'r1', name: 'general', type: 'channel', siteId: 'site-A' })}>
```

Change line 41 (inside `useRoomSummaries.mockReturnValue`):

```javascript
      { id: 'r1', name: 'general', type: 'channel', siteId: 'site-A', userCount: 2, lastMsgAt: null, unreadCount: 0, hasMention: false, mentionAll: false },
```

Leave `pick-group` (line 16), the test name on line 63, and any other identifiers alone for now — those move in Task 5.

- [ ] **Step 2: Run the test file and confirm three tests fail**

```bash
cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx
```

Expected: the three tests that click `pick-group` (`shows Members and Leave on a group room`, `opens ManageMembersDialog when Members is clicked`, `closes ManageMembersDialog when Close is clicked`) fail. Each fails because `isGroup` is now false (fixture is `'channel'`, production still checks `=== 'group'`), so the Members/Leave buttons don't render and `getByRole('button', { name: /^Members$/ })` throws. The two tests that don't depend on a selected channel still pass.

- [ ] **Step 3: Update production: flip the literal and rename `isGroup` → `isChannel`**

In `chat-frontend/src/pages/ChatPage.jsx`, change line 33:

```javascript
  const isChannel = selectedRoom?.type === 'channel'
```

And change line 39:

```javascript
        {isChannel && (
```

- [ ] **Step 4: Run the test file and confirm all five tests pass**

```bash
cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx
```

Expected: 5 passed.

- [ ] **Step 5: Commit (held until Task 3 — do NOT commit yet)**

Skip. Stage 1 commits as one unit at the end of Task 3.

---

## Task 3: Stage 1 — Run full test suite and commit the bugfix

**Files:** none modified.

- [ ] **Step 1: Run the full chat-frontend test suite**

```bash
cd chat-frontend && npm test
```

Expected: all tests pass. (No new failures elsewhere — `RoomEventsContext.test.jsx` already uses `type: 'channel'`, `ManageMembersDialog.test.jsx` doesn't gate on `type`, and other tests don't reference `'group'`.)

- [ ] **Step 2: Stage the four files modified in Tasks 1–2**

```bash
cd /home/user/chat
git add \
  chat-frontend/src/components/LeaveRoomButton.jsx \
  chat-frontend/src/components/LeaveRoomButton.test.jsx \
  chat-frontend/src/pages/ChatPage.jsx \
  chat-frontend/src/pages/ChatPage.test.jsx
```

- [ ] **Step 3: Commit with a `fix:` message**

```bash
git commit -m "$(cat <<'EOF'
fix(chat-frontend): use channel room type to match backend

LeaveRoomButton and ChatPage gated on room.type === 'group', but the
backend only sends RoomTypeChannel = "channel". The Members and Leave
header buttons were therefore never rendered. Flip both checks to
'channel' and rename ChatPage's local isGroup to isChannel; update
the load-bearing test fixtures so the existing assertions exercise the
live branches.
EOF
)"
```

Expected: commit succeeds; pre-commit hooks (if any apply to JS) pass.

- [ ] **Step 4: Verify working tree is clean except for any leftover Stage-2 work**

```bash
cd /home/user/chat && git status
```

Expected: working tree clean (Stage 2 hasn't started).

---

## Task 4: Stage 2 — Rename `groupRoom` → `channelRoom` in `LeaveRoomButton.test.jsx`

**Why:** Pure identifier cleanup. The fixture is already `type: 'channel'`; the variable name should match.

**Files:**
- Modify: `chat-frontend/src/components/LeaveRoomButton.test.jsx` (declaration on line 11; uses on lines 42, 53, 62)

- [ ] **Step 1: Rename the local in all four locations**

Replace every occurrence of `groupRoom` with `channelRoom` in this file. After the edit, line 11 reads:

```javascript
const channelRoom = { id: 'r1', siteId: 'site-A', name: 'general', type: 'channel' }
```

And the three call sites become `setup(channelRoom)` (lines 42 and 53) and `setup(channelRoom, { request })` (line 62).

- [ ] **Step 2: Run the test file**

```bash
cd chat-frontend && npx vitest run src/components/LeaveRoomButton.test.jsx
```

Expected: 5 passed.

---

## Task 5: Stage 2 — Rename `pick-group` and the test description in `ChatPage.test.jsx`

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.test.jsx` (lines 15–16, 63, 65, 72, 80)

- [ ] **Step 1: Update the button label inside the `RoomList` mock**

Lines 15–16 currently read:

```javascript
      <button onClick={() => onSelectRoom({ id: 'r1', name: 'general', type: 'channel', siteId: 'site-A' })}>
        pick-group
      </button>
```

Change the inner text to:

```javascript
      <button onClick={() => onSelectRoom({ id: 'r1', name: 'general', type: 'channel', siteId: 'site-A' })}>
        pick-channel
      </button>
```

- [ ] **Step 2: Update the three `fireEvent.click(screen.getByText('pick-group'))` calls**

Lines 65, 72, 80 — change the argument from `'pick-group'` to `'pick-channel'`. Each becomes:

```javascript
    fireEvent.click(screen.getByText('pick-channel'))
```

- [ ] **Step 3: Update the test description on line 63**

```javascript
  it('shows Members and Leave on a channel room', () => {
```

- [ ] **Step 4: Run the test file**

```bash
cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx
```

Expected: 5 passed.

---

## Task 6: Stage 2 — Update the `ManageMembersDialog.test.jsx` fixture

**Why:** Stale `type: 'group'` fixture that doesn't gate any behavior in the component under test, but should match the backend for consistency.

**Files:**
- Modify: `chat-frontend/src/components/ManageMembersDialog.test.jsx:11`

- [ ] **Step 1: Flip the fixture `type` to `'channel'`**

Change line 11 to:

```javascript
const room = { id: 'r1', siteId: 'site-A', name: 'general', type: 'channel' }
```

- [ ] **Step 2: Run the test file**

```bash
cd chat-frontend && npx vitest run src/components/ManageMembersDialog.test.jsx
```

Expected: all tests pass (this fixture's `type` is informational; the dialog does not gate on it).

---

## Task 7: Stage 2 — Update `RoomEventsContext.test.jsx` fixture display name

**Why:** The fixture's `type` is already `'channel'`; the `name` field is the literal string `'group'` which is just confusing now.

**Files:**
- Modify: `chat-frontend/src/context/RoomEventsContext.test.jsx:134`

- [ ] **Step 1: Rename the fixture's display name**

Change line 134 from:

```javascript
      { id: 'g1', name: 'group', type: 'channel', siteId: 'site-A', userCount: 3, lastMsgAt: '2026-04-17T10:00:00Z' },
```

to:

```javascript
      { id: 'g1', name: 'general-channel', type: 'channel', siteId: 'site-A', userCount: 3, lastMsgAt: '2026-04-17T10:00:00Z' },
```

- [ ] **Step 2: Run the test file**

```bash
cd chat-frontend && npx vitest run src/context/RoomEventsContext.test.jsx
```

Expected: all tests pass. (No assertion in this file matches on the literal `'group'` string for this fixture — verified by reading the test file; the name field is only referenced via `r.name` for display, not asserted on.)

If this step unexpectedly fails because some assertion does check the string `'group'`, revert this single line change and skip this task — it is purely cosmetic and the fixture can remain as-is.

---

## Task 8: Stage 2 — Update `smoke-test.mjs` mock payload

**Why:** The smoke test stands up a fake NATS reply to exercise the request/reply path; its mock payload should mirror real backend payloads.

**Files:**
- Modify: `chat-frontend/smoke-test.mjs:135`

- [ ] **Step 1: Flip the mock `type` to `'channel'`**

Change line 135 from:

```javascript
      const reply = { rooms: [{ id: roomId, name: 'test-room', type: 'group', userCount: 2 }] }
```

to:

```javascript
      const reply = { rooms: [{ id: roomId, name: 'test-room', type: 'channel', userCount: 2 }] }
```

- [ ] **Step 2: No automated run — smoke-test.mjs requires a live NATS stack**

This file is not part of `npm test`. It is verified by inspection: a one-character change to a mock payload that mirrors `pkg/model/room.go`'s `RoomTypeChannel`. No further action.

---

## Task 9: Stage 2 — Final test run, sweep, and commit

**Files:** none modified.

- [ ] **Step 1: Run the full chat-frontend test suite**

```bash
cd chat-frontend && npm test
```

Expected: all tests pass.

- [ ] **Step 2: Sweep the chat-frontend tree for any remaining `'group'` references**

```bash
cd chat-frontend && grep -rin "group" . \
  --include='*.jsx' --include='*.js' --include='*.html' \
  --include='*.css' --include='*.json' --include='*.mjs' \
  | grep -v node_modules | grep -v package-lock
```

Expected: no output. If anything is reported, decide case-by-case whether it's in scope (a stale room-type reference) or unrelated (e.g., the word "group" appearing in some other context). The spec lists every in-scope reference exhaustively; anything not on that list can be left alone, but flag it in the commit message if you do change it.

- [ ] **Step 3: Stage the five files modified in Tasks 4–8**

```bash
cd /home/user/chat
git add \
  chat-frontend/src/components/LeaveRoomButton.test.jsx \
  chat-frontend/src/pages/ChatPage.test.jsx \
  chat-frontend/src/components/ManageMembersDialog.test.jsx \
  chat-frontend/src/context/RoomEventsContext.test.jsx \
  chat-frontend/smoke-test.mjs
```

(If Task 7 was skipped due to an unexpected assertion failure, omit `RoomEventsContext.test.jsx` from the `git add`.)

- [ ] **Step 4: Commit with a `refactor:` message**

```bash
git commit -m "$(cat <<'EOF'
refactor(chat-frontend): rename remaining group references to channel

Cosmetic cleanup of identifiers and fixtures that still used the
obsolete 'group' term: groupRoom -> channelRoom in LeaveRoomButton
tests, pick-group -> pick-channel in ChatPage tests, and stale
'group' fixture values in ManageMembersDialog and RoomEventsContext
tests, plus the smoke-test mock payload. No behavior change.
EOF
)"
```

Expected: commit succeeds.

- [ ] **Step 5: Push both commits**

```bash
cd /home/user/chat && git push -u origin claude/rename-groups-to-channels-NCaag
```

Expected: push succeeds; remote branch is updated with the two new commits (plus the spec commit pushed earlier).

- [ ] **Step 6: Final verification**

```bash
cd /home/user/chat && git log --oneline origin/main..HEAD
```

Expected: three commits on the branch — the spec commit, the Stage 1 `fix:` commit, and the Stage 2 `refactor:` commit.

```bash
cd chat-frontend && npm test
```

Expected: all tests pass on the final tree.
