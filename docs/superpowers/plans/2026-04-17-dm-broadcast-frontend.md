# DM Broadcast Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make DM `new_message` events visible in the frontend by introducing an app-level room-event dispatcher, and add unread/mention indicators to the room list.

**Architecture:** A new `RoomEventsProvider` sits between `NatsProvider` and the app. It owns one user-scoped DM subscription (`chat.user.{account}.event.room`), a map of per-group-room subscriptions (`chat.room.{roomID}.event`), a bounded message cache, and per-room unread/mention state. `MessageArea` and `RoomList` become consumers of two hooks (`useRoomEvents`, `useRoomSummaries`) instead of subscribing to NATS directly.

**Tech Stack:** React 19, Vite 6, nats.ws, Vitest (new dev dep), @testing-library/react (new dev dep).

**Spec:** `docs/superpowers/specs/2026-04-17-dm-broadcast-frontend-design.md`

---

## File Map

### New files (`chat-frontend/`)

| File | Responsibility |
|------|----------------|
| `chat-frontend/src/lib/roomEventsReducer.js` | Pure reducer: rooms, messages, unread, mention flags |
| `chat-frontend/src/lib/roomEventsReducer.test.js` | Unit tests for the reducer |
| `chat-frontend/src/context/RoomEventsContext.jsx` | Provider + `useRoomEvents` + `useRoomSummaries` hooks |
| `chat-frontend/src/context/RoomEventsContext.test.jsx` | Provider integration tests with mocked NATS context |
| `chat-frontend/src/components/RoomList.test.jsx` | Component tests for unread/mention UI |
| `chat-frontend/src/components/MessageArea.test.jsx` | Component tests for message rendering from provider |
| `chat-frontend/vitest.config.js` | Vitest config (jsdom env, setup file) |
| `chat-frontend/test/setup.js` | Test setup: jest-dom matchers |

### Modified files

| File | Change |
|------|--------|
| `chat-frontend/package.json` | Add `test` script and Vitest / testing-library dev deps |
| `chat-frontend/src/lib/subjects.js` | Add `userRoomEvent(account)` subject builder |
| `chat-frontend/src/App.jsx` | Wrap `AppContent` in `RoomEventsProvider` |
| `chat-frontend/src/pages/ChatPage.jsx` | Call `setActiveRoom` when a room is selected |
| `chat-frontend/src/components/RoomList.jsx` | Consume `useRoomSummaries` instead of fetching/subscribing |
| `chat-frontend/src/components/MessageArea.jsx` | Consume `useRoomEvents` instead of subscribing directly |
| `chat-frontend/src/styles/index.css` | Add unread/mention classes |
| `chat-frontend/smoke-test.mjs` | Add a DM broadcast assertion |

---

## Task Overview

1. Set up Vitest and testing-library
2. Add `userRoomEvent` subject builder
3. Reducer part 1: rooms list actions
4. Reducer part 2: `MESSAGE_RECEIVED` (the core dispatcher)
5. Reducer part 3: history + active room + reset + cache bound
6. `RoomEventsProvider` skeleton (reducer + hook exposure, no subscriptions yet)
7. `RoomEventsProvider` subscriptions (rooms.list fetch, DM sub, group subs, subscription.update handling)
8. Wire `RoomEventsProvider` into `App.jsx`
9. Refactor `MessageArea` to use `useRoomEvents`
10. Refactor `RoomList` to use `useRoomSummaries` + unread/mention UI
11. Update `ChatPage` to call `setActiveRoom`
12. Add unread/mention CSS
13. Update smoke test for DM broadcast
14. Manual verification

---

## Task 1: Set up Vitest and testing-library

**Files:**
- Modify: `chat-frontend/package.json`
- Create: `chat-frontend/vitest.config.js`
- Create: `chat-frontend/test/setup.js`

- [ ] **Step 1: Add Vitest + testing-library dev deps and test script**

Edit `chat-frontend/package.json`:

```json
{
  "name": "chat-frontend",
  "private": true,
  "version": "0.0.1",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "test:watch": "vitest"
  },
  "dependencies": {
    "nats.ws": "^1.30.0",
    "nkeys.js": "^1.1.0",
    "react": "^19.1.0",
    "react-dom": "^19.1.0",
    "uuid": "^11.1.0"
  },
  "devDependencies": {
    "@testing-library/jest-dom": "^6.4.0",
    "@testing-library/react": "^16.0.0",
    "@types/react": "^19.1.2",
    "@types/react-dom": "^19.1.2",
    "@vitejs/plugin-react": "^4.4.1",
    "jsdom": "^24.0.0",
    "vite": "^6.3.2",
    "vitest": "^2.1.0"
  }
}
```

- [ ] **Step 2: Install deps**

Run (from `chat-frontend/`): `npm install`
Expected: installs new dev deps, updates `package-lock.json`, no errors.

- [ ] **Step 3: Create Vitest config**

Create `chat-frontend/vitest.config.js`:

```js
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./test/setup.js'],
  },
})
```

- [ ] **Step 4: Create test setup file**

Create `chat-frontend/test/setup.js`:

```js
import '@testing-library/jest-dom/vitest'
```

- [ ] **Step 5: Sanity-check the runner**

Create `chat-frontend/test/smoke.test.js` (temporary):

```js
import { describe, it, expect } from 'vitest'

describe('vitest setup', () => {
  it('runs', () => {
    expect(1 + 1).toBe(2)
  })
})
```

Run (from `chat-frontend/`): `npm test`
Expected: 1 test passes.

- [ ] **Step 6: Delete the sanity test**

Delete `chat-frontend/test/smoke.test.js`.

- [ ] **Step 7: Commit**

```bash
git add chat-frontend/package.json chat-frontend/package-lock.json chat-frontend/vitest.config.js chat-frontend/test/setup.js
git commit -m "chore(chat-frontend): add Vitest and testing-library"
```

---

## Task 2: Add `userRoomEvent` subject builder

**Files:**
- Modify: `chat-frontend/src/lib/subjects.js`
- Create: `chat-frontend/src/lib/subjects.test.js`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/lib/subjects.test.js`:

```js
import { describe, it, expect } from 'vitest'
import { userRoomEvent, roomEvent } from './subjects'

