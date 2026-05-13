# Thread Side-Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a right-hand thread side-panel to `chat-frontend`, plus quote-reply, Edit/Delete hover actions, and the layout split (AppHeader + Sidebar + ChatPage + ThreadRightBar). Frontend-only PR; no backend changes.

**Architecture:** Approach A from the spec — refactor `MessageArea` / `MessageInput` into presentational components (`MessageList`, `MessageRow`, `MessageInputForm`, `QuotedBlock`, `MessageActions`) plus thin containers that bind them to either `RoomEventsContext` (main feed) or a new `ThreadEventsContext` (thread panel). State for the open thread lives in a new `threadEventsReducer.js` mirroring `roomEventsReducer.js`. Thread replies are filtered out of the main feed client-side (`broadcast-worker` publishes them on the main subject today).

**Tech Stack:** React 18 + Vite, `@testing-library/react`, `vitest`, existing CSS tokens (`src/styles/tokens.css`).

**Spec:** `docs/superpowers/specs/2026-05-13-thread-panel-design.md` is the source of truth. If anything in the plan disagrees with the spec, the spec wins — open the disagreement, fix, then continue.

**Workflow rules for every task:**
- **TDD red-green-refactor.** Write the test first, run it, see it fail with a specific error. Implement minimal code. Run again. See it pass. Then refactor if needed.
- **Run `make lint` before every commit.** Existing pre-commit hook enforces it; failing locally first is cheaper.
- **Run vitest in the focused scope first**, then the full suite before committing.
- **Never edit `mock_store_test.go`** or any generated mock. (Frontend has no mockgen; only relevant if you stray into Go.)
- **One commit per task.** Conventional commits: `feat(chat-frontend): …`, `test(chat-frontend): …`, `refactor(chat-frontend): …`.
- **Do not implement features beyond the current task.** The next task assumes the previous task's state.

**Commands quick-reference (run from repo root):**
- Run all chat-frontend tests: `cd chat-frontend && npm test -- --run`
- Run one test file: `cd chat-frontend && npx vitest run src/lib/messageBuffer.test.js`
- Run one test by name: `cd chat-frontend && npx vitest run -t "appendBounded drops oldest when over MAX_CACHED"`
- Lint: `make lint`
- Format: `make fmt`

---

## Chapter 1 — Foundation utilities

Goal: extract shared message-buffer helpers and add the three new NATS subject builders. Nothing user-visible changes yet; this lays groundwork all later chapters depend on.

### Task 1.1: Create `src/lib/messageBuffer.js` with `appendBounded` + `mergeById`

The current `roomEventsReducer.js` inlines `appendBounded` (lines 53–60) and a one-off de-dup Set in `HISTORY_LOADED` (lines 183–186). Both reducers (existing room reducer + the new thread reducer in Chapter 6) need the same logic. `mergeById` MUST preserve `_local: true` and `_status: 'failed'` markers when merging — those flags only exist on optimistic rows and would be lost if we naively replaced from the incoming side.

**Files:**
- Create: `chat-frontend/src/lib/messageBuffer.js`
- Create: `chat-frontend/src/lib/messageBuffer.test.js`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/lib/messageBuffer.test.js`:

```js
import { describe, it, expect } from 'vitest'
import { appendBounded, mergeById, MAX_CACHED } from './messageBuffer'

describe('appendBounded', () => {
  it('appends a new message', () => {
    expect(appendBounded([{ id: 'a' }], { id: 'b' })).toEqual([{ id: 'a' }, { id: 'b' }])
  })

  it('is a no-op when the id already exists', () => {
    const input = [{ id: 'a' }, { id: 'b' }]
    expect(appendBounded(input, { id: 'a' })).toBe(input)
  })

  it('slices oldest off when length exceeds MAX_CACHED', () => {
    const seed = Array.from({ length: MAX_CACHED }, (_, i) => ({ id: String(i) }))
    const result = appendBounded(seed, { id: 'new' })
    expect(result).toHaveLength(MAX_CACHED)
    expect(result[0]).toEqual({ id: '1' })
    expect(result[result.length - 1]).toEqual({ id: 'new' })
  })
})

describe('mergeById', () => {
  it('dedupes by id and preserves order (incoming first, then existing)', () => {
    const existing = [{ id: 'b', content: 'old-b' }, { id: 'c' }]
    const incoming = [{ id: 'a' }, { id: 'b', content: 'new-b' }]
    const result = mergeById(existing, incoming)
    expect(result).toEqual([{ id: 'a' }, { id: 'b', content: 'new-b' }, { id: 'c' }])
  })

  it('preserves _local: true on the existing row when an incoming row with the same id arrives', () => {
    const existing = [{ id: 'a', _local: true, _status: 'failed' }]
    const incoming = [{ id: 'a', content: 'server-confirmed' }]
    const result = mergeById(existing, incoming)
    expect(result).toHaveLength(1)
    expect(result[0]).toEqual({ id: 'a', content: 'server-confirmed', _local: true, _status: 'failed' })
  })

  it('does not invent _local on rows that never had it', () => {
    const result = mergeById([{ id: 'a' }], [{ id: 'b' }])
    expect(result[0]).not.toHaveProperty('_local')
    expect(result[1]).not.toHaveProperty('_local')
  })

  it('caps total length at MAX_CACHED, dropping oldest', () => {
    const existing = Array.from({ length: MAX_CACHED }, (_, i) => ({ id: `e${i}` }))
    const incoming = [{ id: 'new-1' }, { id: 'new-2' }]
    const result = mergeById(existing, incoming)
    expect(result).toHaveLength(MAX_CACHED)
    expect(result[0]).toEqual({ id: 'new-2' })
    expect(result[result.length - 1]).toEqual({ id: `e${MAX_CACHED - 1}` })
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/lib/messageBuffer.test.js`
Expected: FAIL — `Cannot find module './messageBuffer'`.

- [ ] **Step 3: Write the implementation**

Create `chat-frontend/src/lib/messageBuffer.js`:

```js
export const MAX_CACHED = 200

export function appendBounded(messages, msg) {
  if (messages.some((m) => m.id === msg.id)) return messages
  const next = [...messages, msg]
  if (next.length > MAX_CACHED) {
    return next.slice(next.length - MAX_CACHED)
  }
  return next
}

// mergeById merges `incoming` ahead of `existing`, dedupes by `id`, and
// preserves any `_local` / `_status` markers that live only on the existing
// rows (the server doesn't know about them). Order: incoming rows keep their
// relative order at the front; existing rows that aren't in incoming follow.
export function mergeById(existing, incoming) {
  const incomingById = new Map()
  for (const m of incoming) incomingById.set(m.id, m)

  const merged = []
  const seen = new Set()
  for (const m of incoming) {
    const ex = existing.find((e) => e.id === m.id)
    if (ex) {
      const out = { ...m }
      if (ex._local) out._local = ex._local
      if (ex._status) out._status = ex._status
      merged.push(out)
    } else {
      merged.push(m)
    }
    seen.add(m.id)
  }
  for (const e of existing) {
    if (!seen.has(e.id)) merged.push(e)
  }
  if (merged.length > MAX_CACHED) {
    return merged.slice(merged.length - MAX_CACHED)
  }
  return merged
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/lib/messageBuffer.test.js`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/messageBuffer.js chat-frontend/src/lib/messageBuffer.test.js
git commit -m "feat(chat-frontend): extract messageBuffer utility (appendBounded, mergeById)"
```

### Task 1.2: Refactor `roomEventsReducer.js` to use `messageBuffer`

Replace the inlined `appendBounded` and the `HISTORY_LOADED` de-dup Set with imports from `messageBuffer.js`. No behaviour change — the existing reducer test suite must still pass unchanged.

**Files:**
- Modify: `chat-frontend/src/lib/roomEventsReducer.js`

- [ ] **Step 1: Verify existing tests pass before the change**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js`
Expected: all existing tests PASS.

- [ ] **Step 2: Apply the refactor**

In `chat-frontend/src/lib/roomEventsReducer.js`:

Replace the top constant + helper block:

```js
export const MAX_CACHED = 200

export const BUFFER_MODE = {
  LIVE: 'live',
  HISTORICAL: 'historical',
}
```
… and the in-file `appendBounded` (lines 53–60) with:

```js
import { appendBounded, mergeById, MAX_CACHED } from './messageBuffer'

export { MAX_CACHED }

export const BUFFER_MODE = {
  LIVE: 'live',
  HISTORICAL: 'historical',
}
```

(Delete the local `appendBounded` function definition entirely.)

Replace the body of `case 'HISTORY_LOADED'` (lines 181–201) with:

```js
case 'HISTORY_LOADED': {
  const prev = state.roomState[action.roomId] ?? emptyRoomState()
  const merged = mergeById(prev.messages, action.messages)
  return {
    ...state,
    roomState: {
      ...state.roomState,
      [action.roomId]: {
        ...prev,
        messages: merged,
        hasLoadedHistory: true,
        historyError: null,
      },
    },
  }
}
```

Note the change: today's code splices incoming **before** existing because the action carries older history. `mergeById` puts incoming first too, so the visible order is preserved. Re-read the test cases in `roomEventsReducer.test.js` that target `HISTORY_LOADED` to be sure.

- [ ] **Step 3: Run the room reducer tests**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js`
Expected: all PASS unchanged.

- [ ] **Step 4: Run the full chat-frontend suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/roomEventsReducer.js
git commit -m "refactor(chat-frontend): use messageBuffer in roomEventsReducer"
```

### Task 1.3: Add `msgThread`, `msgEdit`, `msgDelete` subject builders

The thread RPC and edit/delete RPCs exist server-side (`history-service/internal/service/messages.go:322, 401`, `history-service/internal/service/threads.go:14`) but the frontend has no builders for them. Add all three at once — Chapters 4 (Edit/Delete) and 7 (Thread) will both need them.

**Files:**
- Modify: `chat-frontend/src/lib/subjects.js`
- Modify: `chat-frontend/src/lib/subjects.test.js`

- [ ] **Step 1: Write the failing tests**

Append to `chat-frontend/src/lib/subjects.test.js`:

```js
import { msgThread, msgEdit, msgDelete } from './subjects'

describe('msgThread', () => {
  it('builds the thread RPC subject', () => {
    expect(msgThread('alice', 'r1', 'site-1')).toBe(
      'chat.user.alice.request.room.r1.site-1.msg.thread'
    )
  })
})

describe('msgEdit', () => {
  it('builds the edit RPC subject', () => {
    expect(msgEdit('alice', 'r1', 'site-1')).toBe(
      'chat.user.alice.request.room.r1.site-1.msg.edit'
    )
  })
})

describe('msgDelete', () => {
  it('builds the delete RPC subject', () => {
    expect(msgDelete('alice', 'r1', 'site-1')).toBe(
      'chat.user.alice.request.room.r1.site-1.msg.delete'
    )
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd chat-frontend && npx vitest run src/lib/subjects.test.js`
Expected: FAIL — the three named exports don't exist.

- [ ] **Step 3: Add the builders**

In `chat-frontend/src/lib/subjects.js`, append after `msgSurrounding`:

```js
export function msgThread(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.msg.thread`
}

export function msgEdit(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.msg.edit`
}

export function msgDelete(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.msg.delete`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd chat-frontend && npx vitest run src/lib/subjects.test.js`
Expected: PASS (all three new tests).

- [ ] **Step 5: Lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/lib/subjects.js chat-frontend/src/lib/subjects.test.js
git commit -m "feat(chat-frontend): add msgThread, msgEdit, msgDelete subject builders"
```

---

*Chapter 2 follows — Layout shell (AppHeader, Sidebar, MainApp, slimmed ChatPage).*