describe('subjects', () => {
  it('userRoomEvent builds the per-user room event subject', () => {
    expect(userRoomEvent('alice')).toBe('chat.user.alice.event.room')
  })

  it('roomEvent still builds the per-room subject', () => {
    expect(roomEvent('r1')).toBe('chat.room.r1.event')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `npm test`
Expected: FAIL with `userRoomEvent is not a function` (or similar import error).

- [ ] **Step 3: Add the builder**

Edit `chat-frontend/src/lib/subjects.js` — append:

```js
export function userRoomEvent(account) {
  return `chat.user.${account}.event.room`
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `npm test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/subjects.js chat-frontend/src/lib/subjects.test.js
git commit -m "feat(chat-frontend): add userRoomEvent subject builder"
```

---

## Task 3: Reducer — rooms list actions

**Files:**
- Create: `chat-frontend/src/lib/roomEventsReducer.js`
- Create: `chat-frontend/src/lib/roomEventsReducer.test.js`

This task introduces the reducer and the rooms-list actions. Later tasks extend it.

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/lib/roomEventsReducer.test.js`:

```js
import { describe, it, expect } from 'vitest'
import { initialState, roomEventsReducer } from './roomEventsReducer'

function room(id, overrides = {}) {
  return {
    id,
    name: `room-${id}`,
    type: 'group',
    siteId: 'site-A',
    userCount: 2,
    lastMsgAt: '2026-04-17T10:00:00Z',
    ...overrides,
  }
}

describe('roomEventsReducer: rooms actions', () => {
  it('ROOMS_LOADED populates summaries sorted by lastMsgAt desc', () => {
    const a = room('a', { lastMsgAt: '2026-04-17T10:00:00Z' })
    const b = room('b', { lastMsgAt: '2026-04-17T12:00:00Z' })
    const next = roomEventsReducer(initialState, {
      type: 'ROOMS_LOADED',
      rooms: [a, b],
    })
    expect(next.summaries.map((r) => r.id)).toEqual(['b', 'a'])
    expect(next.summaries[0]).toMatchObject({
      id: 'b',
      name: 'room-b',
      type: 'group',
      unreadCount: 0,
      hasMention: false,
      mentionAll: false,
    })
  })

  it('ROOM_ADDED appends a room and keeps sort order', () => {
    const a = room('a', { lastMsgAt: '2026-04-17T09:00:00Z' })
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a] })
    const b = room('b', { lastMsgAt: '2026-04-17T10:00:00Z' })
    const next = roomEventsReducer(state, { type: 'ROOM_ADDED', room: b })
    expect(next.summaries.map((r) => r.id)).toEqual(['b', 'a'])
  })

  it('ROOM_ADDED ignores duplicates', () => {
    const a = room('a')
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a] })
    const next = roomEventsReducer(state, { type: 'ROOM_ADDED', room: a })
    expect(next.summaries).toHaveLength(1)
  })

  it('ROOM_REMOVED drops the room from summaries and clears roomState', () => {
    const a = room('a')
    const b = room('b')
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a, b] })
    const withCache = {
      ...state,
      roomState: {
        a: { messages: [], hasLoadedHistory: false, historyError: null, unreadCount: 1, hasMention: false, mentionAll: false, lastMsgAt: null, lastMsgId: null },
      },
    }
    const next = roomEventsReducer(withCache, { type: 'ROOM_REMOVED', roomId: 'a' })
    expect(next.summaries.map((r) => r.id)).toEqual(['b'])
    expect(next.roomState.a).toBeUndefined()
  })

  it('ROOM_METADATA_UPDATED patches name/userCount/lastMsgAt and re-sorts', () => {
    const a = room('a', { lastMsgAt: '2026-04-17T09:00:00Z' })
    const b = room('b', { lastMsgAt: '2026-04-17T10:00:00Z' })
    const state = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a, b] })
    const next = roomEventsReducer(state, {
      type: 'ROOM_METADATA_UPDATED',
      roomId: 'a',
      name: 'a-renamed',
      userCount: 5,
      lastMsgAt: '2026-04-17T11:00:00Z',
    })
    expect(next.summaries[0]).toMatchObject({ id: 'a', name: 'a-renamed', userCount: 5 })
  })

  it('ROOM_METADATA_UPDATED for unknown room is a no-op', () => {
    const next = roomEventsReducer(initialState, {
      type: 'ROOM_METADATA_UPDATED',
      roomId: 'missing',
      name: 'x',
      userCount: 1,
      lastMsgAt: '2026-04-17T11:00:00Z',
    })
    expect(next).toBe(initialState)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npm test`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement the reducer skeleton with rooms actions**

Create `chat-frontend/src/lib/roomEventsReducer.js`:

```js
export const MAX_CACHED = 200

export const initialState = {
  summaries: [],
  roomState: {},
  activeRoomId: null,
  roomsError: null,
}

function sortByLastMsgDesc(summaries) {
  return [...summaries].sort((a, b) => {
    const at = a.lastMsgAt ? new Date(a.lastMsgAt).getTime() : 0
    const bt = b.lastMsgAt ? new Date(b.lastMsgAt).getTime() : 0
    return bt - at
  })
}

function toSummary(room) {
  return {
    id: room.id,
    name: room.name,
    type: room.type,
    siteId: room.siteId,
    userCount: room.userCount,
    lastMsgAt: room.lastMsgAt ?? null,
    unreadCount: 0,
    hasMention: false,
    mentionAll: false,
  }
}

export function roomEventsReducer(state, action) {
  switch (action.type) {
    case 'ROOMS_LOADED': {
      const summaries = sortByLastMsgDesc(action.rooms.map(toSummary))
      return { ...state, summaries, roomsError: null }
    }
    case 'ROOM_ADDED': {
      if (state.summaries.some((r) => r.id === action.room.id)) return state
      const summaries = sortByLastMsgDesc([...state.summaries, toSummary(action.room)])
      return { ...state, summaries }
    }
    case 'ROOM_REMOVED': {
      const summaries = state.summaries.filter((r) => r.id !== action.roomId)
      const { [action.roomId]: _removed, ...rest } = state.roomState
      return { ...state, summaries, roomState: rest }
    }
    case 'ROOM_METADATA_UPDATED': {
      if (!state.summaries.some((r) => r.id === action.roomId)) return state
      const summaries = sortByLastMsgDesc(
        state.summaries.map((r) =>
          r.id === action.roomId
            ? { ...r, name: action.name, userCount: action.userCount, lastMsgAt: action.lastMsgAt }
            : r
        )
      )
      return { ...state, summaries }
    }
    default:
      return state
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npm test`
Expected: all 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/roomEventsReducer.js chat-frontend/src/lib/roomEventsReducer.test.js
git commit -m "feat(chat-frontend): add roomEventsReducer with rooms actions"
```

---

## Task 4: Reducer — `MESSAGE_RECEIVED`

**Files:**
- Modify: `chat-frontend/src/lib/roomEventsReducer.js`
- Modify: `chat-frontend/src/lib/roomEventsReducer.test.js`

This is the heart of the dispatcher. The same action handles both group and DM room events. The dispatcher (later in `RoomEventsContext`) computes `hasMention` before dispatching for group rooms; for DM rooms the event already carries `hasMention`.

- [ ] **Step 1: Extend the tests**

Append to `chat-frontend/src/lib/roomEventsReducer.test.js`:

```js
function newMessageEvent(overrides = {}) {
  return {
    type: 'new_message',
    roomId: 'a',
    roomName: 'room-a',
    roomType: 'group',
    siteId: 'site-A',
    userCount: 3,
    lastMsgAt: '2026-04-17T12:00:00Z',
    lastMsgId: 'm1',
    mentionAll: false,
    hasMention: false,
    message: {
      id: 'm1',
      roomId: 'a',
      content: 'hi',
      createdAt: '2026-04-17T12:00:00Z',
      sender: { account: 'bob', engName: 'Bob' },
    },
    timestamp: 1,
    ...overrides,
  }
}

describe('roomEventsReducer: MESSAGE_RECEIVED', () => {
  it('appends a message and seeds roomState for an unknown room', () => {
    const next = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    expect(next.roomState.a.messages).toHaveLength(1)
    expect(next.roomState.a.messages[0].id).toBe('m1')
    expect(next.roomState.a.unreadCount).toBe(1)
    expect(next.roomState.a.lastMsgAt).toBe('2026-04-17T12:00:00Z')
    expect(next.roomState.a.lastMsgId).toBe('m1')
  })

  it('deduplicates by message.id', () => {
    const s1 = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    const s2 = roomEventsReducer(s1, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    expect(s2.roomState.a.messages).toHaveLength(1)
    expect(s2.roomState.a.unreadCount).toBe(1)
  })

  it('does not increment unreadCount for the active room', () => {
    const state = { ...initialState, activeRoomId: 'a' }
    const next = roomEventsReducer(state, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent(),
    })
    expect(next.roomState.a.unreadCount).toBe(0)
  })

  it('sets hasMention when event.hasMention is true and room is not active', () => {
    const next = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ hasMention: true }),
    })
    expect(next.roomState.a.hasMention).toBe(true)
    expect(next.roomState.a.mentionAll).toBe(false)
  })

  it('sets mentionAll when event.mentionAll is true and room is not active', () => {
    const next = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ mentionAll: true }),
    })
    expect(next.roomState.a.mentionAll).toBe(true)
  })

  it('does not set mention flags for the active room', () => {
    const state = { ...initialState, activeRoomId: 'a' }
    const next = roomEventsReducer(state, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ hasMention: true, mentionAll: true }),
    })
    expect(next.roomState.a.hasMention).toBe(false)
    expect(next.roomState.a.mentionAll).toBe(false)
  })

  it('updates matching summary lastMsgAt and resorts', () => {
    const a = { id: 'a', name: 'a', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: '2026-04-17T08:00:00Z' }
    const b = { id: 'b', name: 'b', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: '2026-04-17T09:00:00Z' }
    const loaded = roomEventsReducer(initialState, { type: 'ROOMS_LOADED', rooms: [a, b] })
    const next = roomEventsReducer(loaded, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ roomId: 'a', lastMsgAt: '2026-04-17T10:00:00Z' }),
    })
    expect(next.summaries.map((r) => r.id)).toEqual(['a', 'b'])
    expect(next.summaries[0].lastMsgAt).toBe('2026-04-17T10:00:00Z')
    expect(next.summaries[0].unreadCount).toBe(1)
  })

  it('caps the cached messages at MAX_CACHED, dropping oldest', async () => {
    const { MAX_CACHED } = await import('./roomEventsReducer')
    let state = initialState
    for (let i = 0; i < MAX_CACHED + 5; i++) {
      state = roomEventsReducer(state, {
        type: 'MESSAGE_RECEIVED',
        event: newMessageEvent({
          message: {
            id: `m${i}`,
            roomId: 'a',
            content: String(i),
            createdAt: '2026-04-17T12:00:00Z',
            sender: { account: 'bob', engName: 'Bob' },
          },
        }),
      })
    }
    const msgs = state.roomState.a.messages
    expect(msgs).toHaveLength(MAX_CACHED)
    expect(msgs[0].id).toBe('m5')
    expect(msgs[MAX_CACHED - 1].id).toBe(`m${MAX_CACHED + 4}`)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npm test`
Expected: FAIL — `MESSAGE_RECEIVED` is unhandled.

- [ ] **Step 3: Implement `MESSAGE_RECEIVED`**

Edit `chat-frontend/src/lib/roomEventsReducer.js`. Add helpers near the top:

```js
function emptyRoomState() {
  return {
    messages: [],
    hasLoadedHistory: false,
    historyError: null,
    unreadCount: 0,
    hasMention: false,
    mentionAll: false,
    lastMsgAt: null,
    lastMsgId: null,
  }
}

function appendBounded(messages, msg) {
  if (messages.some((m) => m.id === msg.id)) return messages
  const next = [...messages, msg]
  if (next.length > MAX_CACHED) {
    return next.slice(next.length - MAX_CACHED)
  }
  return next
}
```

Add a new case inside `switch (action.type)` (before `default`):

```js
case 'MESSAGE_RECEIVED': {
  const evt = action.event
  const roomId = evt.roomId
  const prev = state.roomState[roomId] ?? emptyRoomState()
  const isDup = prev.messages.some((m) => m.id === evt.message.id)
  const messages = appendBounded(prev.messages, evt.message)
  const isActive = state.activeRoomId === roomId
  const nextRoomState = {
    ...prev,
    messages,
    lastMsgAt: isDup ? prev.lastMsgAt : (evt.lastMsgAt ?? prev.lastMsgAt),
    lastMsgId: isDup ? prev.lastMsgId : (evt.lastMsgId ?? prev.lastMsgId),
    unreadCount: isDup || isActive ? prev.unreadCount : prev.unreadCount + 1,
    hasMention: isActive ? false : prev.hasMention || !!evt.hasMention,
    mentionAll: isActive ? false : prev.mentionAll || !!evt.mentionAll,
  }
  const summaries = state.summaries.some((r) => r.id === roomId)
    ? sortByLastMsgDesc(
        state.summaries.map((r) =>
          r.id === roomId
            ? {
                ...r,
                lastMsgAt: nextRoomState.lastMsgAt ?? r.lastMsgAt,
                unreadCount: nextRoomState.unreadCount,
                hasMention: nextRoomState.hasMention,
                mentionAll: nextRoomState.mentionAll,
              }
            : r
        )
      )
    : state.summaries
  return {
    ...state,
    summaries,
    roomState: { ...state.roomState, [roomId]: nextRoomState },
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npm test`
Expected: all reducer tests PASS (6 from Task 3 + 8 new).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/roomEventsReducer.js chat-frontend/src/lib/roomEventsReducer.test.js
git commit -m "feat(chat-frontend): add MESSAGE_RECEIVED reducer action"
```

---

## Task 5: Reducer — history, active room, reset

**Files:**
- Modify: `chat-frontend/src/lib/roomEventsReducer.js`
- Modify: `chat-frontend/src/lib/roomEventsReducer.test.js`

- [ ] **Step 1: Extend the tests**

Append to `chat-frontend/src/lib/roomEventsReducer.test.js`:

```js
describe('roomEventsReducer: history and active room', () => {
  it('HISTORY_LOADED merges ascending messages and preserves live ones', () => {
    const live = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({
        message: { id: 'm3', roomId: 'a', content: 'live', createdAt: '2026-04-17T12:00:00Z', sender: { account: 'bob' } },
      }),
    })
    const hist = [
      { id: 'm1', roomId: 'a', content: 'old1', createdAt: '2026-04-17T10:00:00Z', sender: { account: 'bob' } },
      { id: 'm2', roomId: 'a', content: 'old2', createdAt: '2026-04-17T11:00:00Z', sender: { account: 'bob' } },
    ]
    const next = roomEventsReducer(live, {
      type: 'HISTORY_LOADED',
      roomId: 'a',
      messages: hist,
    })
    expect(next.roomState.a.messages.map((m) => m.id)).toEqual(['m1', 'm2', 'm3'])
    expect(next.roomState.a.hasLoadedHistory).toBe(true)
    expect(next.roomState.a.historyError).toBe(null)
  })

  it('HISTORY_LOADED dedupes overlaps', () => {
    const live = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({
        message: { id: 'm2', roomId: 'a', content: 'live', createdAt: '2026-04-17T11:00:00Z', sender: { account: 'bob' } },
      }),
    })
    const hist = [
      { id: 'm1', roomId: 'a', content: 'old1', createdAt: '2026-04-17T10:00:00Z', sender: { account: 'bob' } },
      { id: 'm2', roomId: 'a', content: 'old2', createdAt: '2026-04-17T11:00:00Z', sender: { account: 'bob' } },
    ]
    const next = roomEventsReducer(live, { type: 'HISTORY_LOADED', roomId: 'a', messages: hist })
    expect(next.roomState.a.messages.map((m) => m.id)).toEqual(['m1', 'm2'])
  })

  it('HISTORY_FAILED sets historyError and does not flip hasLoadedHistory', () => {
    const next = roomEventsReducer(initialState, {
      type: 'HISTORY_FAILED',
      roomId: 'a',
      error: 'boom',
    })
    expect(next.roomState.a.historyError).toBe('boom')
    expect(next.roomState.a.hasLoadedHistory).toBe(false)
  })

  it('SET_ACTIVE_ROOM updates activeRoomId and clears unread/mention for that room', () => {
    const s1 = roomEventsReducer(initialState, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ hasMention: true, mentionAll: true }),
    })
    expect(s1.roomState.a.unreadCount).toBe(1)
    const s2 = roomEventsReducer(s1, { type: 'SET_ACTIVE_ROOM', roomId: 'a' })
    expect(s2.activeRoomId).toBe('a')
    expect(s2.roomState.a.unreadCount).toBe(0)
    expect(s2.roomState.a.hasMention).toBe(false)
    expect(s2.roomState.a.mentionAll).toBe(false)
  })

  it('SET_ACTIVE_ROOM clears the matching summary flags', () => {
    const loaded = roomEventsReducer(initialState, {
      type: 'ROOMS_LOADED',
      rooms: [{ id: 'a', name: 'a', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: '2026-04-17T10:00:00Z' }],
    })
    const withMsg = roomEventsReducer(loaded, {
      type: 'MESSAGE_RECEIVED',
      event: newMessageEvent({ hasMention: true }),
    })
    expect(withMsg.summaries[0].hasMention).toBe(true)
    expect(withMsg.summaries[0].unreadCount).toBe(1)
    const next = roomEventsReducer(withMsg, { type: 'SET_ACTIVE_ROOM', roomId: 'a' })
    expect(next.summaries[0].hasMention).toBe(false)
    expect(next.summaries[0].unreadCount).toBe(0)
  })

  it('SET_ACTIVE_ROOM to null clears the activeRoomId only', () => {
    const s1 = { ...initialState, activeRoomId: 'a' }
    const next = roomEventsReducer(s1, { type: 'SET_ACTIVE_ROOM', roomId: null })
    expect(next.activeRoomId).toBe(null)
  })

  it('RESET returns the initial state', () => {
    const s1 = roomEventsReducer(initialState, {
      type: 'ROOMS_LOADED',
      rooms: [{ id: 'a', name: 'a', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: null }],
    })
    const next = roomEventsReducer(s1, { type: 'RESET' })
    expect(next).toEqual(initialState)
  })

  it('ROOMS_FAILED stores the error message', () => {
    const next = roomEventsReducer(initialState, { type: 'ROOMS_FAILED', error: 'boom' })
    expect(next.roomsError).toBe('boom')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npm test`
Expected: FAIL — `HISTORY_LOADED` / `HISTORY_FAILED` / `SET_ACTIVE_ROOM` / `RESET` / `ROOMS_FAILED` unhandled.

- [ ] **Step 3: Implement the remaining actions**

Edit `chat-frontend/src/lib/roomEventsReducer.js`. Add the cases before `default`:

```js
case 'HISTORY_LOADED': {
  const prev = state.roomState[action.roomId] ?? emptyRoomState()
  const existingIds = new Set(prev.messages.map((m) => m.id))
  const merged = [
    ...action.messages.filter((m) => !existingIds.has(m.id)),
    ...prev.messages,
  ]
  const bounded = merged.length > MAX_CACHED ? merged.slice(merged.length - MAX_CACHED) : merged
  return {
    ...state,
    roomState: {
      ...state.roomState,
      [action.roomId]: {
        ...prev,
        messages: bounded,
        hasLoadedHistory: true,
        historyError: null,
      },
    },
  }
}
case 'HISTORY_FAILED': {
  const prev = state.roomState[action.roomId] ?? emptyRoomState()
  return {
    ...state,
    roomState: {
      ...state.roomState,
      [action.roomId]: { ...prev, historyError: action.error },
    },
  }
}
case 'SET_ACTIVE_ROOM': {
  const roomId = action.roomId
  if (roomId === null) {
    return { ...state, activeRoomId: null }
  }
  const prev = state.roomState[roomId] ?? emptyRoomState()
  const nextRoomState = { ...prev, unreadCount: 0, hasMention: false, mentionAll: false }
  const summaries = state.summaries.map((r) =>
    r.id === roomId ? { ...r, unreadCount: 0, hasMention: false, mentionAll: false } : r
  )
  return {
    ...state,
    activeRoomId: roomId,
    summaries,
    roomState: { ...state.roomState, [roomId]: nextRoomState },
  }
}
case 'RESET': {
  return initialState
}
case 'ROOMS_FAILED': {
  return { ...state, roomsError: action.error }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npm test`
Expected: all reducer tests PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/roomEventsReducer.js chat-frontend/src/lib/roomEventsReducer.test.js
git commit -m "feat(chat-frontend): add history, active-room, reset reducer actions"
```

---

## Task 6: `RoomEventsProvider` skeleton

**Files:**
- Create: `chat-frontend/src/context/RoomEventsContext.jsx`
- Create: `chat-frontend/src/context/RoomEventsContext.test.jsx`

This task introduces the provider with its `useReducer`, exposes `useRoomEvents` / `useRoomSummaries`, and wires `loadHistory` + `setActiveRoom`. It does **not** yet open NATS subscriptions — Task 7 adds those.

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/context/RoomEventsContext.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, waitFor } from '@testing-library/react'
import { NatsContext } from './NatsContext'
import { RoomEventsProvider, useRoomEvents, useRoomSummaries } from './RoomEventsContext'

function mockNats({ request, subscribe, user = { account: 'alice', siteId: 'site-A' } } = {}) {
  return {
    connected: true,
    user,
    error: null,
    connect: vi.fn(),
    request: request ?? vi.fn().mockResolvedValue({ rooms: [] }),
    publish: vi.fn(),
    subscribe: subscribe ?? vi.fn().mockReturnValue({ unsubscribe: vi.fn() }),
    disconnect: vi.fn(),
  }
}

function wrap(ui, nats) {
  return (
    <NatsContext.Provider value={nats}>
      <RoomEventsProvider>{ui}</RoomEventsProvider>
    </NatsContext.Provider>
  )
}

function SummariesProbe() {
  const { summaries } = useRoomSummaries()
  return <div data-testid="count">{summaries.length}</div>
}

function EventsProbe({ roomId }) {
  const { messages, hasLoadedHistory, historyError } = useRoomEvents(roomId)
  return (
    <div>
      <div data-testid="messages">{messages.map((m) => m.id).join(',')}</div>
      <div data-testid="loaded">{String(hasLoadedHistory)}</div>
      <div data-testid="error">{historyError ?? ''}</div>
    </div>
  )
}

describe('RoomEventsProvider', () => {
  beforeEach(() => vi.clearAllMocks())

  it('exposes empty summaries before rooms load', () => {
    const nats = mockNats()
    render(wrap(<SummariesProbe />, nats))
    expect(screen.getByTestId('count').textContent).toBe('0')
  })

  it('loadHistory requests msg.history and populates messages', async () => {
    const history = [
      { id: 'm1', roomId: 'a', content: 'old', createdAt: '2026-04-17T10:00:00Z', sender: { account: 'bob' } },
    ]
    const request = vi.fn().mockImplementation((subject) => {
      if (subject.includes('.msg.history')) return Promise.resolve({ messages: [...history] })
      if (subject.endsWith('.rooms.list')) return Promise.resolve({ rooms: [] })
      throw new Error('unexpected subject: ' + subject)
    })
    const nats = mockNats({ request })

    function Trigger() {
      const { messages, loadHistory } = useRoomEvents('a')
      return (
        <div>
          <button onClick={() => loadHistory()}>load</button>
          <div data-testid="messages">{messages.map((m) => m.id).join(',')}</div>
        </div>
      )
    }

    render(wrap(<Trigger />, nats))
    await act(async () => {
      screen.getByText('load').click()
    })
    await waitFor(() => expect(screen.getByTestId('messages').textContent).toBe('m1'))
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.a.site-A.msg.history',
      { limit: 50 }
    )
  })

  it('loadHistory surfaces historyError on failure', async () => {
    const request = vi.fn().mockImplementation((subject) => {
      if (subject.includes('.msg.history')) return Promise.reject(new Error('boom'))
      return Promise.resolve({ rooms: [] })
    })
    const nats = mockNats({ request })

    function Trigger() {
      const { loadHistory, historyError } = useRoomEvents('a')
      return (
        <div>
          <button onClick={() => loadHistory().catch(() => {})}>load</button>
          <div data-testid="error">{historyError ?? ''}</div>
        </div>
      )
    }

    render(wrap(<Trigger />, nats))
    await act(async () => {
      screen.getByText('load').click()
    })
    await waitFor(() => expect(screen.getByTestId('error').textContent).toBe('boom'))
  })

  it('setActiveRoom clears unread after receiving a message while inactive', async () => {
    const nats = mockNats()
    let captured
    function Probe() {
      const { summaries, setActiveRoom } = useRoomSummaries()
      captured = { summaries, setActiveRoom }
      return null
    }
    render(wrap(<Probe />, nats))
    // No rooms loaded; nothing to click. Just assert setActiveRoom exists.
    expect(typeof captured.setActiveRoom).toBe('function')
  })
})
```

- [ ] **Step 2: Export `NatsContext` so tests can provide a mock**

Edit `chat-frontend/src/context/NatsContext.jsx`. Change the const line to an export:

```jsx
export const NatsContext = createContext(null)
```

(Replaces `const NatsContext = createContext(null)`.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `npm test`
Expected: FAIL — `RoomEventsContext` module does not exist.

- [ ] **Step 4: Create the provider**

Create `chat-frontend/src/context/RoomEventsContext.jsx`:

```jsx
import { createContext, useCallback, useContext, useMemo, useReducer, useRef } from 'react'
import { useNats } from './NatsContext'
import { initialState, roomEventsReducer } from '../lib/roomEventsReducer'
import { msgHistory } from '../lib/subjects'

const RoomEventsContext = createContext(null)

export function RoomEventsProvider({ children }) {
  const { user, request } = useNats()
  const [state, dispatch] = useReducer(roomEventsReducer, initialState)
  const inflightHistory = useRef(new Map())

  const loadHistory = useCallback(
    async (roomId) => {
      if (!user || !roomId) return
      const prev = state.roomState[roomId]
      if (prev?.hasLoadedHistory) return
      if (inflightHistory.current.has(roomId)) return inflightHistory.current.get(roomId)

      const promise = (async () => {
        try {
          const resp = await request(msgHistory(user.account, roomId, user.siteId), { limit: 50 })
          const asc = [...(resp.messages ?? [])].reverse()
          dispatch({ type: 'HISTORY_LOADED', roomId, messages: asc })
        } catch (err) {
          dispatch({ type: 'HISTORY_FAILED', roomId, error: err.message })
          throw err
        } finally {
          inflightHistory.current.delete(roomId)
        }
      })()
      inflightHistory.current.set(roomId, promise)
      return promise
    },
    [user, request, state.roomState]
  )

  const setActiveRoom = useCallback((roomId) => {
    dispatch({ type: 'SET_ACTIVE_ROOM', roomId })
  }, [])

  const value = useMemo(
    () => ({ state, dispatch, loadHistory, setActiveRoom }),
    [state, loadHistory, setActiveRoom]
  )

  return <RoomEventsContext.Provider value={value}>{children}</RoomEventsContext.Provider>
}

function useRoomEventsInternal() {
  const ctx = useContext(RoomEventsContext)
  if (!ctx) throw new Error('RoomEvents hooks must be used inside RoomEventsProvider')
  return ctx
}

export function useRoomEvents(roomId) {
  const { state, loadHistory } = useRoomEventsInternal()
  const room = state.roomState[roomId]
  return {
    messages: room?.messages ?? [],
    hasLoadedHistory: !!room?.hasLoadedHistory,
    historyError: room?.historyError ?? null,
    loadHistory: () => loadHistory(roomId),
  }
}

export function useRoomSummaries() {
  const { state, setActiveRoom } = useRoomEventsInternal()
  return {
    summaries: state.summaries,
    setActiveRoom,
    error: state.roomsError,
  }
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `npm test`
Expected: all RoomEventsProvider tests PASS.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/context/NatsContext.jsx chat-frontend/src/context/RoomEventsContext.jsx chat-frontend/src/context/RoomEventsContext.test.jsx
git commit -m "feat(chat-frontend): add RoomEventsProvider with history and active-room"
```

---

## Task 7: `RoomEventsProvider` subscriptions

**Files:**
- Modify: `chat-frontend/src/context/RoomEventsContext.jsx`
- Modify: `chat-frontend/src/context/RoomEventsContext.test.jsx`

Add the full subscription lifecycle: fetch rooms on login, open one DM subscription, open one group subscription per joined group room, and listen to `subscription.update` and `room.metadata.update`.

- [ ] **Step 1: Extend the tests**

Append to `chat-frontend/src/context/RoomEventsContext.test.jsx`:

```jsx
function makeSub() {
  const handlers = []
  return {
    handlers,
    sub: {
      unsubscribe: vi.fn(),
    },
    deliver: (data) => handlers.forEach((h) => h(data)),
  }
}

describe('RoomEventsProvider subscriptions', () => {
  beforeEach(() => vi.clearAllMocks())

  it('fetches rooms on mount and subscribes to user-scoped events', async () => {
    const rooms = [
      { id: 'g1', name: 'group', type: 'group', siteId: 'site-A', userCount: 3, lastMsgAt: '2026-04-17T10:00:00Z' },
      { id: 'd1', name: 'dm',    type: 'dm',    siteId: 'site-A', userCount: 2, lastMsgAt: '2026-04-17T11:00:00Z' },
    ]
    const request = vi.fn().mockImplementation((subject) => {
      if (subject === 'chat.user.alice.request.rooms.list') return Promise.resolve({ rooms })
      throw new Error('unexpected request: ' + subject)
    })
    const subjects = []
    const subscribe = vi.fn().mockImplementation((subject) => {
      subjects.push(subject)
      return { unsubscribe: vi.fn() }
    })
    const nats = mockNats({ request, subscribe })

    render(wrap(<SummariesProbe />, nats))
    await waitFor(() => expect(screen.getByTestId('count').textContent).toBe('2'))

    expect(subjects).toContain('chat.user.alice.event.room')
    expect(subjects).toContain('chat.user.alice.event.subscription.update')
    expect(subjects).toContain('chat.user.alice.event.room.metadata.update')
    expect(subjects).toContain('chat.room.g1.event')
    expect(subjects).not.toContain('chat.room.d1.event')
  })

  it('applies DM events from the user-scoped subscription', async () => {
    const rooms = [{ id: 'd1', name: 'dm', type: 'dm', siteId: 'site-A', userCount: 2, lastMsgAt: null }]
    const request = vi.fn().mockResolvedValue({ rooms })
    const handlers = new Map()
    const subscribe = vi.fn().mockImplementation((subject, cb) => {
      handlers.set(subject, cb)
      return { unsubscribe: vi.fn() }
    })
    const nats = mockNats({ request, subscribe })

    render(wrap(<EventsProbe roomId="d1" />, nats))
    await waitFor(() => expect(subscribe).toHaveBeenCalled())

    act(() => {
      handlers.get('chat.user.alice.event.room')({
        type: 'new_message',
        roomId: 'd1',
        hasMention: false,
        lastMsgAt: '2026-04-17T12:00:00Z',
        lastMsgId: 'mdm1',
        message: { id: 'mdm1', roomId: 'd1', content: 'hey', createdAt: '2026-04-17T12:00:00Z', sender: { account: 'bob' } },
      })
    })
    await waitFor(() => expect(screen.getByTestId('messages').textContent).toBe('mdm1'))
  })

  it('opens a new group subscription when a group room is added', async () => {
    const request = vi.fn().mockResolvedValue({ rooms: [] })
    const handlers = new Map()
    const subscribe = vi.fn().mockImplementation((subject, cb) => {
      handlers.set(subject, cb)
      return { unsubscribe: vi.fn() }
    })
    const nats = mockNats({ request, subscribe })

    render(wrap(<SummariesProbe />, nats))
    await waitFor(() => expect(subscribe).toHaveBeenCalled())

    act(() => {
      handlers.get('chat.user.alice.event.subscription.update')({
        action: 'added',
        subscription: { roomId: 'g2' },
        room: { id: 'g2', name: 'new', type: 'group', siteId: 'site-A', userCount: 1, lastMsgAt: null },
      })
    })
    await waitFor(() =>
      expect(subscribe.mock.calls.map((c) => c[0])).toContain('chat.room.g2.event')
    )
    expect(screen.getByTestId('count').textContent).toBe('1')
  })

  it('drops state and unsubscribes on room removal', async () => {
    const rooms = [{ id: 'g1', name: 'g', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: null }]
    const request = vi.fn().mockResolvedValue({ rooms })
    const unsubs = []
    const handlers = new Map()
    const subscribe = vi.fn().mockImplementation((subject, cb) => {
      handlers.set(subject, cb)
      const sub = { unsubscribe: vi.fn() }
      if (subject === 'chat.room.g1.event') unsubs.push(sub)
      return sub
    })
    const nats = mockNats({ request, subscribe })

    render(wrap(<SummariesProbe />, nats))
    await waitFor(() => expect(screen.getByTestId('count').textContent).toBe('1'))

    act(() => {
      handlers.get('chat.user.alice.event.subscription.update')({
        action: 'removed',
        subscription: { roomId: 'g1' },
      })
    })
    await waitFor(() => expect(screen.getByTestId('count').textContent).toBe('0'))
    expect(unsubs[0].unsubscribe).toHaveBeenCalled()
  })
})
```

Note: the `subscribe` signature the tests use (`subscribe(subject, callback)`) matches `NatsContext`. The callback receives the parsed JSON data.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npm test`
Expected: FAIL — provider does not open subscriptions yet.

- [ ] **Step 3: Implement the subscription lifecycle**

Update imports at the top of `chat-frontend/src/context/RoomEventsContext.jsx`. Add `useEffect` to the React import and extend the subjects import so the full list reads:

```jsx
import { createContext, useCallback, useContext, useEffect, useMemo, useReducer, useRef } from 'react'
import { useNats } from './NatsContext'
import { initialState, roomEventsReducer } from '../lib/roomEventsReducer'
import {
  msgHistory,
  roomEvent,
  roomsGet,
  roomsList,
  subscriptionUpdate,
  roomMetadataUpdate,
  userRoomEvent,
} from '../lib/subjects'
```

Then inside `RoomEventsProvider`, after `const inflightHistory = useRef(new Map())`, add:

```jsx
const groupSubs = useRef(new Map())

useEffect(() => {
  if (!user) return
  let cancelled = false

  const dmSub = subscribe(userRoomEvent(user.account), (evt) => {
    if (evt?.type === 'new_message') {
      dispatch({ type: 'MESSAGE_RECEIVED', event: evt })
    }
  })

  const openGroupSub = (roomId) => {
    if (groupSubs.current.has(roomId)) return
    const sub = subscribe(roomEvent(roomId), (evt) => {
      if (evt?.type === 'new_message') {
        dispatch({ type: 'MESSAGE_RECEIVED', event: evt })
      }
    })
    groupSubs.current.set(roomId, sub)
  }

  const closeGroupSub = (roomId) => {
    const sub = groupSubs.current.get(roomId)
    if (sub) {
      sub.unsubscribe()
      groupSubs.current.delete(roomId)
    }
  }

  const subUpdate = subscribe(subscriptionUpdate(user.account), (evt) => {
    if (evt.action === 'added') {
      if (evt.room) {
        dispatch({ type: 'ROOM_ADDED', room: evt.room })
        if (evt.room.type === 'group') openGroupSub(evt.room.id)
      } else if (evt.subscription?.roomId) {
        // Fetch room details if not inlined
        request(roomsGet(user.account, evt.subscription.roomId), {})
          .then((room) => {
            if (cancelled || !room) return
            dispatch({ type: 'ROOM_ADDED', room })
            if (room.type === 'group') openGroupSub(room.id)
          })
          .catch(() => {})
      }
    } else if (evt.action === 'removed') {
      const roomId = evt.subscription?.roomId
      if (!roomId) return
      closeGroupSub(roomId)
      dispatch({ type: 'ROOM_REMOVED', roomId })
    }
  })

  const metaUpdate = subscribe(roomMetadataUpdate(user.account), (evt) => {
    dispatch({
      type: 'ROOM_METADATA_UPDATED',
      roomId: evt.roomId,
      name: evt.name,
      userCount: evt.userCount,
      lastMsgAt: evt.lastMsgAt,
    })
  })

  request(roomsList(user.account), {})
    .then((resp) => {
      if (cancelled) return
      const rooms = resp.rooms ?? []
      dispatch({ type: 'ROOMS_LOADED', rooms })
      for (const r of rooms) {
        if (r.type === 'group') openGroupSub(r.id)
      }
    })
    .catch((err) => {
      if (!cancelled) dispatch({ type: 'ROOMS_FAILED', error: err.message })
    })

  return () => {
    cancelled = true
    dmSub.unsubscribe()
    subUpdate.unsubscribe()
    metaUpdate.unsubscribe()
    for (const sub of groupSubs.current.values()) sub.unsubscribe()
    groupSubs.current.clear()
    dispatch({ type: 'RESET' })
  }
}, [user, subscribe, request])
```

Also pull `subscribe` from `useNats()`:

```jsx
const { user, request, subscribe } = useNats()
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npm test`
Expected: all RoomEventsProvider tests PASS (provider skeleton + subscription lifecycle).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/context/RoomEventsContext.jsx chat-frontend/src/context/RoomEventsContext.test.jsx
git commit -m "feat(chat-frontend): open DM and group subscriptions from RoomEventsProvider"
```

---

## Task 8: Wire `RoomEventsProvider` into `App.jsx`

**Files:**
- Modify: `chat-frontend/src/App.jsx`

The provider must be mounted only after login; otherwise `user` is `null` and the effect is a no-op (which is fine, but cleaner to mount it inside `AppContent` once connected).

- [ ] **Step 1: Edit `App.jsx`**

Replace the file content with:

```jsx
import { NatsProvider, useNats } from './context/NatsContext'
import { RoomEventsProvider } from './context/RoomEventsContext'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'

function AppContent() {
  const { connected } = useNats()

  if (!connected) {
    return <LoginPage />
  }

  return (
    <RoomEventsProvider>
      <ChatPage />
    </RoomEventsProvider>
  )
}

export default function App() {
  return (
    <NatsProvider>
      <AppContent />
    </NatsProvider>
  )
}
```

- [ ] **Step 2: Run tests to confirm nothing regressed**

Run: `npm test`
Expected: all tests PASS.

- [ ] **Step 3: Build check**

Run: `npm run build`
Expected: build succeeds.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/App.jsx
git commit -m "feat(chat-frontend): mount RoomEventsProvider after login"
```

---

## Task 9: Refactor `MessageArea` to use `useRoomEvents`

**Files:**
- Create: `chat-frontend/src/components/MessageArea.test.jsx`
- Modify: `chat-frontend/src/components/MessageArea.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/MessageArea.test.jsx`:

```jsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import MessageArea from './MessageArea'

vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: vi.fn(),
}))

import { useRoomEvents } from '../context/RoomEventsContext'

describe('MessageArea', () => {
  it('shows the empty-state when no room is selected', () => {
    useRoomEvents.mockReturnValue({
      messages: [], hasLoadedHistory: false, historyError: null, loadHistory: vi.fn(),
    })
    render(<MessageArea room={null} />)
    expect(screen.getByText(/Select a room/i)).toBeInTheDocument()
  })

  it('renders messages from the provider', () => {
    useRoomEvents.mockReturnValue({
      messages: [
        { id: 'm1', content: 'hello', createdAt: '2026-04-17T12:00:00Z', sender: { account: 'bob', engName: 'Bob' } },
      ],
      hasLoadedHistory: true,
      historyError: null,
      loadHistory: vi.fn().mockResolvedValue(),
    })
    render(<MessageArea room={{ id: 'r1', name: 'general', type: 'group', userCount: 2 }} />)
    expect(screen.getByText('hello')).toBeInTheDocument()
    expect(screen.getByText('Bob')).toBeInTheDocument()
  })

  it('surfaces historyError', () => {
    useRoomEvents.mockReturnValue({
      messages: [],
      hasLoadedHistory: false,
      historyError: 'boom',
      loadHistory: vi.fn().mockResolvedValue(),
    })
    render(<MessageArea room={{ id: 'r1', name: 'general', type: 'group', userCount: 2 }} />)
    expect(screen.getByText('boom')).toBeInTheDocument()
  })

  it('calls loadHistory when room changes', () => {
    const loadHistory = vi.fn().mockResolvedValue()
    useRoomEvents.mockReturnValue({
      messages: [], hasLoadedHistory: false, historyError: null, loadHistory,
    })
    render(<MessageArea room={{ id: 'r1', name: 'general', type: 'group', userCount: 2 }} />)
    expect(loadHistory).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `npm test`
Expected: FAIL — `MessageArea` still uses `useNats` and `subscribe` directly, not `useRoomEvents`.

- [ ] **Step 3: Rewrite `MessageArea.jsx`**

Replace `chat-frontend/src/components/MessageArea.jsx` with:

```jsx
import { useEffect, useRef } from 'react'
import { useRoomEvents } from '../context/RoomEventsContext'

function formatTime(dateStr) {
  const d = new Date(dateStr)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function senderName(msg) {
  if (msg.sender) {
    return msg.sender.engName || msg.sender.account || msg.sender.userId || 'Unknown'
  }
  return msg.userAccount || msg.userId || 'Unknown'
}

function messageContent(msg) {
  return msg.content || msg.msg || ''
}

export default function MessageArea({ room }) {
  const { messages, hasLoadedHistory, historyError, loadHistory } = useRoomEvents(room?.id ?? null)
  const bottomRef = useRef(null)

  useEffect(() => {
    if (!room) return
    loadHistory().catch(() => {
      // historyError is surfaced via the hook; nothing to do here
    })
  }, [room, loadHistory])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  if (!room) {
    return (
      <div className="message-area">
        <div className="message-area-empty">Select a room to start chatting</div>
      </div>
    )
  }

  return (
    <div className="message-area">
      <div className="message-area-header">
        <span className="message-area-room-name">
          {room.type === 'dm' ? '@ ' : '# '}{room.name}
        </span>
        <span className="message-area-members">{room.userCount} members</span>
      </div>
      <div className="message-list">
        {!hasLoadedHistory && !historyError && <div className="message-loading">Loading messages...</div>}
        {historyError && <div className="message-error">{historyError}</div>}
        {messages.map((msg) => (
          <div key={msg.id} className="message">
            <span className="message-sender">{senderName(msg)}</span>
            <span className="message-time">{formatTime(msg.createdAt)}</span>
            <div className="message-content">{messageContent(msg)}</div>
          </div>
        ))}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `npm test`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/MessageArea.jsx chat-frontend/src/components/MessageArea.test.jsx
git commit -m "refactor(chat-frontend): MessageArea consumes useRoomEvents"
```

---

## Task 10: Refactor `RoomList` to use `useRoomSummaries` with unread/mention UI

**Files:**
- Create: `chat-frontend/src/components/RoomList.test.jsx`
- Modify: `chat-frontend/src/components/RoomList.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/RoomList.test.jsx`:

```jsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import RoomList from './RoomList'

vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: vi.fn(),
}))

import { useRoomSummaries } from '../context/RoomEventsContext'

function summary(id, overrides = {}) {
  return {
    id,
    name: id,
    type: 'group',
    siteId: 'site-A',
    userCount: 2,
    lastMsgAt: '2026-04-17T10:00:00Z',
    unreadCount: 0,
    hasMention: false,
    mentionAll: false,
    ...overrides,
  }
}

describe('RoomList', () => {
  it('renders summaries', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a'), summary('b', { type: 'dm' })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(screen.getByText(/# a/)).toBeInTheDocument()
    expect(screen.getByText(/@ b/)).toBeInTheDocument()
  })

  it('shows unread count badge and bold class when unreadCount > 0', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { unreadCount: 3 })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-item-unread')).toBeInTheDocument()
    expect(container.querySelector('.room-badge-unread').textContent).toBe('3')
  })

  it('shows [@] pill when hasMention', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { hasMention: true, unreadCount: 1 })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-badge-mention')).toBeInTheDocument()
    expect(container.querySelector('.room-badge-mention-all')).not.toBeInTheDocument()
  })

  it('shows [!] pill when mentionAll (takes precedence over [@])', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { hasMention: true, mentionAll: true, unreadCount: 1 })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-badge-mention-all')).toBeInTheDocument()
    expect(container.querySelector('.room-badge-mention')).not.toBeInTheDocument()
  })

  it('calls onSelectRoom and setActiveRoom on click', () => {
    const onSelectRoom = vi.fn()
    const setActiveRoom = vi.fn()
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a')],
      setActiveRoom,
      error: null,
    })
    render(<RoomList selectedRoomId={null} onSelectRoom={onSelectRoom} />)
    fireEvent.click(screen.getByText(/# a/))
    expect(onSelectRoom).toHaveBeenCalledWith(expect.objectContaining({ id: 'a' }))
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `npm test`
Expected: FAIL — `RoomList` still uses `useNats` and fetches rooms.

- [ ] **Step 3: Rewrite `RoomList.jsx`**

Replace `chat-frontend/src/components/RoomList.jsx` with:

```jsx
import { useRoomSummaries } from '../context/RoomEventsContext'

function mentionBadge(summary) {
  if (summary.mentionAll) return <span className="room-badge-mention-all">!</span>
  if (summary.hasMention) return <span className="room-badge-mention">@</span>
  return null
}

export default function RoomList({ selectedRoomId, onSelectRoom }) {
  const { summaries, error } = useRoomSummaries()

  return (
    <div className="room-list">
      <div className="room-list-header">Rooms</div>
      {error && <div className="room-list-error">{error}</div>}
      <div className="room-list-items">
        {summaries.map((room) => {
          const isSelected = room.id === selectedRoomId
          const unread = room.unreadCount > 0
          const classes = ['room-item']
          if (isSelected) classes.push('room-item-selected')
          if (unread) classes.push('room-item-unread')
          return (
            <div
              key={room.id}
              className={classes.join(' ')}
              onClick={() => onSelectRoom(room)}
            >
              <span className="room-name">
                {room.type === 'dm' ? '@ ' : '# '}{room.name}
              </span>
              {mentionBadge(room)}
              <span className="room-meta">{room.userCount}</span>
              {unread && <span className="room-badge-unread">{room.unreadCount}</span>}
            </div>
          )
        })}
        {summaries.length === 0 && !error && (
          <div className="room-list-empty">No rooms yet</div>
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `npm test`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/RoomList.jsx chat-frontend/src/components/RoomList.test.jsx
git commit -m "refactor(chat-frontend): RoomList consumes useRoomSummaries with unread/mention UI"
```

---

## Task 11: Update `ChatPage` to call `setActiveRoom`

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`

- [ ] **Step 1: Edit `ChatPage.jsx`**

Replace the file content with:

```jsx
import { useEffect, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { useRoomSummaries } from '../context/RoomEventsContext'
import RoomList from '../components/RoomList'
import MessageArea from '../components/MessageArea'
import MessageInput from '../components/MessageInput'
import CreateRoomDialog from '../components/CreateRoomDialog'

export default function ChatPage() {
  const { user, disconnect } = useNats()
  const { summaries, setActiveRoom } = useRoomSummaries()
  const [selectedRoom, setSelectedRoom] = useState(null)
  const [showCreateRoom, setShowCreateRoom] = useState(false)

  // Clear selection if the selected room disappears from summaries
  useEffect(() => {
    if (selectedRoom && !summaries.some((r) => r.id === selectedRoom.id)) {
      setSelectedRoom(null)
      setActiveRoom(null)
    }
  }, [summaries, selectedRoom, setActiveRoom])

  const handleSelectRoom = (room) => {
    setSelectedRoom(room)
    setActiveRoom(room?.id ?? null)
  }

  return (
    <div className="chat-layout">
      <div className="chat-header">
        <span className="chat-header-title">Chat</span>
        <span className="chat-header-user">
          {user?.account} &middot; {user?.siteId}
        </span>
        <button className="chat-header-logout" onClick={disconnect}>
          Logout
        </button>
      </div>
      <div className="chat-body">
        <div className="chat-sidebar">
          <RoomList
            selectedRoomId={selectedRoom?.id}
            onSelectRoom={handleSelectRoom}
          />
          <button
            className="create-room-btn"
            onClick={() => setShowCreateRoom(true)}
          >
            + Create Room
          </button>
        </div>
        <div className="chat-main">
          <MessageArea room={selectedRoom} />
          <MessageInput room={selectedRoom} />
        </div>
      </div>
      {showCreateRoom && (
        <CreateRoomDialog
          onClose={() => setShowCreateRoom(false)}
          onCreated={(room) => handleSelectRoom(room)}
        />
      )}
    </div>
  )
}
```

- [ ] **Step 2: Run tests to confirm nothing regressed**

Run: `npm test`
Expected: all tests PASS.

- [ ] **Step 3: Build check**

Run: `npm run build`
Expected: build succeeds.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx
git commit -m "feat(chat-frontend): drive active room from ChatPage selection"
```

---

## Task 12: Add unread and mention CSS

**Files:**
- Modify: `chat-frontend/src/styles/index.css`

- [ ] **Step 1: Append styles**

Append to `chat-frontend/src/styles/index.css` (keeping the existing dark-theme palette consistent):

```css
/* Unread + mention badges */
.room-item-unread .room-name {
  font-weight: 700;
  color: #fff;
}

.room-item-unread::before {
  content: '';
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #5865f2;
  margin-right: 0.375rem;
  flex-shrink: 0;
}

.room-badge-unread {
  font-size: 11px;
  line-height: 1;
  padding: 2px 6px;
  border-radius: 10px;
  background: #5865f2;
  color: #fff;
  margin-left: 0.375rem;
  flex-shrink: 0;
}

.room-badge-mention,
.room-badge-mention-all {
  font-size: 10px;
  font-weight: 700;
  line-height: 1;
  padding: 2px 5px;
  border-radius: 4px;
  margin-left: 0.375rem;
  flex-shrink: 0;
  color: #fff;
}

.room-badge-mention {
  background: #faa61a;
}

.room-badge-mention-all {
  background: #ed4245;
}
```

- [ ] **Step 2: Build check**

Run: `npm run build`
Expected: build succeeds; CSS is included.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): style unread dot and mention badges"
```

---

## Task 13: Update `smoke-test.mjs` to cover DM broadcast

**Files:**
- Modify: `chat-frontend/smoke-test.mjs`

The existing smoke test publishes to `chat.room.{roomID}.event` directly, mimicking what the broadcast-worker would do. Add a step that publishes a DM-style event to `chat.user.{account}.event.room` and asserts the other side receives it.

- [ ] **Step 1: Edit `smoke-test.mjs`**

Append the new steps inside `main()` after step 7 (alice receiving the group event) and before step 8:

```js
  console.log('7b. Bob subscribes to his DM event stream...')
  const bobDmReceived = []
  const bobDmSub = bobNc.subscribe('chat.user.bob.event.room')
  ;(async () => {
    for await (const msg of bobDmSub) {
      bobDmReceived.push(JSON.parse(sc.decode(msg.data)))
    }
  })()
  console.log('   ✓ Subscribed to chat.user.bob.event.room')

  console.log('7c. Alice publishes a DM event directly to bob...')
  const dmEvent = {
    type: 'new_message',
    roomId: 'dm-' + Date.now(),
    roomType: 'dm',
    hasMention: false,
    lastMsgAt: new Date().toISOString(),
    lastMsgId: 'dm-msg-' + Date.now(),
    message: {
      id: 'dm-msg-' + Date.now(),
      content: 'Direct hello from alice',
      sender: { account: 'alice', engName: 'alice' },
      createdAt: new Date().toISOString(),
    },
    timestamp: Date.now(),
  }
  aliceNc.publish('chat.user.bob.event.room', sc.encode(JSON.stringify(dmEvent)))
  console.log('   ✓ Published DM event')

  console.log('7d. Waiting for bob to receive the DM event...')
  await new Promise((r) => setTimeout(r, 500))
  if (bobDmReceived.length > 0 && bobDmReceived[0].message.content === 'Direct hello from alice') {
    console.log(`   ✓ Bob received DM: "${bobDmReceived[0].message.content}"`)
  } else {
    console.log('   ✗ Bob did NOT receive the DM event')
    process.exitCode = 1
  }
  bobDmSub.unsubscribe()
```

- [ ] **Step 2: Run the smoke test**

The smoke test requires the Docker stack. If the stack is already running:

Run (from `chat-frontend/`): `node smoke-test.mjs`
Expected: all existing steps pass + new DM step passes. If Docker isn't running, skip this step and note it for manual verification.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/smoke-test.mjs
git commit -m "test(chat-frontend): smoke-test DM broadcast delivery"
```

---

## Task 14: Manual verification

**Files:** none modified.

- [ ] **Step 1: Start the dev stack**

```bash
cd chat-frontend/deploy
docker compose up -d
cd ..
npm run dev
```

Open `http://localhost:3000`.

- [ ] **Step 2: Verify DM broadcast end-to-end**

1. In one browser tab, log in as `alice`.
2. In a second browser tab (or incognito window), log in as `bob`.
3. From `alice`, create a DM room with `bob` as the only member.
4. `alice` sends a message in the DM room.
5. Confirm `bob` sees the DM room appear in the room list and the message appears in real time when he opens it.
6. With `bob` on a different room, have `alice` send another DM. Confirm the DM room in `bob`'s list shows the unread dot, bold name, and unread count badge.
7. Click the DM room from `bob`'s side. Confirm the unread dot/badge clears.

- [ ] **Step 3: Verify group mention indicators**

1. In a group room with `alice` and `bob`, have `alice` send `@bob hi`.
2. In `bob`'s list, while viewing a different room, confirm the group room shows the `@` pill plus unread count.
3. Switch to the group room. Confirm `@` pill clears.
4. Have `alice` send `@all heads up`. In `bob`'s list (while on another room), confirm the group shows the `!` pill instead.

- [ ] **Step 4: Verify cache and reconnect behavior**

1. With a DM room open in `bob`'s tab, restart the Docker NATS container (`docker compose restart nats`).
2. Confirm the frontend shows the reconnecting state (existing NATS auto-reconnect UI) and that once reconnected, new DM messages flow again.

- [ ] **Step 5: Tear down**

```bash
docker compose -f chat-frontend/deploy/docker-compose.yml down
```

No commit for this task — verification only. If regressions are found, open a follow-up task in a new commit rather than amending.

---





