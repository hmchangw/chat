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

## Chapter 2 — Layout shell

Goal: split today's monolithic `ChatPage` into a three-column shell. `AppHeader` carries global controls (search, theme, user, logout). `Sidebar` carries the room list + Create button. `MainApp` is the shell that wires the row layout and providers. `ChatPage` slims down to the middle column with its own small room-header strip (name + Members + Leave). No thread features yet; `ThreadRightBar` is added in Chapter 7.

CSS: today's `.chat-layout`, `.chat-header`, `.chat-sidebar`, `.chat-main`, `.chat-main-with-side-panel`, `.chat-main-content` are in `src/styles/index.css`. We **rename** the page-level layout selectors (`.chat-layout` → `.app-shell`, etc.) and keep the inner `.chat-main-*` selectors so the in-chapter-3 message components don't move twice. Concrete CSS changes are itemised in Task 2.7.

### Task 2.1: Extract `AppHeader.jsx`

`AppHeader` renders the four global controls in this order: SearchBar | user chip | ThemeToggle | Logout. The "Chat" title is dropped (low signal in a single-app screen). Members + Leave move out of this strip in Task 2.5.

**Files:**
- Create: `chat-frontend/src/components/AppHeader.jsx`
- Create: `chat-frontend/src/components/AppHeader.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/AppHeader.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import AppHeader from './AppHeader'

vi.mock('../context/NatsContext', () => ({
  useNats: () => ({
    user: { account: 'alice', siteId: 'site-1' },
    disconnect: vi.fn(),
  }),
}))
vi.mock('./SearchBar', () => ({
  default: ({ onEnterSearch }) => (
    <button type="button" onClick={() => onEnterSearch?.('q')}>fake-search</button>
  ),
}))
vi.mock('./ThemeToggle', () => ({ default: () => <span>fake-theme</span> }))

describe('AppHeader', () => {
  it('renders user chip, theme toggle, logout', () => {
    render(<AppHeader onSelectRoom={() => {}} onEnterSearch={() => {}} />)
    expect(screen.getByText('alice · site-1')).toBeInTheDocument()
    expect(screen.getByText('fake-theme')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /logout/i })).toBeInTheDocument()
  })

  it('clicking Logout invokes disconnect', async () => {
    const disconnect = vi.fn()
    vi.doMock('../context/NatsContext', () => ({
      useNats: () => ({ user: { account: 'a', siteId: 's' }, disconnect }),
    }))
    // re-import after re-mock
    const { default: Re } = await import('./AppHeader')
    render(<Re onSelectRoom={() => {}} onEnterSearch={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /logout/i }))
    expect(disconnect).toHaveBeenCalled()
  })

  it('forwards onSelectRoom and onEnterSearch to the search bar', () => {
    const onEnterSearch = vi.fn()
    render(<AppHeader onSelectRoom={() => {}} onEnterSearch={onEnterSearch} />)
    fireEvent.click(screen.getByText('fake-search'))
    expect(onEnterSearch).toHaveBeenCalledWith('q')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/AppHeader.test.jsx`
Expected: FAIL — `Cannot find module './AppHeader'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/AppHeader.jsx`:

```jsx
import { useNats } from '../context/NatsContext'
import SearchBar from './SearchBar'
import ThemeToggle from './ThemeToggle'

export default function AppHeader({ onSelectRoom, onEnterSearch }) {
  const { user, disconnect } = useNats()

  return (
    <header className="app-header">
      <div className="app-header-search">
        <SearchBar onSelectRoom={onSelectRoom} onEnterSearch={onEnterSearch} />
      </div>
      <span className="app-header-user">
        {user?.account} · {user?.siteId}
      </span>
      <ThemeToggle />
      <button type="button" className="app-header-logout" onClick={disconnect}>
        Logout
      </button>
    </header>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/AppHeader.test.jsx`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/AppHeader.jsx chat-frontend/src/components/AppHeader.test.jsx
git commit -m "feat(chat-frontend): add AppHeader component"
```

### Task 2.2: Extract `Sidebar.jsx`

`Sidebar` renders the `RoomList` plus a `+ Create Room` button that opens `CreateRoomDialog`. It receives `selectedRoomId` and `onSelectRoom` as props (lifted state stays in `ChatPage` for now, threaded through `MainApp` in Task 2.4).

**Files:**
- Create: `chat-frontend/src/components/Sidebar.jsx`
- Create: `chat-frontend/src/components/Sidebar.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/Sidebar.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import Sidebar from './Sidebar'

vi.mock('./RoomList', () => ({
  default: ({ selectedRoomId, onSelectRoom }) => (
    <button type="button" onClick={() => onSelectRoom({ id: 'r1' })}>
      RoomList:{selectedRoomId ?? 'none'}
    </button>
  ),
}))
vi.mock('./CreateRoomDialog', () => ({
  default: ({ onClose, onCreated }) => (
    <div role="dialog">
      <button type="button" onClick={onClose}>close-create</button>
      <button type="button" onClick={() => onCreated({ id: 'new-room' })}>did-create</button>
    </div>
  ),
}))

describe('Sidebar', () => {
  it('renders the room list with selectedRoomId and forwards onSelectRoom', () => {
    const onSelectRoom = vi.fn()
    render(<Sidebar selectedRoomId="r-current" onSelectRoom={onSelectRoom} />)
    fireEvent.click(screen.getByText('RoomList:r-current'))
    expect(onSelectRoom).toHaveBeenCalledWith({ id: 'r1' })
  })

  it('opens CreateRoomDialog when "+ Create Room" is clicked, closes via dialog', () => {
    render(<Sidebar selectedRoomId={null} onSelectRoom={() => {}} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /create room/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(screen.getByText('close-create'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('after CreateRoomDialog reports onCreated, dispatches onSelectRoom with the new room', () => {
    const onSelectRoom = vi.fn()
    render(<Sidebar selectedRoomId={null} onSelectRoom={onSelectRoom} />)
    fireEvent.click(screen.getByRole('button', { name: /create room/i }))
    fireEvent.click(screen.getByText('did-create'))
    expect(onSelectRoom).toHaveBeenCalledWith({ id: 'new-room' })
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/Sidebar.test.jsx`
Expected: FAIL — `Cannot find module './Sidebar'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/Sidebar.jsx`:

```jsx
import { useState } from 'react'
import RoomList from './RoomList'
import CreateRoomDialog from './CreateRoomDialog'

export default function Sidebar({ selectedRoomId, onSelectRoom }) {
  const [showCreateRoom, setShowCreateRoom] = useState(false)

  const handleCreated = (room) => {
    setShowCreateRoom(false)
    onSelectRoom(room)
  }

  return (
    <aside className="app-sidebar">
      <RoomList selectedRoomId={selectedRoomId} onSelectRoom={onSelectRoom} />
      <button
        type="button"
        className="create-room-btn"
        onClick={() => setShowCreateRoom(true)}
      >
        + Create Room
      </button>
      {showCreateRoom && (
        <CreateRoomDialog
          onClose={() => setShowCreateRoom(false)}
          onCreated={handleCreated}
        />
      )}
    </aside>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/Sidebar.test.jsx`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/Sidebar.jsx chat-frontend/src/components/Sidebar.test.jsx
git commit -m "feat(chat-frontend): add Sidebar component"
```

### Task 2.3: Create `MainApp.jsx` shell

`MainApp` is the connected-user shell that the current `App.jsx` will mount instead of `ChatPage`. It holds two pieces of lifted state: `selectedRoom` and `searchQuery` (since they're shared between AppHeader's search, Sidebar's room selection, and the body). It renders `AppHeader` on top and a flex row `[Sidebar | (SearchResultsPane OR ChatPage)]` below. (ThreadRightBar comes in Chapter 7.)

**Files:**
- Create: `chat-frontend/src/components/MainApp.jsx`
- Create: `chat-frontend/src/components/MainApp.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/MainApp.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import MainApp from './MainApp'

vi.mock('./AppHeader', () => ({
  default: ({ onSelectRoom, onEnterSearch }) => (
    <header>
      <button type="button" onClick={() => onSelectRoom({ id: 'r-from-search', name: 'r' })}>
        header-pick
      </button>
      <button type="button" onClick={() => onEnterSearch('hello')}>
        header-enter-search
      </button>
    </header>
  ),
}))
vi.mock('./Sidebar', () => ({
  default: ({ selectedRoomId, onSelectRoom }) => (
    <aside>
      <span>side:{selectedRoomId ?? 'none'}</span>
      <button type="button" onClick={() => onSelectRoom({ id: 'r-from-side', name: 's' })}>
        side-pick
      </button>
    </aside>
  ),
}))
vi.mock('../pages/ChatPage', () => ({
  default: ({ selectedRoom }) => <section>page:{selectedRoom?.id ?? 'none'}</section>,
}))
vi.mock('../pages/SearchResultsPane', () => ({
  default: ({ query, onClose }) => (
    <section>
      results:{query}
      <button type="button" onClick={onClose}>close-results</button>
    </section>
  ),
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: () => ({ summaries: [], setActiveRoom: vi.fn(), jumpToMessage: vi.fn() }),
}))

describe('MainApp', () => {
  it('starts with no room and renders ChatPage with null room', () => {
    render(<MainApp />)
    expect(screen.getByText('page:none')).toBeInTheDocument()
    expect(screen.getByText('side:none')).toBeInTheDocument()
  })

  it('selecting from the Sidebar updates the selected room everywhere', () => {
    render(<MainApp />)
    fireEvent.click(screen.getByText('side-pick'))
    expect(screen.getByText('side:r-from-side')).toBeInTheDocument()
    expect(screen.getByText('page:r-from-side')).toBeInTheDocument()
  })

  it('entering a search query swaps ChatPage for SearchResultsPane', () => {
    render(<MainApp />)
    fireEvent.click(screen.getByText('header-enter-search'))
    expect(screen.getByText('results:hello')).toBeInTheDocument()
    expect(screen.queryByText(/^page:/)).not.toBeInTheDocument()
  })

  it('closing search results restores ChatPage', () => {
    render(<MainApp />)
    fireEvent.click(screen.getByText('header-enter-search'))
    fireEvent.click(screen.getByText('close-results'))
    expect(screen.getByText('page:none')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/MainApp.test.jsx`
Expected: FAIL — `Cannot find module './MainApp'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/MainApp.jsx`:

```jsx
import { useCallback, useEffect, useState } from 'react'
import { useRoomSummaries } from '../context/RoomEventsContext'
import AppHeader from './AppHeader'
import Sidebar from './Sidebar'
import ChatPage from '../pages/ChatPage'
import SearchResultsPane from '../pages/SearchResultsPane'

export default function MainApp() {
  const { summaries, setActiveRoom, jumpToMessage } = useRoomSummaries()
  const [selectedRoom, setSelectedRoom] = useState(null)
  const [searchQuery, setSearchQuery] = useState(null)

  // Clear selection if the room disappears from summaries (left / kicked).
  useEffect(() => {
    if (selectedRoom && !summaries.some((r) => r.id === selectedRoom.id)) {
      setSelectedRoom(null)
      setActiveRoom(null)
    }
  }, [summaries, selectedRoom, setActiveRoom])

  const handleSelectRoom = useCallback(
    (room) => {
      setSelectedRoom(room)
      setActiveRoom(room?.id ?? null)
      setSearchQuery(null)
    },
    [setActiveRoom]
  )

  const handleEnterSearch = useCallback((q) => setSearchQuery(q), [])

  const handleJumpToMessage = useCallback(
    (roomId, messageId) => {
      const room = summaries.find((r) => r.id === roomId)
      if (room) {
        setSelectedRoom(room)
        setActiveRoom(room.id)
      }
      setSearchQuery(null)
      if (jumpToMessage) jumpToMessage(roomId, messageId)?.catch?.(() => {})
    },
    [summaries, setActiveRoom, jumpToMessage]
  )

  return (
    <div className="app-shell">
      <AppHeader onSelectRoom={handleSelectRoom} onEnterSearch={handleEnterSearch} />
      <div className="app-row">
        <Sidebar selectedRoomId={selectedRoom?.id ?? null} onSelectRoom={handleSelectRoom} />
        {searchQuery ? (
          <SearchResultsPane
            query={searchQuery}
            onClose={() => setSearchQuery(null)}
            onSelectRoom={handleSelectRoom}
            onJumpToMessage={handleJumpToMessage}
          />
        ) : (
          <ChatPage selectedRoom={selectedRoom} onSelectRoom={handleSelectRoom} />
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/MainApp.test.jsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/MainApp.jsx chat-frontend/src/components/MainApp.test.jsx
git commit -m "feat(chat-frontend): add MainApp shell"
```

### Task 2.4: Wire `MainApp` into `App.jsx`

Replace the current `<ChatPage />` mount inside `RoomEventsProvider` with `<MainApp />`. `App.jsx` test (if any) still passes — the provider tree is unchanged.

**Files:**
- Modify: `chat-frontend/src/App.jsx`

- [ ] **Step 1: Apply the change**

In `chat-frontend/src/App.jsx`, replace the import and the JSX:

```jsx
import { useEffect, useState, useCallback } from 'react'
import { NatsProvider, useNats } from './context/NatsContext'
import { RoomEventsProvider } from './context/RoomEventsContext'
import LoginPage from './pages/LoginPage'
import MainApp from './components/MainApp'
import OidcCallback from './pages/OidcCallback'
```

And later:

```jsx
  return (
    <RoomEventsProvider>
      <MainApp />
    </RoomEventsProvider>
  )
```

(Delete the now-unused `import ChatPage from './pages/ChatPage'`.)

- [ ] **Step 2: Run the full chat-frontend suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: existing tests still pass; `ChatPage.test.jsx` likely fails because the page now receives props (`selectedRoom`, `onSelectRoom`) — leave that for Task 2.6 to fix.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/App.jsx
git commit -m "refactor(chat-frontend): mount MainApp shell from App"
```

### Task 2.5: Slim `ChatPage.jsx` — receive props, render room-header strip

`ChatPage` no longer owns: selectedRoom state, AppHeader content, Sidebar, CreateRoomDialog, SearchResultsPane. It DOES own: the room-header strip (room name + Members + Leave), the message area + input area, the in-room search panel, the ManageMembersDialog mount.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`

- [ ] **Step 1: Replace the whole file**

Replace the contents of `chat-frontend/src/pages/ChatPage.jsx` with:

```jsx
import { useEffect, useState } from 'react'
import { useRoomSummaries } from '../context/RoomEventsContext'
import MessageArea from '../components/MessageArea'
import MessageInput from '../components/MessageInput'
import ManageMembersDialog from '../components/ManageMembersDialog'
import LeaveRoomButton from '../components/LeaveRoomButton'
import InRoomSearch from '../components/InRoomSearch'
import { roomPrefix } from '../lib/roomFormat'

export default function ChatPage({ selectedRoom, onSelectRoom }) {
  const { jumpToMessage } = useRoomSummaries()
  const [showMembers, setShowMembers] = useState(false)
  const [inRoomSearchOpen, setInRoomSearchOpen] = useState(false)

  // When the selected room changes, close room-scoped overlays.
  useEffect(() => {
    setShowMembers(false)
    setInRoomSearchOpen(false)
  }, [selectedRoom?.id])

  // Ctrl/Cmd-F opens the in-room side panel; Esc closes it.
  useEffect(() => {
    if (!selectedRoom) return
    const handler = (e) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.key === 'F')) {
        e.preventDefault()
        setInRoomSearchOpen(true)
      } else if (e.key === 'Escape') {
        setInRoomSearchOpen(false)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [selectedRoom])

  const handleInRoomJump = (msgId) => {
    if (selectedRoom && jumpToMessage) {
      jumpToMessage(selectedRoom.id, msgId)?.catch?.(() => {})
    }
  }

  const isChannel = selectedRoom?.type === 'channel'

  return (
    <main className="chat-page">
      {selectedRoom && (
        <header className="chat-room-header">
          <span className="chat-room-name">
            {roomPrefix(selectedRoom.type)}{selectedRoom.name}
          </span>
          <span className="chat-room-members-count">{selectedRoom.userCount} members</span>
          <div className="chat-room-header-spacer" />
          {isChannel && (
            <>
              <button
                type="button"
                className="chat-room-members-btn"
                onClick={() => setShowMembers(true)}
              >
                Members
              </button>
              <LeaveRoomButton room={selectedRoom} />
            </>
          )}
        </header>
      )}
      <div className="chat-page-body">
        <div className="chat-main-content">
          <MessageArea room={selectedRoom} />
          <MessageInput room={selectedRoom} />
        </div>
        {inRoomSearchOpen && selectedRoom && (
          <InRoomSearch
            roomId={selectedRoom.id}
            onClose={() => setInRoomSearchOpen(false)}
            onJumpToMessage={handleInRoomJump}
          />
        )}
      </div>
      {showMembers && selectedRoom && (
        <ManageMembersDialog
          room={selectedRoom}
          onClose={() => setShowMembers(false)}
        />
      )}
    </main>
  )
}
```

`onSelectRoom` is accepted as a prop for future use (deselect on leave) but isn't called from inside yet — `LeaveRoomButton` already triggers room removal via the summary update path.

- [ ] **Step 2: Run the chat-frontend suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: `ChatPage.test.jsx` likely still fails — the existing test wraps `ChatPage` directly, not `MainApp`. Update it in Task 2.6.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx
git commit -m "refactor(chat-frontend): slim ChatPage to middle column + room-header"
```

### Task 2.6: Update `ChatPage.test.jsx` for the slimmed shape

`ChatPage.test.jsx` currently asserts on the global header (search, theme, logout) which has moved to `AppHeader`. Rewrite it to test only what `ChatPage` now owns: the room-header strip, the in-room search shortcut, the members dialog, and the message area / input mounts.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.test.jsx`

- [ ] **Step 1: Read the existing test**

Run: `cd chat-frontend && cat src/pages/ChatPage.test.jsx | head -30`
Note the existing mocks and assertions; reuse the mock patterns where they still apply (MessageArea, MessageInput).

- [ ] **Step 2: Replace the file**

Replace `chat-frontend/src/pages/ChatPage.test.jsx` with:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import ChatPage from './ChatPage'

vi.mock('../components/MessageArea', () => ({
  default: ({ room }) => <div>area:{room?.id ?? 'none'}</div>,
}))
vi.mock('../components/MessageInput', () => ({
  default: ({ room }) => <div>input:{room?.id ?? 'none'}</div>,
}))
vi.mock('../components/InRoomSearch', () => ({
  default: ({ onClose }) => (
    <aside role="complementary">
      in-room-search
      <button type="button" onClick={onClose}>close-inroom</button>
    </aside>
  ),
}))
vi.mock('../components/ManageMembersDialog', () => ({
  default: ({ onClose }) => (
    <div role="dialog">
      members-dialog
      <button type="button" onClick={onClose}>close-members</button>
    </div>
  ),
}))
vi.mock('../components/LeaveRoomButton', () => ({
  default: ({ room }) => <button type="button">Leave {room?.name}</button>,
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: () => ({ jumpToMessage: vi.fn() }),
}))

const channel = { id: 'r1', name: 'general', type: 'channel', userCount: 7 }
const dm = { id: 'r2', name: 'alice & bob', type: 'dm', userCount: 2 }

describe('ChatPage (middle column)', () => {
  it('renders MessageArea and MessageInput with the selected room', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    expect(screen.getByText('area:r1')).toBeInTheDocument()
    expect(screen.getByText('input:r1')).toBeInTheDocument()
  })

  it('renders room-header with room name, member count, Members and Leave for channels', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    expect(screen.getByText(/general/)).toBeInTheDocument()
    expect(screen.getByText(/7 members/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^members$/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /leave/i })).toBeInTheDocument()
  })

  it('hides Members and Leave for DMs', () => {
    render(<ChatPage selectedRoom={dm} onSelectRoom={() => {}} />)
    expect(screen.queryByRole('button', { name: /^members$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /leave/i })).not.toBeInTheDocument()
  })

  it('clicking Members opens the dialog', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /^members$/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('Ctrl-F opens InRoomSearch; Esc closes it', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.keyDown(window, { key: 'f', ctrlKey: true })
    expect(screen.getByText('in-room-search')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByText('in-room-search')).not.toBeInTheDocument()
  })

  it('renders no room-header when no room is selected', () => {
    render(<ChatPage selectedRoom={null} onSelectRoom={() => {}} />)
    expect(screen.queryByRole('button', { name: /^members$/i })).not.toBeInTheDocument()
    expect(screen.getByText('area:none')).toBeInTheDocument()
  })
})
```

- [ ] **Step 3: Run the focused test**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx`
Expected: PASS (6 tests).

- [ ] **Step 4: Run the full suite to confirm nothing else regressed**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "test(chat-frontend): rewrite ChatPage test for slimmed shape"
```

### Task 2.7: CSS — rename top-level selectors, add room-header strip

Today's CSS uses `.chat-layout`, `.chat-header`, `.chat-sidebar`, `.chat-body`, `.chat-main`. The new shell uses `.app-shell`, `.app-header`, `.app-sidebar`, `.app-row`, `.chat-page`, `.chat-room-header`, `.chat-page-body`. Inner `.chat-main-content` and message-area selectors stay (they're refactored visually in Chapter 3, structurally unchanged).

**Files:**
- Modify: `chat-frontend/src/styles/index.css`

- [ ] **Step 1: Locate the existing selectors**

Run: `grep -nE "^\.(chat-layout|chat-header|chat-sidebar|chat-body|chat-main)\b" chat-frontend/src/styles/index.css`
Note the line ranges so you can edit in place.

- [ ] **Step 2: Apply CSS replacements**

For every selector that names the *page-level* layout (the outermost layout grid), rename and adjust:

```css
/* old → new mapping (apply in place; keep colors / spacing identical) */
.chat-layout          → .app-shell
.chat-header          → .app-header
.chat-header-search   → .app-header-search
.chat-header-user     → .app-header-user
.chat-header-logout   → .app-header-logout
.chat-body            → .app-row
.chat-sidebar         → .app-sidebar
.chat-main            → .chat-page         /* the middle column */
```

**Important — also update descendant rules.** `index.css` contains rules that scope to `.chat-main` as an ancestor, e.g. lines 536 + 543:

```css
.chat-main .message-area  { /* … */ }
.chat-main .message-list  { /* … */ }
```

These MUST be renamed to `.chat-page .message-area` and `.chat-page .message-list` respectively. Run `grep -n "chat-main\|chat-layout\|chat-header\|chat-body\|chat-sidebar" chat-frontend/src/styles/index.css` after the rename and verify ZERO remaining matches that aren't the new `.chat-main-content` (which stays — it's the inner content wrapper, see CSS line 879).

Add a new `.chat-room-header` rule after `.chat-page`:

```css
.chat-room-header {
  display: flex;
  align-items: center;
  gap: var(--space-md, 12px);
  padding: var(--space-sm, 8px) var(--space-md, 12px);
  border-bottom: 1px solid var(--border-subtle, #e5e7eb);
  background: var(--bg-surface);
}
.chat-room-header .chat-room-name {
  font-weight: 600;
}
.chat-room-header .chat-room-members-count {
  color: var(--text-muted);
  font-size: 0.85em;
}
.chat-room-header .chat-room-header-spacer {
  flex: 1;
}
.chat-room-header .chat-room-members-btn {
  /* match existing chat-header-logout button styling — copy that rule's
     visual values verbatim if the codebase has no shared button token. */
}

.chat-page-body {
  display: flex;
  flex: 1;
  min-height: 0;
  /* Internal scrolling owned by .chat-main-content and the right-rail
     occupants (InRoomSearch today, ThreadRightBar in Chapter 7). */
}
```

(If the codebase already has a shared button token, prefer that over copy-pasting the button rule.)

- [ ] **Step 3: Manual smoke check**

Run: `cd chat-frontend && npm run dev`
Open the app in a browser; sign in; verify:
- Top header still shows search + user chip + theme toggle + Logout.
- Sidebar still shows rooms + Create button.
- Selecting a channel shows the new room-header strip with the channel name, member count, Members button, Leave button.
- DM rooms show no Members / Leave.

If anything looks off, fix the CSS before committing. (The visuals can iterate later — the structural layout is what matters here.)

- [ ] **Step 4: Run the full test suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/styles/index.css
git commit -m "style(chat-frontend): rename layout selectors, add chat-room-header"
```

---

## Chapter 3 — Message component refactor

Goal: split `MessageArea.jsx` and `MessageInput.jsx` into presentational + container pieces, paving the way for Chapters 4 (Edit/Delete), 5 (quote-reply), and 7 (thread panel) to plug into the same `<MessageList>` and `<MessageInputForm>`. This chapter ships **only** Thread + Reply icons on the hover menu — Edit and Delete come in Chapter 4, and quote-staging wiring comes in Chapter 5. The Reply icon and Thread icon both accept handler props but the callers don't do anything meaningful yet; Chapters 5 and 7 wire them up.

File layout produced by this chapter:

```
chat-frontend/src/components/messages/
  QuotedBlock.jsx          (+ QuotedBlock.test.jsx)
  MessageActions.jsx       (+ MessageActions.test.jsx)
  MessageRow.jsx           (+ MessageRow.test.jsx)
  MessageList.jsx          (+ MessageList.test.jsx)
  MessageInputForm.jsx     (+ MessageInputForm.test.jsx)

chat-frontend/src/components/
  RoomMessageArea.jsx      (+ RoomMessageArea.test.jsx)
  RoomMessageInput.jsx     (+ RoomMessageInput.test.jsx)
```

At the end of the chapter, `MessageArea.jsx` and `MessageInput.jsx` are deleted (their tests too) and `ChatPage.jsx` imports the new containers.

### Task 3.1: Create `QuotedBlock.jsx` (two variants in one component)

`QuotedBlock` renders the sender on row 1 and a one-line ellipsized plain-text excerpt on row 2. Two variants control the right-side affordance:
- `variant="chip"` — renders a ✕ button calling `onClear`. Used above an input.
- `variant="bubble"` — no ✕; whole block is clickable (calls `onClick` with the original message id). Used in-bubble above a reply's content.

A `deleted: true` flag on the snapshot renders `*[message deleted]*` on row 2 and disables the click.

**Files:**
- Create: `chat-frontend/src/components/messages/QuotedBlock.jsx`
- Create: `chat-frontend/src/components/messages/QuotedBlock.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/messages/QuotedBlock.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import QuotedBlock from './QuotedBlock'

const snap = { id: 'm1', senderName: 'alice', content: 'hello world from another time' }

describe('QuotedBlock — chip variant', () => {
  it('renders sender and content', () => {
    render(<QuotedBlock variant="chip" snapshot={snap} onClear={() => {}} />)
    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(screen.getByText(/hello world/)).toBeInTheDocument()
  })

  it('clicking ✕ invokes onClear', () => {
    const onClear = vi.fn()
    render(<QuotedBlock variant="chip" snapshot={snap} onClear={onClear} />)
    fireEvent.click(screen.getByRole('button', { name: /clear quoted message/i }))
    expect(onClear).toHaveBeenCalled()
  })
})

describe('QuotedBlock — bubble variant', () => {
  it('renders sender and content, click invokes onClick with snapshot.id', () => {
    const onClick = vi.fn()
    render(<QuotedBlock variant="bubble" snapshot={snap} onClick={onClick} />)
    fireEvent.click(screen.getByText(/hello world/).closest('.quoted-block'))
    expect(onClick).toHaveBeenCalledWith('m1')
  })

  it('renders no ✕ in bubble variant', () => {
    render(<QuotedBlock variant="bubble" snapshot={snap} onClick={() => {}} />)
    expect(screen.queryByRole('button', { name: /clear/i })).not.toBeInTheDocument()
  })
})

describe('QuotedBlock — deleted snapshot', () => {
  it('renders "[message deleted]" placeholder and disables click in bubble variant', () => {
    const onClick = vi.fn()
    render(
      <QuotedBlock
        variant="bubble"
        snapshot={{ id: 'm2', senderName: 'alice', content: '', deleted: true }}
        onClick={onClick}
      />
    )
    expect(screen.getByText(/message deleted/i)).toBeInTheDocument()
    fireEvent.click(screen.getByText(/message deleted/i).closest('.quoted-block'))
    expect(onClick).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/messages/QuotedBlock.test.jsx`
Expected: FAIL — `Cannot find module './QuotedBlock'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/messages/QuotedBlock.jsx`:

```jsx
function senderLabel(snapshot) {
  return snapshot.senderName || snapshot.sender?.engName || snapshot.sender?.account || 'Unknown'
}

function excerpt(snapshot) {
  if (snapshot.deleted) return '[message deleted]'
  return snapshot.content || snapshot.msg || ''
}

export default function QuotedBlock({ variant, snapshot, onClear, onClick }) {
  if (!snapshot) return null
  const deleted = !!snapshot.deleted
  const handleClick = () => {
    if (deleted || !onClick) return
    onClick(snapshot.id)
  }

  if (variant === 'chip') {
    return (
      <div className="quoted-block quoted-block-chip">
        <div className="quoted-block-body">
          <div className="quoted-block-sender">{senderLabel(snapshot)}</div>
          <div className="quoted-block-content">{excerpt(snapshot)}</div>
        </div>
        <button
          type="button"
          className="quoted-block-clear"
          aria-label="Clear quoted message"
          onClick={onClear}
        >
          ✕
        </button>
      </div>
    )
  }

  return (
    <div
      className={`quoted-block quoted-block-bubble${deleted ? ' quoted-block-deleted' : ''}`}
      onClick={handleClick}
      role={deleted ? undefined : 'button'}
      tabIndex={deleted ? -1 : 0}
      onKeyDown={(e) => {
        if (deleted) return
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          handleClick()
        }
      }}
    >
      <div className="quoted-block-sender">{senderLabel(snapshot)}</div>
      <div className="quoted-block-content">{excerpt(snapshot)}</div>
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/messages/QuotedBlock.test.jsx`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/QuotedBlock.jsx chat-frontend/src/components/messages/QuotedBlock.test.jsx
git commit -m "feat(chat-frontend): add QuotedBlock (chip + bubble variants)"
```

### Task 3.2: Create `MessageActions.jsx` (Thread + Reply only)

`MessageActions` is the hover-revealed row of action buttons sitting top-right of a `MessageRow`. In this chapter it exposes only Thread and Reply. Visibility rules (Edit/Delete added in Chapter 4):

- `Thread` — shown unless `context === 'thread-parent'` (the row is the parent inside the open thread panel).
- `Reply` — shown unless `context === 'thread-parent'`. The handler is called with the hovered message; routing is the caller's concern.

Visibility-by-CSS uses `:hover, :focus-within` of the parent row — CSS is added in Task 3.8 alongside the other refactor styling. The component itself is always rendered; the caller's CSS hides it when not hovered.

**Files:**
- Create: `chat-frontend/src/components/messages/MessageActions.jsx`
- Create: `chat-frontend/src/components/messages/MessageActions.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/messages/MessageActions.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import MessageActions from './MessageActions'

const msg = { id: 'm1', userAccount: 'alice' }

describe('MessageActions', () => {
  it('renders Thread and Reply buttons in the main feed context', () => {
    render(<MessageActions message={msg} context="main" onThread={() => {}} onReply={() => {}} />)
    expect(screen.getByRole('button', { name: /reply in thread/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /quote/i })).toBeInTheDocument()
  })

  it('omits Thread and Reply when context is thread-parent', () => {
    render(<MessageActions message={msg} context="thread-parent" onThread={() => {}} onReply={() => {}} />)
    expect(screen.queryByRole('button', { name: /reply in thread/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /quote/i })).not.toBeInTheDocument()
  })

  it('clicking Thread invokes onThread with the message', () => {
    const onThread = vi.fn()
    render(<MessageActions message={msg} context="main" onThread={onThread} onReply={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /reply in thread/i }))
    expect(onThread).toHaveBeenCalledWith(msg)
  })

  it('clicking Reply invokes onReply with the message', () => {
    const onReply = vi.fn()
    render(<MessageActions message={msg} context="main" onThread={() => {}} onReply={onReply} />)
    fireEvent.click(screen.getByRole('button', { name: /quote/i }))
    expect(onReply).toHaveBeenCalledWith(msg)
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageActions.test.jsx`
Expected: FAIL — `Cannot find module './MessageActions'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/messages/MessageActions.jsx`:

```jsx
export default function MessageActions({ message, context, onThread, onReply }) {
  const showThread = context !== 'thread-parent'
  const showReply = context !== 'thread-parent'

  return (
    <div className="message-actions" role="toolbar">
      {showThread && (
        <button
          type="button"
          className="message-action message-action-thread"
          aria-label="Reply in thread"
          onClick={() => onThread?.(message)}
        >
          💬
        </button>
      )}
      {showReply && (
        <button
          type="button"
          className="message-action message-action-reply"
          aria-label="Quote this message"
          onClick={() => onReply?.(message)}
        >
          ↩
        </button>
      )}
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageActions.test.jsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/MessageActions.jsx chat-frontend/src/components/messages/MessageActions.test.jsx
git commit -m "feat(chat-frontend): add MessageActions hover menu (Thread + Reply)"
```

### Task 3.3: Create `MessageRow.jsx`

`MessageRow` renders one message: an optional in-bubble `QuotedBlock` above the content, the sender + time + content, and the `MessageActions` menu. Today's `MessageArea` does this inline (lines 89–95 — sender, time, content); we lift it out. The row is focusable (`tabindex="0"`) so keyboard users can reveal the action menu via `:focus-within`.

Reply-count badge ("💬 N replies") and the click-to-jump on `QuotedBlock` come in later chapters — this task just renders the static shape.

**Files:**
- Create: `chat-frontend/src/components/messages/MessageRow.jsx`
- Create: `chat-frontend/src/components/messages/MessageRow.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/messages/MessageRow.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import MessageRow from './MessageRow'

const msg = {
  id: 'm1',
  content: 'hello world',
  createdAt: '2026-05-13T10:42:00Z',
  sender: { engName: 'Alice', account: 'alice' },
}

describe('MessageRow', () => {
  it('renders sender, time, and content', () => {
    render(<MessageRow message={msg} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />)
    expect(screen.getByText('Alice')).toBeInTheDocument()
    expect(screen.getByText('hello world')).toBeInTheDocument()
    expect(screen.getByText(/\d\d:\d\d/)).toBeInTheDocument()
  })

  it('renders the row with tabindex 0 and data-message-id', () => {
    const { container } = render(
      <MessageRow message={msg} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />
    )
    const row = container.querySelector('.message-row')
    expect(row).not.toBeNull()
    expect(row.getAttribute('tabindex')).toBe('0')
    expect(row.getAttribute('data-message-id')).toBe('m1')
  })

  it('renders an in-bubble QuotedBlock when message.quotedParentMessage is set', () => {
    const quoted = {
      ...msg,
      quotedParentMessage: { id: 'orig', senderName: 'bob', content: 'the original' },
    }
    render(<MessageRow message={quoted} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />)
    expect(screen.getByText('bob')).toBeInTheDocument()
    expect(screen.getByText('the original')).toBeInTheDocument()
  })

  it('clicking the in-bubble quote fires onJumpToMessage with the original id', () => {
    const onJumpToMessage = vi.fn()
    const quoted = {
      ...msg,
      quotedParentMessage: { id: 'orig', senderName: 'bob', content: 'the original' },
    }
    const { container } = render(
      <MessageRow message={quoted} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={onJumpToMessage} />
    )
    fireEvent.click(container.querySelector('.quoted-block-bubble'))
    expect(onJumpToMessage).toHaveBeenCalledWith('orig')
  })

  it('forwards Thread/Reply clicks via MessageActions', () => {
    const onThread = vi.fn()
    const onReply = vi.fn()
    render(<MessageRow message={msg} context="main" onThread={onThread} onReply={onReply} onJumpToMessage={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /reply in thread/i }))
    expect(onThread).toHaveBeenCalledWith(msg)
    fireEvent.click(screen.getByRole('button', { name: /quote/i }))
    expect(onReply).toHaveBeenCalledWith(msg)
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageRow.test.jsx`
Expected: FAIL — `Cannot find module './MessageRow'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/messages/MessageRow.jsx`:

```jsx
import MessageActions from './MessageActions'
import QuotedBlock from './QuotedBlock'

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

export default function MessageRow({
  message,
  context,
  onThread,
  onReply,
  onJumpToMessage,
}) {
  return (
    <div
      className="message-row"
      data-message-id={message.id}
      tabIndex={0}
    >
      {message.quotedParentMessage && (
        <QuotedBlock
          variant="bubble"
          snapshot={message.quotedParentMessage}
          onClick={onJumpToMessage}
        />
      )}
      <div className="message-header">
        <span className="message-sender">{senderName(message)}</span>
        <span className="message-time">{formatTime(message.createdAt)}</span>
      </div>
      <div className="message-content">{messageContent(message)}</div>
      <MessageActions
        message={message}
        context={context}
        onThread={onThread}
        onReply={onReply}
      />
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageRow.test.jsx`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/MessageRow.jsx chat-frontend/src/components/messages/MessageRow.test.jsx
git commit -m "feat(chat-frontend): add MessageRow with embedded QuotedBlock + MessageActions"
```

### Task 3.4: Create `MessageList.jsx` (pure)

`MessageList` is the scrolling list. It accepts:
- `messages` — array
- `context` — `'main' | 'thread'` (passed through to each row's `MessageActions`)
- `focusMessageId` — when set, scroll the matching row into view + add `flash-jump` class for 2 s
- `hasLoadedHistory`, `historyLoading`, `historyError` — drive the loading / error rendering
- `emptyText` — caller-provided empty-state line (e.g. "No messages yet" for main feed, "No replies yet…" for thread)
- `onThread`, `onReply`, `onJumpToMessage` — forwarded to each row
- `bottomRef` (optional) — caller can attach a ref to the bottom sentinel for scroll-to-bottom

It does NOT own `loadHistory` — the container does.

**Files:**
- Create: `chat-frontend/src/components/messages/MessageList.jsx`
- Create: `chat-frontend/src/components/messages/MessageList.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/messages/MessageList.test.jsx`:

```jsx
import { render, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import MessageList from './MessageList'

vi.mock('./MessageRow', () => ({
  default: ({ message }) => <div data-testid={`row-${message.id}`}>{message.content}</div>,
}))

const msgs = [
  { id: 'a', content: 'first', createdAt: '2026-05-13T10:00:00Z' },
  { id: 'b', content: 'second', createdAt: '2026-05-13T10:01:00Z' },
]

describe('MessageList', () => {
  it('renders one row per message', () => {
    render(<MessageList messages={msgs} hasLoadedHistory context="main" />)
    expect(screen.getByTestId('row-a')).toBeInTheDocument()
    expect(screen.getByTestId('row-b')).toBeInTheDocument()
  })

  it('renders loading placeholder when historyLoading is true', () => {
    render(<MessageList messages={[]} historyLoading context="main" />)
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  it('renders error placeholder when historyError is set', () => {
    render(<MessageList messages={[]} historyError="oops" context="main" />)
    expect(screen.getByText(/oops|couldn.t/i)).toBeInTheDocument()
  })

  it('renders emptyText when history is loaded and messages is empty', () => {
    render(
      <MessageList
        messages={[]}
        hasLoadedHistory
        context="main"
        emptyText="No messages yet"
      />
    )
    expect(screen.getByText('No messages yet')).toBeInTheDocument()
  })

  it('omits emptyText when messages is non-empty', () => {
    render(
      <MessageList messages={msgs} hasLoadedHistory context="main" emptyText="None" />
    )
    expect(screen.queryByText('None')).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageList.test.jsx`
Expected: FAIL — `Cannot find module './MessageList'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/messages/MessageList.jsx`:

```jsx
import { useEffect, useRef } from 'react'
import MessageRow from './MessageRow'

export default function MessageList({
  messages,
  hasLoadedHistory,
  historyLoading,
  historyError,
  emptyText,
  context,
  focusMessageId,
  onThread,
  onReply,
  onJumpToMessage,
  bottomRef,
  ariaLive,
}) {
  const listRef = useRef(null)
  const localBottomRef = useRef(null)
  const effectiveBottomRef = bottomRef ?? localBottomRef

  useEffect(() => {
    if (!focusMessageId || !listRef.current) return
    const el = listRef.current.querySelector(`[data-message-id="${focusMessageId}"]`)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block: 'center' })
    el.classList.add('flash-jump')
    const timer = setTimeout(() => el.classList.remove('flash-jump'), 2000)
    return () => clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusMessageId])

  const empty = hasLoadedHistory && !historyLoading && !historyError && messages.length === 0

  return (
    <div
      className="message-list"
      ref={listRef}
      {...(ariaLive ? { 'aria-live': ariaLive } : {})}
    >
      {historyLoading && <div className="message-loading">Loading messages…</div>}
      {historyError && <div className="message-error">{historyError}</div>}
      {messages.map((msg) => (
        <MessageRow
          key={msg.id}
          message={msg}
          context={context}
          onThread={onThread}
          onReply={onReply}
          onJumpToMessage={onJumpToMessage}
        />
      ))}
      {empty && emptyText && <div className="message-empty">{emptyText}</div>}
      <div ref={effectiveBottomRef} />
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageList.test.jsx`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/MessageList.jsx chat-frontend/src/components/messages/MessageList.test.jsx
git commit -m "feat(chat-frontend): add presentational MessageList"
```

### Task 3.5: Create `MessageInputForm.jsx` (pure)

`MessageInputForm` is the form element today's `MessageInput` produces, minus the publish side-effect. Props:
- `value`, `onChange`, `onSubmit` — controlled-form essentials.
- `placeholder`, `disabled` — UX.
- `quotedTarget` (optional, `{ id, senderName, content }`) and `onClearQuote` — render a `<QuotedBlock variant="chip">` above the textarea when set.

It owns no local state beyond what the caller passes in.

**Files:**
- Create: `chat-frontend/src/components/messages/MessageInputForm.jsx`
- Create: `chat-frontend/src/components/messages/MessageInputForm.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/messages/MessageInputForm.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import MessageInputForm from './MessageInputForm'

describe('MessageInputForm', () => {
  it('renders value and fires onChange on input', () => {
    const onChange = vi.fn()
    render(<MessageInputForm value="hi" onChange={onChange} onSubmit={() => {}} placeholder="Say something…" />)
    const input = screen.getByPlaceholderText('Say something…')
    expect(input).toHaveValue('hi')
    fireEvent.change(input, { target: { value: 'hi there' } })
    expect(onChange).toHaveBeenCalledWith('hi there')
  })

  it('Enter submits with trimmed value; Shift+Enter does not', () => {
    const onSubmit = vi.fn()
    render(<MessageInputForm value="  hello  " onChange={() => {}} onSubmit={onSubmit} placeholder="x" />)
    const input = screen.getByPlaceholderText('x')
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledTimes(1)
    onSubmit.mockClear()
    fireEvent.keyDown(input, { key: 'Enter', shiftKey: true })
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('disables the textarea and Send button when disabled prop is set', () => {
    render(<MessageInputForm value="" onChange={() => {}} onSubmit={() => {}} placeholder="x" disabled />)
    expect(screen.getByPlaceholderText('x')).toBeDisabled()
    expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
  })

  it('Send button is disabled when value is empty / whitespace', () => {
    render(<MessageInputForm value="   " onChange={() => {}} onSubmit={() => {}} placeholder="x" />)
    expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
  })

  it('renders a QuotedBlock chip when quotedTarget is set; ✕ calls onClearQuote', () => {
    const onClearQuote = vi.fn()
    render(
      <MessageInputForm
        value=""
        onChange={() => {}}
        onSubmit={() => {}}
        placeholder="x"
        quotedTarget={{ id: 'q', senderName: 'bob', content: 'orig' }}
        onClearQuote={onClearQuote}
      />
    )
    expect(screen.getByText('bob')).toBeInTheDocument()
    expect(screen.getByText('orig')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /clear quoted message/i }))
    expect(onClearQuote).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageInputForm.test.jsx`
Expected: FAIL — `Cannot find module './MessageInputForm'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/messages/MessageInputForm.jsx`:

```jsx
import QuotedBlock from './QuotedBlock'

export default function MessageInputForm({
  value,
  onChange,
  onSubmit,
  placeholder,
  disabled,
  quotedTarget,
  onClearQuote,
}) {
  const handleSubmit = (e) => {
    e?.preventDefault?.()
    if (disabled) return
    if (!value || !value.trim()) return
    onSubmit()
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSubmit(e)
    }
  }

  const canSubmit = !disabled && value && value.trim().length > 0

  return (
    <form className="message-input-form" onSubmit={handleSubmit}>
      {quotedTarget && (
        <QuotedBlock variant="chip" snapshot={quotedTarget} onClear={onClearQuote} />
      )}
      <div className="message-input-row">
        <input
          type="text"
          value={value ?? ''}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={placeholder}
          disabled={disabled}
        />
        <button type="submit" disabled={!canSubmit}>
          Send
        </button>
      </div>
    </form>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageInputForm.test.jsx`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/MessageInputForm.jsx chat-frontend/src/components/messages/MessageInputForm.test.jsx
git commit -m "feat(chat-frontend): add presentational MessageInputForm"
```

### Task 3.6: Create `RoomMessageArea.jsx` container

`RoomMessageArea` replaces today's `MessageArea` body. It uses `useRoomEvents(room?.id)` to pull state, drives `loadHistory()` on mount/room-change, owns the live-tail auto-scroll, and renders `<MessageList>`. It surfaces the existing "Jump to latest" pill when in HISTORICAL buffer mode.

Note: the room-header strip that today's `MessageArea` rendered (lines 80–85) has moved to `ChatPage` in Task 2.5 — the container does NOT render a header.

**Files:**
- Create: `chat-frontend/src/components/RoomMessageArea.jsx`
- Create: `chat-frontend/src/components/RoomMessageArea.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/RoomMessageArea.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import RoomMessageArea from './RoomMessageArea'
import { BUFFER_MODE } from '../lib/roomEventsReducer'

const loadHistory = vi.fn(async () => {})
const resetToLiveTail = vi.fn()
const jumpToMessage = vi.fn()

vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: (roomId) => ({
    messages: roomId === 'r1' ? [{ id: 'a', content: 'hi', createdAt: '2026-05-13T10:00:00Z' }] : [],
    hasLoadedHistory: roomId === 'r1',
    historyError: null,
    loadHistory,
    bufferMode: BUFFER_MODE.LIVE,
    pendingCount: 0,
    focusMessageId: null,
    resetToLiveTail,
    jumpToMessage,
  }),
}))
vi.mock('./messages/MessageList', () => ({
  default: ({ messages, emptyText, onThread, onReply, onJumpToMessage }) => (
    <div data-testid="list">
      <span>count:{messages.length}</span>
      <button type="button" onClick={() => onThread?.({ id: 'a' })}>fire-thread</button>
      <button type="button" onClick={() => onReply?.({ id: 'a' })}>fire-reply</button>
      <button type="button" onClick={() => onJumpToMessage?.('a')}>fire-jump</button>
      <span>empty:{emptyText}</span>
    </div>
  ),
}))

const room = { id: 'r1', name: 'general', type: 'channel', siteId: 's', userCount: 1 }

describe('RoomMessageArea', () => {
  beforeEach(() => {
    loadHistory.mockClear()
    resetToLiveTail.mockClear()
    jumpToMessage.mockClear()
  })

  it('calls loadHistory once the room is set', () => {
    render(<RoomMessageArea room={room} onThread={() => {}} onReply={() => {}} />)
    expect(loadHistory).toHaveBeenCalled()
  })

  it('renders a "select a room" placeholder when room is null', () => {
    render(<RoomMessageArea room={null} onThread={() => {}} onReply={() => {}} />)
    expect(screen.getByText(/select a room/i)).toBeInTheDocument()
  })

  it('forwards onThread / onReply to MessageList', () => {
    const onThread = vi.fn()
    const onReply = vi.fn()
    render(<RoomMessageArea room={room} onThread={onThread} onReply={onReply} />)
    fireEvent.click(screen.getByText('fire-thread'))
    expect(onThread).toHaveBeenCalledWith({ id: 'a' })
    fireEvent.click(screen.getByText('fire-reply'))
    expect(onReply).toHaveBeenCalledWith({ id: 'a' })
  })

  it('routes onJumpToMessage to jumpToMessage(room.id, msgId)', () => {
    render(<RoomMessageArea room={room} onThread={() => {}} onReply={() => {}} />)
    fireEvent.click(screen.getByText('fire-jump'))
    expect(jumpToMessage).toHaveBeenCalledWith('r1', 'a')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/RoomMessageArea.test.jsx`
Expected: FAIL — `Cannot find module './RoomMessageArea'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/RoomMessageArea.jsx`:

```jsx
import { useEffect, useRef } from 'react'
import { useRoomEvents } from '../context/RoomEventsContext'
import { BUFFER_MODE } from '../lib/roomEventsReducer'
import MessageList from './messages/MessageList'

export default function RoomMessageArea({ room, onThread, onReply }) {
  const {
    messages,
    hasLoadedHistory,
    historyError,
    loadHistory,
    bufferMode,
    pendingCount,
    focusMessageId,
    resetToLiveTail,
    jumpToMessage,
  } = useRoomEvents(room?.id ?? null)
  const bottomRef = useRef(null)

  useEffect(() => {
    if (!room) return
    loadHistory().catch(() => {})
  }, [room, loadHistory])

  useEffect(() => {
    if (bufferMode === BUFFER_MODE.HISTORICAL) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, bufferMode])

  useEffect(() => {
    if (bufferMode === BUFFER_MODE.LIVE) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [bufferMode])

  if (!room) {
    return (
      <div className="message-area">
        <div className="message-area-empty">Select a room to start chatting</div>
      </div>
    )
  }

  return (
    <div className="message-area">
      <MessageList
        messages={messages}
        hasLoadedHistory={hasLoadedHistory}
        historyError={historyError}
        context="main"
        focusMessageId={focusMessageId}
        onThread={onThread}
        onReply={onReply}
        onJumpToMessage={(msgId) => jumpToMessage?.(room.id, msgId)?.catch?.(() => {})}
        bottomRef={bottomRef}
      />
      {bufferMode === BUFFER_MODE.HISTORICAL && pendingCount > 0 && (
        <div className="jump-latest-pill">
          <button type="button" onClick={() => resetToLiveTail()}>
            Jump to latest ({pendingCount} new)
          </button>
        </div>
      )}
    </div>
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/RoomMessageArea.test.jsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/RoomMessageArea.jsx chat-frontend/src/components/RoomMessageArea.test.jsx
git commit -m "feat(chat-frontend): add RoomMessageArea container"
```

### Task 3.7: Create `RoomMessageInput.jsx` container

`RoomMessageInput` replaces today's `MessageInput`. It owns the local text state and (in Chapter 5) the `quotedTarget` state. For Chapter 3 it accepts a `quotedTarget` + `onClearQuote` from props so Chapter 5 can lift the state up to `ChatPage` later without restructuring the component.

**Files:**
- Create: `chat-frontend/src/components/RoomMessageInput.jsx`
- Create: `chat-frontend/src/components/RoomMessageInput.test.jsx`

- [ ] **Step 1: Write the failing test**

Create `chat-frontend/src/components/RoomMessageInput.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import RoomMessageInput from './RoomMessageInput'

const publish = vi.fn()
vi.mock('../context/NatsContext', () => ({
  useNats: () => ({ user: { account: 'alice', siteId: 's1' }, publish }),
}))
vi.mock('../lib/idgen', () => ({ generateMessageID: () => '12345678901234567890' }))
vi.mock('uuid', () => ({ v4: () => 'req-uuid' }))

const room = { id: 'r1', name: 'general', type: 'channel' }

describe('RoomMessageInput', () => {
  beforeEach(() => publish.mockClear())

  it('renders the form disabled when no room is selected', () => {
    render(<RoomMessageInput room={null} />)
    expect(screen.getByPlaceholderText(/select a room/i)).toBeDisabled()
  })

  it('publishes msg.send on submit with the correct payload', () => {
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(publish).toHaveBeenCalledWith(
      'chat.user.alice.room.r1.s1.msg.send',
      { id: '12345678901234567890', content: 'hello', requestId: 'req-uuid' }
    )
  })

  it('clears the text after publish', () => {
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(input).toHaveValue('')
  })

  it('does not publish when text is empty or whitespace', () => {
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: '   ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(publish).not.toHaveBeenCalled()
  })

  it('forwards quotedTarget and onClearQuote to MessageInputForm', () => {
    const onClearQuote = vi.fn()
    render(
      <RoomMessageInput
        room={room}
        quotedTarget={{ id: 'q', senderName: 'bob', content: 'orig' }}
        onClearQuote={onClearQuote}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /clear quoted message/i }))
    expect(onClearQuote).toHaveBeenCalled()
  })

  it('includes quotedParentMessageId in the publish payload when quotedTarget is set', () => {
    render(
      <RoomMessageInput
        room={room}
        quotedTarget={{ id: 'q123', senderName: 'bob', content: 'orig' }}
        onClearQuote={() => {}}
      />
    )
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'reply' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(publish).toHaveBeenCalledWith(
      'chat.user.alice.room.r1.s1.msg.send',
      { id: '12345678901234567890', content: 'reply', requestId: 'req-uuid', quotedParentMessageId: 'q123' }
    )
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd chat-frontend && npx vitest run src/components/RoomMessageInput.test.jsx`
Expected: FAIL — `Cannot find module './RoomMessageInput'`.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/RoomMessageInput.jsx`:

```jsx
import { useState } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from '../context/NatsContext'
import { msgSend } from '../lib/subjects'
import { generateMessageID } from '../lib/idgen'
import MessageInputForm from './messages/MessageInputForm'

export default function RoomMessageInput({ room, quotedTarget, onClearQuote }) {
  const { user, publish } = useNats()
  const [text, setText] = useState('')

  const placeholder = room ? `Message #${room.name}` : 'Select a room...'
  const disabled = !room || !user

  const handleSubmit = () => {
    if (disabled || !text.trim()) return
    const payload = {
      id: generateMessageID(),
      content: text.trim(),
      requestId: uuidv4(),
    }
    if (quotedTarget?.id) payload.quotedParentMessageId = quotedTarget.id
    publish(msgSend(user.account, room.id, user.siteId), payload)
    setText('')
    onClearQuote?.()
  }

  return (
    <MessageInputForm
      value={text}
      onChange={setText}
      onSubmit={handleSubmit}
      placeholder={placeholder}
      disabled={disabled}
      quotedTarget={quotedTarget}
      onClearQuote={onClearQuote}
    />
  )
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd chat-frontend && npx vitest run src/components/RoomMessageInput.test.jsx`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/RoomMessageInput.jsx chat-frontend/src/components/RoomMessageInput.test.jsx
git commit -m "feat(chat-frontend): add RoomMessageInput container"
```

### Task 3.8: Wire `ChatPage` to the new containers and delete the old components

Swap `MessageArea` → `RoomMessageArea` and `MessageInput` → `RoomMessageInput` in `ChatPage.jsx`. `ChatPage` passes through `onThread` / `onReply` callbacks (no-ops for now — Chapter 7 and Chapter 5 wire them). Delete the now-unused `MessageArea.jsx` + test and `MessageInput.jsx` (no test today).

Also add CSS for the new hover-reveal pattern.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`
- Modify: `chat-frontend/src/styles/index.css`
- Delete: `chat-frontend/src/components/MessageArea.jsx`
- Delete: `chat-frontend/src/components/MessageArea.test.jsx`
- Delete: `chat-frontend/src/components/MessageInput.jsx`

- [ ] **Step 1: Swap the imports in `ChatPage.jsx`**

Replace:

```jsx
import MessageArea from '../components/MessageArea'
import MessageInput from '../components/MessageInput'
```

with:

```jsx
import RoomMessageArea from '../components/RoomMessageArea'
import RoomMessageInput from '../components/RoomMessageInput'
```

And replace the usages:

```jsx
<RoomMessageArea
  room={selectedRoom}
  onThread={() => { /* wired in Chapter 7 */ }}
  onReply={() => { /* wired in Chapter 5 */ }}
/>
<RoomMessageInput room={selectedRoom} />
```

- [ ] **Step 2: Add CSS for hover-reveal MessageActions and the message row**

Append to `chat-frontend/src/styles/index.css`:

```css
.message-row {
  position: relative;
  padding: 6px 12px;
}
.message-row:hover,
.message-row:focus-within {
  background: var(--bg-hover, rgba(0,0,0,0.03));
}
.message-row .message-actions {
  position: absolute;
  top: 4px;
  right: 8px;
  display: none;
  gap: 4px;
  background: var(--bg-surface);
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  padding: 2px 4px;
}
.message-row:hover .message-actions,
.message-row:focus-within .message-actions {
  display: flex;
}
.message-action {
  padding: 2px 6px;
  background: transparent;
  border: 0;
  cursor: pointer;
  font-size: 0.95em;
}
.message-action:hover {
  background: var(--bg-hover, rgba(0,0,0,0.06));
}
.quoted-block {
  border-left: 3px solid var(--border-subtle, #ccc);
  padding: 2px 8px;
  margin: 2px 0;
  background: var(--bg-quoted, rgba(0,0,0,0.03));
}
.quoted-block-sender {
  font-size: 0.85em;
  font-weight: 600;
  color: var(--text-muted);
}
.quoted-block-content {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
}
.quoted-block-chip {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
.quoted-block-chip .quoted-block-body {
  flex: 1;
  min-width: 0;
}
.quoted-block-bubble {
  cursor: pointer;
}
.quoted-block-bubble:hover {
  background: var(--bg-hover, rgba(0,0,0,0.05));
}
.quoted-block-deleted {
  cursor: default;
  font-style: italic;
  color: var(--text-muted);
}
.message-empty {
  text-align: center;
  color: var(--text-muted);
  padding: 12px;
}
```

- [ ] **Step 3: Delete the old files**

Run:

```bash
rm chat-frontend/src/components/MessageArea.jsx
rm chat-frontend/src/components/MessageArea.test.jsx
rm chat-frontend/src/components/MessageInput.jsx
```

- [ ] **Step 4: Run the full chat-frontend suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS. If `ChatPage.test.jsx` mocks `MessageArea` / `MessageInput` by name, update those mocks to point at `RoomMessageArea` / `RoomMessageInput`.

- [ ] **Step 5: Manual smoke check**

Run: `cd chat-frontend && npm run dev`
Verify: sending a message in a channel still works; the message renders with the new hover menu (icons appear on hover); the new room-header strip and global AppHeader look right.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/styles/index.css chat-frontend/src/pages/ChatPage.test.jsx
git rm chat-frontend/src/components/MessageArea.jsx chat-frontend/src/components/MessageArea.test.jsx chat-frontend/src/components/MessageInput.jsx
git commit -m "refactor(chat-frontend): wire new message containers, drop old MessageArea/Input"
```

---

## Chapter 4 — Edit + Delete actions

Goal: surface inline Edit and Delete from the hover menu on own messages. Both RPCs already exist server-side (`history-service/internal/service/messages.go:322, 401`); this chapter wires the frontend to call them and applies optimistic UI updates. **Live propagation of edit/delete events from other users is out of scope** for this PR (no broadcast type defined in `pkg/model/event.go` for those today — see Task 4.1 for the verification step).

Scope:
- `Edit` → inline-edit mode swaps the row's content for a textarea. Enter publishes via `msg.edit`; Esc cancels.
- `Delete` → opens a confirm dialog. Confirm publishes via `msg.delete` and renders a "[message deleted]" placeholder.
- Both apply optimistically; the next `HISTORY_LOADED` is authoritative.

### Task 4.1: Red-phase backend verification

Confirm the exact request payload shape for `msg.edit` and `msg.delete` before writing client code, so the RPCs don't fail silently.

**Files:**
- Read only (no edits).

- [ ] **Step 1: Read the edit handler**

Run: `sed -n '300,360p' history-service/internal/service/messages.go`
Capture in your notes:
- The expected JSON request body (struct field names + types).
- The success response shape.
- Whether the handler is in `history-service` itself (so the subject is `chat.user.{account}.request.room.{roomId}.{siteId}.msg.edit` — matching the `msgEdit` builder from Task 1.3).

- [ ] **Step 2: Read the delete handler**

Run: `sed -n '380,440p' history-service/internal/service/messages.go`
Capture the same details for delete.

- [ ] **Step 3: Document the shapes**

Add a short note to the top of `chat-frontend/src/components/RoomMessageArea.jsx` (or a new `// payloads:` comment block near the new edit/delete handlers in Task 4.6) listing the verified field names. Example:

```js
// msg.edit request: { messageId: string, createdAt: string, content: string, requestId: string }
// msg.edit response: { ok: true } | { error: string }
// msg.delete request: { messageId: string, createdAt: string, requestId: string }
// msg.delete response: { ok: true } | { error: string }
```

If the actual shapes differ, use the actual shapes. The example above is **a guess** — confirm in Steps 1–2 before pasting.

- [ ] **Step 4: No commit**

This is a documentation-only step; do not commit yet. The notes feed into Task 4.6.

### Task 4.1.5: Expose `dispatch` on `RoomEventsContext` (prerequisite)

Task 4.6 dispatches `MESSAGE_EDITED_LOCAL` / `MESSAGE_DELETED_LOCAL` via `useRoomEvents(roomId).dispatch`. Today the context doesn't return `dispatch` — the value object exposes only state-readers and action wrappers. Add it **before** Task 4.6 so the implementation can run in dev without a runtime "Cannot read properties of undefined (reading 'dispatch')".

**Files:**
- Modify: `chat-frontend/src/context/RoomEventsContext.jsx`

- [ ] **Step 1: Locate the provider's `value={…}` and the `useRoomEvents` return**

Run: `grep -n "value=\|useRoomEvents\|return useMemo\|return {" chat-frontend/src/context/RoomEventsContext.jsx | head`

- [ ] **Step 2: Add `dispatch` to both**

In the provider's `value={…}` JSX, add `dispatch` next to `state` (or wherever the action wrappers live). In `useRoomEvents(roomId)`'s memoised return, add `dispatch` (it's per-context, not per-room, but exposing it from the per-room hook keeps the call site clean for the new edit/delete handlers).

Also export a small `useRoomDispatch` hook (used by `ThreadEventsContext` in Ch.8 Task 8.3):

```js
export function useRoomDispatch() {
  const ctx = useContext(RoomEventsContext)
  if (!ctx) throw new Error('useRoomDispatch must be used inside RoomEventsProvider')
  return ctx.dispatch
}
```

- [ ] **Step 3: Run the chat-frontend suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS — the existing tests ignore extra returned fields.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/context/RoomEventsContext.jsx
git commit -m "feat(chat-frontend): expose dispatch + useRoomDispatch from RoomEventsContext"
```

### Task 4.2: Extend `roomEventsReducer` with local edit / delete actions

Add two new actions that optimistically mutate a message in place. These are dispatched only from the local user's RPC-success path — broadcast handling is deferred.

**Files:**
- Modify: `chat-frontend/src/lib/roomEventsReducer.js`
- Modify: `chat-frontend/src/lib/roomEventsReducer.test.js`

- [ ] **Step 1: Write the failing tests**

Append to `chat-frontend/src/lib/roomEventsReducer.test.js`:

```js
import { roomEventsReducer, initialState } from './roomEventsReducer'

describe('MESSAGE_EDITED_LOCAL', () => {
  it('replaces content + editedAt on the matching message in roomState[roomId].messages', () => {
    const seed = {
      ...initialState,
      roomState: {
        r1: {
          messages: [{ id: 'm1', content: 'old' }, { id: 'm2', content: 'other' }],
          hasLoadedHistory: true,
          historyError: null,
          unreadCount: 0,
          hasMention: false,
          mentionAll: false,
          lastMsgAt: null,
          lastMsgId: null,
          bufferMode: 'live',
          pendingLiveMessages: [],
          focusMessageId: null,
        },
      },
    }
    const out = roomEventsReducer(seed, {
      type: 'MESSAGE_EDITED_LOCAL',
      roomId: 'r1',
      messageId: 'm1',
      content: 'new',
      editedAt: '2026-05-13T11:00:00Z',
    })
    expect(out.roomState.r1.messages[0]).toEqual({
      id: 'm1', content: 'new', editedAt: '2026-05-13T11:00:00Z',
    })
    expect(out.roomState.r1.messages[1]).toEqual({ id: 'm2', content: 'other' })
  })

  it('is a no-op when the message id is not buffered', () => {
    const seed = {
      ...initialState,
      roomState: { r1: { messages: [{ id: 'm1', content: 'old' }] } },
    }
    const out = roomEventsReducer(seed, {
      type: 'MESSAGE_EDITED_LOCAL', roomId: 'r1', messageId: 'unknown', content: 'x', editedAt: 't',
    })
    expect(out).toBe(seed)
  })
})

describe('MESSAGE_DELETED_LOCAL', () => {
  it('flags the matching message as deleted', () => {
    const seed = {
      ...initialState,
      roomState: { r1: { messages: [{ id: 'm1', content: 'bye' }] } },
    }
    const out = roomEventsReducer(seed, {
      type: 'MESSAGE_DELETED_LOCAL', roomId: 'r1', messageId: 'm1',
    })
    expect(out.roomState.r1.messages[0]).toEqual({
      id: 'm1', content: 'bye', deleted: true,
    })
  })

  it('is a no-op when the message id is not buffered', () => {
    const seed = { ...initialState, roomState: { r1: { messages: [] } } }
    const out = roomEventsReducer(seed, {
      type: 'MESSAGE_DELETED_LOCAL', roomId: 'r1', messageId: 'm1',
    })
    expect(out).toBe(seed)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js`
Expected: FAIL — neither action is handled; the new test cases fall through to `default` and return the seed unchanged where the tests expect a change.

- [ ] **Step 3: Add the action handlers**

In `chat-frontend/src/lib/roomEventsReducer.js`, before the `default:` clause:

```js
case 'MESSAGE_EDITED_LOCAL': {
  const prev = state.roomState[action.roomId]
  if (!prev) return state
  const idx = prev.messages.findIndex((m) => m.id === action.messageId)
  if (idx < 0) return state
  const updatedMsg = { ...prev.messages[idx], content: action.content, editedAt: action.editedAt }
  const messages = [...prev.messages.slice(0, idx), updatedMsg, ...prev.messages.slice(idx + 1)]
  return {
    ...state,
    roomState: { ...state.roomState, [action.roomId]: { ...prev, messages } },
  }
}
case 'MESSAGE_DELETED_LOCAL': {
  const prev = state.roomState[action.roomId]
  if (!prev) return state
  const idx = prev.messages.findIndex((m) => m.id === action.messageId)
  if (idx < 0) return state
  const updatedMsg = { ...prev.messages[idx], deleted: true }
  const messages = [...prev.messages.slice(0, idx), updatedMsg, ...prev.messages.slice(idx + 1)]
  return {
    ...state,
    roomState: { ...state.roomState, [action.roomId]: { ...prev, messages } },
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/roomEventsReducer.js chat-frontend/src/lib/roomEventsReducer.test.js
git commit -m "feat(chat-frontend): roomEventsReducer handles MESSAGE_EDITED_LOCAL / DELETED_LOCAL"
```

### Task 4.3: Add Edit + Delete buttons to `MessageActions`

Edit and Delete are only shown on the current user's own messages. `MessageActions` receives `isOwn` (boolean) — keeps the visibility rule colocated with the rest of the action menu logic.

**Files:**
- Modify: `chat-frontend/src/components/messages/MessageActions.jsx`
- Modify: `chat-frontend/src/components/messages/MessageActions.test.jsx`

- [ ] **Step 1: Append failing tests**

Append to `chat-frontend/src/components/messages/MessageActions.test.jsx`:

```jsx
describe('MessageActions — Edit / Delete visibility', () => {
  it('renders Edit and Delete on own messages', () => {
    render(
      <MessageActions
        message={msg}
        context="main"
        isOwn
        onThread={() => {}} onReply={() => {}}
        onEdit={() => {}} onDelete={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: /edit message/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /delete message/i })).toBeInTheDocument()
  })

  it('omits Edit and Delete on other users\' messages', () => {
    render(
      <MessageActions
        message={msg}
        context="main"
        isOwn={false}
        onThread={() => {}} onReply={() => {}}
        onEdit={() => {}} onDelete={() => {}}
      />
    )
    expect(screen.queryByRole('button', { name: /edit message/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /delete message/i })).not.toBeInTheDocument()
  })

  it('clicking Edit / Delete invokes the handlers with the message', () => {
    const onEdit = vi.fn()
    const onDelete = vi.fn()
    render(
      <MessageActions
        message={msg}
        context="main"
        isOwn
        onThread={() => {}} onReply={() => {}}
        onEdit={onEdit} onDelete={onDelete}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /edit message/i }))
    expect(onEdit).toHaveBeenCalledWith(msg)
    fireEvent.click(screen.getByRole('button', { name: /delete message/i }))
    expect(onDelete).toHaveBeenCalledWith(msg)
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageActions.test.jsx`
Expected: FAIL — Edit/Delete buttons don't exist.

- [ ] **Step 3: Extend the component**

Replace `chat-frontend/src/components/messages/MessageActions.jsx` with:

```jsx
export default function MessageActions({
  message, context, isOwn,
  onThread, onReply, onEdit, onDelete,
}) {
  const showThread = context !== 'thread-parent'
  const showReply = context !== 'thread-parent'
  const showEdit = !!isOwn
  const showDelete = !!isOwn

  return (
    <div className="message-actions" role="toolbar">
      {showThread && (
        <button
          type="button"
          className="message-action message-action-thread"
          aria-label="Reply in thread"
          onClick={() => onThread?.(message)}
        >
          💬
        </button>
      )}
      {showReply && (
        <button
          type="button"
          className="message-action message-action-reply"
          aria-label="Quote this message"
          onClick={() => onReply?.(message)}
        >
          ↩
        </button>
      )}
      {showEdit && (
        <button
          type="button"
          className="message-action message-action-edit"
          aria-label="Edit message"
          onClick={() => onEdit?.(message)}
        >
          ✎
        </button>
      )}
      {showDelete && (
        <button
          type="button"
          className="message-action message-action-delete"
          aria-label="Delete message"
          onClick={() => onDelete?.(message)}
        >
          🗑
        </button>
      )}
    </div>
  )
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageActions.test.jsx`
Expected: PASS (all original + new).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/MessageActions.jsx chat-frontend/src/components/messages/MessageActions.test.jsx
git commit -m "feat(chat-frontend): add Edit / Delete to MessageActions (own only)"
```

### Task 4.4: Inline edit mode on `MessageRow`

When the parent calls `onEdit(message)`, the row enters edit mode locally: content swaps for a controlled `<input>` pre-populated with the original text. Enter calls a new `onEditSubmit(message, newContent)` prop (publish happens in `RoomMessageArea`); Esc calls `onEditCancel`.

Render rules while editing:
- Hide the hover menu on that row (CSS class `message-row-editing` disables `.message-actions` visibility).
- Disable click-to-jump on the in-bubble `QuotedBlock` (still rendered, just non-interactive — caller handles via context).
- Show small "Saving…" indicator after submit until `editingPending=false` (caller-controlled prop).

Deleted messages render a `*[message deleted]*` placeholder instead of content + actions.

**Files:**
- Modify: `chat-frontend/src/components/messages/MessageRow.jsx`
- Modify: `chat-frontend/src/components/messages/MessageRow.test.jsx`

- [ ] **Step 1: Append failing tests**

Append to `chat-frontend/src/components/messages/MessageRow.test.jsx`:

```jsx
describe('MessageRow — inline edit mode', () => {
  it('renders an input prefilled with current content when editing=true', () => {
    render(
      <MessageRow
        message={{ ...msg, content: 'original' }}
        context="main"
        editing
        onEditSubmit={() => {}}
        onEditCancel={() => {}}
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    expect(screen.getByDisplayValue('original')).toBeInTheDocument()
  })

  it('Enter calls onEditSubmit with (message, trimmed-content); Esc calls onEditCancel', () => {
    const onEditSubmit = vi.fn()
    const onEditCancel = vi.fn()
    render(
      <MessageRow
        message={{ ...msg, content: 'orig' }}
        context="main"
        editing
        onEditSubmit={onEditSubmit}
        onEditCancel={onEditCancel}
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    const input = screen.getByDisplayValue('orig')
    fireEvent.change(input, { target: { value: '  edited  ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onEditSubmit).toHaveBeenCalledWith(expect.objectContaining({ id: 'm1' }), 'edited')

    fireEvent.keyDown(input, { key: 'Escape' })
    expect(onEditCancel).toHaveBeenCalled()
  })

  it('renders "(edited)" marker when message.editedAt is set', () => {
    render(
      <MessageRow
        message={{ ...msg, editedAt: '2026-05-13T11:00:00Z' }}
        context="main"
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    expect(screen.getByText(/\(edited\)/i)).toBeInTheDocument()
  })

  it('omits "(edited)" marker when editedAt is unset', () => {
    render(
      <MessageRow
        message={msg}
        context="main"
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    expect(screen.queryByText(/\(edited\)/i)).not.toBeInTheDocument()
  })

  it('renders "[message deleted]" placeholder when message.deleted is true', () => {
    render(
      <MessageRow
        message={{ ...msg, deleted: true }}
        context="main"
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    expect(screen.getByText(/message deleted/i)).toBeInTheDocument()
    expect(screen.queryByText('hello world')).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageRow.test.jsx`
Expected: FAIL — neither edit input nor deleted placeholder exists.

- [ ] **Step 3: Extend the component**

Replace `chat-frontend/src/components/messages/MessageRow.jsx` with:

```jsx
import { useEffect, useState } from 'react'
import MessageActions from './MessageActions'
import QuotedBlock from './QuotedBlock'

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

export default function MessageRow({
  message,
  context,
  isOwn,
  editing,
  onEditSubmit,
  onEditCancel,
  onThread,
  onReply,
  onEdit,
  onDelete,
  onJumpToMessage,
}) {
  const [draft, setDraft] = useState(messageContent(message))

  useEffect(() => {
    setDraft(messageContent(message))
  }, [message, editing])

  if (message.deleted) {
    return (
      <div className="message-row message-row-deleted" data-message-id={message.id} tabIndex={0}>
        <div className="message-content message-content-deleted">[message deleted]</div>
      </div>
    )
  }

  return (
    <div
      className={`message-row${editing ? ' message-row-editing' : ''}`}
      data-message-id={message.id}
      tabIndex={0}
    >
      {message.quotedParentMessage && (
        <QuotedBlock
          variant="bubble"
          snapshot={message.quotedParentMessage}
          onClick={onJumpToMessage}
        />
      )}
      <div className="message-header">
        <span className="message-sender">{senderName(message)}</span>
        <span className="message-time">{formatTime(message.createdAt)}</span>
        {message.editedAt && <span className="message-edited"> (edited)</span>}
      </div>
      {editing ? (
        <input
          type="text"
          className="message-edit-input"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              if (draft.trim()) onEditSubmit?.(message, draft.trim())
            } else if (e.key === 'Escape') {
              e.preventDefault()
              onEditCancel?.()
            }
          }}
          autoFocus
        />
      ) : (
        <div className="message-content">{messageContent(message)}</div>
      )}
      {!editing && (
        <MessageActions
          message={message}
          context={context}
          isOwn={isOwn}
          onThread={onThread}
          onReply={onReply}
          onEdit={onEdit}
          onDelete={onDelete}
        />
      )}
    </div>
  )
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageRow.test.jsx`
Expected: PASS (all original + new).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/MessageRow.jsx chat-frontend/src/components/messages/MessageRow.test.jsx
git commit -m "feat(chat-frontend): inline edit + deleted placeholder on MessageRow"
```

### Task 4.5: Create `DeleteConfirmDialog`

Small modal: "Delete this message? This cannot be undone." with `Delete` / `Cancel`. Esc dismisses. Reuses existing dialog patterns from `CreateRoomDialog` (or `ManageMembersDialog`) for consistent CSS.

**Files:**
- Create: `chat-frontend/src/components/messages/DeleteConfirmDialog.jsx`
- Create: `chat-frontend/src/components/messages/DeleteConfirmDialog.test.jsx`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/components/messages/DeleteConfirmDialog.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import DeleteConfirmDialog from './DeleteConfirmDialog'

describe('DeleteConfirmDialog', () => {
  it('renders the confirm prompt', () => {
    render(<DeleteConfirmDialog onConfirm={() => {}} onCancel={() => {}} />)
    expect(screen.getByText(/cannot be undone/i)).toBeInTheDocument()
  })

  it('Cancel button calls onCancel', () => {
    const onCancel = vi.fn()
    render(<DeleteConfirmDialog onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onCancel).toHaveBeenCalled()
  })

  it('Delete button calls onConfirm', () => {
    const onConfirm = vi.fn()
    render(<DeleteConfirmDialog onConfirm={onConfirm} onCancel={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /^delete$/i }))
    expect(onConfirm).toHaveBeenCalled()
  })

  it('Esc dismisses (calls onCancel)', () => {
    const onCancel = vi.fn()
    render(<DeleteConfirmDialog onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/components/messages/DeleteConfirmDialog.test.jsx`
Expected: FAIL.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/messages/DeleteConfirmDialog.jsx`:

```jsx
import { useEffect } from 'react'

export default function DeleteConfirmDialog({ onConfirm, onCancel, pending }) {
  useEffect(() => {
    const handler = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onCancel?.()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onCancel])

  return (
    <div className="dialog-backdrop">
      <div className="dialog dialog-delete-confirm" role="dialog" aria-modal="true">
        <p>Delete this message? This cannot be undone.</p>
        <div className="dialog-actions">
          <button type="button" onClick={onCancel} disabled={pending}>Cancel</button>
          <button type="button" onClick={onConfirm} disabled={pending}>
            {pending ? 'Deleting…' : 'Delete'}
          </button>
        </div>
      </div>
    </div>
  )
}
```

- [ ] **Step 4: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/messages/DeleteConfirmDialog.test.jsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/messages/DeleteConfirmDialog.jsx chat-frontend/src/components/messages/DeleteConfirmDialog.test.jsx
git commit -m "feat(chat-frontend): add DeleteConfirmDialog"
```

### Task 4.6: Wire edit + delete RPCs in `RoomMessageArea`

`RoomMessageArea` now owns local state for which row is being edited and which row is awaiting delete-confirm. It publishes the RPCs and dispatches the optimistic reducer actions on success.

**Files:**
- Modify: `chat-frontend/src/components/RoomMessageArea.jsx`
- Modify: `chat-frontend/src/components/RoomMessageArea.test.jsx`

- [ ] **Step 1: Append failing tests**

Append to `chat-frontend/src/components/RoomMessageArea.test.jsx`. **First add a publish + dispatch mock at the top of the file** (replace the existing `useRoomEvents` mock with one that exposes `dispatch`):

Replace the existing `vi.mock('../context/RoomEventsContext', …)` block with:

```jsx
const dispatch = vi.fn()
vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: (roomId) => ({
    messages: roomId === 'r1' ? [
      { id: 'a', content: 'hi', createdAt: '2026-05-13T10:00:00Z', sender: { account: 'alice' } },
    ] : [],
    hasLoadedHistory: roomId === 'r1',
    historyError: null,
    loadHistory,
    bufferMode: BUFFER_MODE.LIVE,
    pendingCount: 0,
    focusMessageId: null,
    resetToLiveTail,
    jumpToMessage,
    dispatch,
  }),
}))
```

(Add `dispatch` to the `RoomEventsContext` provider return value in Task 4.7 — note this as a follow-up there.)

Also add a `publish` mock:

```jsx
const publish = vi.fn()
vi.mock('../context/NatsContext', () => ({
  useNats: () => ({ user: { account: 'alice', siteId: 's1' }, publish }),
}))
vi.mock('uuid', () => ({ v4: () => 'req-id' }))
```

Update the mocked `MessageList` to expose edit/delete callbacks:

```jsx
vi.mock('./messages/MessageList', () => ({
  default: ({ messages, onEdit, onDelete, onEditSubmit, onEditCancel, editingMessageId }) => (
    <div data-testid="list">
      <span>count:{messages.length}</span>
      <span>editing:{editingMessageId ?? 'none'}</span>
      <button type="button" onClick={() => onEdit?.({ id: 'a' })}>fire-edit</button>
      <button type="button" onClick={() => onDelete?.({ id: 'a', createdAt: '2026-05-13T10:00:00Z' })}>fire-delete</button>
      <button type="button" onClick={() => onEditSubmit?.({ id: 'a', createdAt: '2026-05-13T10:00:00Z' }, 'new text')}>fire-edit-submit</button>
      <button type="button" onClick={() => onEditCancel?.()}>fire-edit-cancel</button>
    </div>
  ),
}))
```

Now append the test cases:

```jsx
describe('RoomMessageArea — Edit', () => {
  beforeEach(() => { publish.mockClear(); dispatch.mockClear() })

  it('entering edit mode passes editingMessageId to MessageList', () => {
    render(<RoomMessageArea room={room} />)
    fireEvent.click(screen.getByText('fire-edit'))
    expect(screen.getByText('editing:a')).toBeInTheDocument()
  })

  it('cancelling edit mode resets editingMessageId', () => {
    render(<RoomMessageArea room={room} />)
    fireEvent.click(screen.getByText('fire-edit'))
    fireEvent.click(screen.getByText('fire-edit-cancel'))
    expect(screen.getByText('editing:none')).toBeInTheDocument()
  })

  it('submitting edit publishes msg.edit and dispatches MESSAGE_EDITED_LOCAL', () => {
    render(<RoomMessageArea room={room} />)
    fireEvent.click(screen.getByText('fire-edit'))
    fireEvent.click(screen.getByText('fire-edit-submit'))
    expect(publish).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.s1.msg.edit',
      { messageId: 'a', createdAt: '2026-05-13T10:00:00Z', content: 'new text', requestId: 'req-id' }
    )
    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: 'MESSAGE_EDITED_LOCAL', roomId: 'r1', messageId: 'a', content: 'new text',
    }))
    expect(screen.getByText('editing:none')).toBeInTheDocument()
  })
})

describe('RoomMessageArea — Delete', () => {
  beforeEach(() => { publish.mockClear(); dispatch.mockClear() })

  it('clicking delete opens the confirm dialog', () => {
    render(<RoomMessageArea room={room} />)
    fireEvent.click(screen.getByText('fire-delete'))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('cancelling the dialog leaves no RPC and no dispatch', () => {
    render(<RoomMessageArea room={room} />)
    fireEvent.click(screen.getByText('fire-delete'))
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(publish).not.toHaveBeenCalled()
    expect(dispatch).not.toHaveBeenCalled()
  })

  it('confirming publishes msg.delete and dispatches MESSAGE_DELETED_LOCAL', () => {
    render(<RoomMessageArea room={room} />)
    fireEvent.click(screen.getByText('fire-delete'))
    fireEvent.click(screen.getByRole('button', { name: /^delete$/i }))
    expect(publish).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.s1.msg.delete',
      { messageId: 'a', createdAt: '2026-05-13T10:00:00Z', requestId: 'req-id' }
    )
    expect(dispatch).toHaveBeenCalledWith({
      type: 'MESSAGE_DELETED_LOCAL', roomId: 'r1', messageId: 'a',
    })
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/components/RoomMessageArea.test.jsx`
Expected: FAIL — `dispatch` not exposed, edit/delete handlers don't exist.

- [ ] **Step 3: Update `RoomMessageArea.jsx`**

Replace `chat-frontend/src/components/RoomMessageArea.jsx`:

```jsx
import { useEffect, useRef, useState } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from '../context/NatsContext'
import { useRoomEvents } from '../context/RoomEventsContext'
import { BUFFER_MODE } from '../lib/roomEventsReducer'
import { msgEdit, msgDelete } from '../lib/subjects'
import MessageList from './messages/MessageList'
import DeleteConfirmDialog from './messages/DeleteConfirmDialog'

export default function RoomMessageArea({ room, onThread, onReply }) {
  const { user, publish } = useNats()
  const {
    messages,
    hasLoadedHistory,
    historyError,
    loadHistory,
    bufferMode,
    pendingCount,
    focusMessageId,
    resetToLiveTail,
    jumpToMessage,
    dispatch,
  } = useRoomEvents(room?.id ?? null)
  const bottomRef = useRef(null)
  const [editingMessageId, setEditingMessageId] = useState(null)
  const [pendingDelete, setPendingDelete] = useState(null)

  useEffect(() => { setEditingMessageId(null); setPendingDelete(null) }, [room?.id])

  useEffect(() => {
    if (!room) return
    loadHistory().catch(() => {})
  }, [room, loadHistory])

  useEffect(() => {
    if (bufferMode === BUFFER_MODE.HISTORICAL) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, bufferMode])

  useEffect(() => {
    if (bufferMode === BUFFER_MODE.LIVE) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [bufferMode])

  const handleEdit = (msg) => setEditingMessageId(msg.id)
  const handleEditCancel = () => setEditingMessageId(null)
  const handleEditSubmit = (msg, newContent) => {
    publish(msgEdit(user.account, room.id, user.siteId), {
      messageId: msg.id,
      createdAt: msg.createdAt,
      content: newContent,
      requestId: uuidv4(),
    })
    dispatch({
      type: 'MESSAGE_EDITED_LOCAL',
      roomId: room.id,
      messageId: msg.id,
      content: newContent,
      editedAt: new Date().toISOString(),
    })
    setEditingMessageId(null)
  }

  const handleDelete = (msg) => setPendingDelete(msg)
  const handleDeleteCancel = () => setPendingDelete(null)
  const handleDeleteConfirm = () => {
    if (!pendingDelete) return
    publish(msgDelete(user.account, room.id, user.siteId), {
      messageId: pendingDelete.id,
      createdAt: pendingDelete.createdAt,
      requestId: uuidv4(),
    })
    dispatch({
      type: 'MESSAGE_DELETED_LOCAL',
      roomId: room.id,
      messageId: pendingDelete.id,
    })
    setPendingDelete(null)
  }

  if (!room) {
    return (
      <div className="message-area">
        <div className="message-area-empty">Select a room to start chatting</div>
      </div>
    )
  }

  return (
    <div className="message-area">
      <MessageList
        messages={messages}
        hasLoadedHistory={hasLoadedHistory}
        historyError={historyError}
        context="main"
        focusMessageId={focusMessageId}
        currentUserAccount={user?.account}
        editingMessageId={editingMessageId}
        onThread={onThread}
        onReply={onReply}
        onEdit={handleEdit}
        onEditSubmit={handleEditSubmit}
        onEditCancel={handleEditCancel}
        onDelete={handleDelete}
        onJumpToMessage={(msgId) => jumpToMessage?.(room.id, msgId)?.catch?.(() => {})}
        bottomRef={bottomRef}
      />
      {bufferMode === BUFFER_MODE.HISTORICAL && pendingCount > 0 && (
        <div className="jump-latest-pill">
          <button type="button" onClick={() => resetToLiveTail()}>
            Jump to latest ({pendingCount} new)
          </button>
        </div>
      )}
      {pendingDelete && (
        <DeleteConfirmDialog onConfirm={handleDeleteConfirm} onCancel={handleDeleteCancel} />
      )}
    </div>
  )
}
```

- [ ] **Step 4: Extend `MessageList` to forward the new props**

In `chat-frontend/src/components/messages/MessageList.jsx`, update the props destructure and the row render to forward `editingMessageId`, `currentUserAccount`, `onEdit`, `onEditSubmit`, `onEditCancel`, `onDelete`. Compute `isOwn` per row:

```jsx
{messages.map((msg) => (
  <MessageRow
    key={msg.id}
    message={msg}
    context={context}
    isOwn={!!currentUserAccount && msg.sender?.account === currentUserAccount}
    editing={editingMessageId === msg.id}
    onThread={onThread}
    onReply={onReply}
    onEdit={onEdit}
    onEditSubmit={onEditSubmit}
    onEditCancel={onEditCancel}
    onDelete={onDelete}
    onJumpToMessage={onJumpToMessage}
  />
))}
```

…and add `currentUserAccount`, `editingMessageId`, `onEdit`, `onEditSubmit`, `onEditCancel`, `onDelete` to `MessageList`'s props.

Add `MessageList` test updates as appropriate (the existing `MessageList.test.jsx` mocked rows so it shouldn't break).

- [ ] **Step 5: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/RoomMessageArea.test.jsx`
Expected: PASS.

Run: `cd chat-frontend && npm test -- --run`
Expected: full suite green.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/components/RoomMessageArea.jsx chat-frontend/src/components/RoomMessageArea.test.jsx chat-frontend/src/components/messages/MessageList.jsx
git commit -m "feat(chat-frontend): wire msg.edit / msg.delete RPCs in RoomMessageArea"
```

### Task 4.7: ~~Expose `dispatch` on `RoomEventsContext`~~ — moved to Task 4.1.5

Task 4.1.5 (above) now handles this prerequisite before Task 4.6 needs it. This slot is intentionally empty; skip and continue to Chapter 5.

---

## Chapter 5 — Quote-reply staging + click-to-jump (main feed)

Goal: clicking the Reply (↩) icon in the main feed stages the hovered message as a `quotedTarget` in `RoomMessageInput`. The chip appears above the textarea; the next Send publishes with `quotedParentMessageId`; the chip clears on success. Click-to-jump on the in-bubble `QuotedBlock` is already wired (Task 3.3 / 3.6); this chapter adds an end-to-end test for it. The staged quote clears on room switch.

The thread input's quote staging is wired in Chapter 7.

Architecture decision: `quotedTarget` lives in **`ChatPage`** state. The Reply icon is rendered inside `RoomMessageArea`, the chip in `RoomMessageInput` — siblings under `ChatPage`. Lifting state up to `ChatPage` is the simplest way to share without prop-drilling through extra layers.

### Task 5.1: Lift `quotedTarget` into `ChatPage`

`ChatPage` owns a `quotedTarget` state, passes a setter callback into `RoomMessageArea` as `onReply`, and passes the value + clear callback into `RoomMessageInput`.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`
- Modify: `chat-frontend/src/pages/ChatPage.test.jsx`

- [ ] **Step 1: Add the failing integration test**

Append to `chat-frontend/src/pages/ChatPage.test.jsx`. First update the mocks to expose the new wiring:

```jsx
vi.mock('../components/RoomMessageArea', () => ({
  default: ({ onReply }) => (
    <div>
      area
      <button type="button" onClick={() => onReply?.({ id: 'm-orig', sender: { account: 'alice' }, content: 'hello there' })}>
        fire-reply
      </button>
    </div>
  ),
}))
vi.mock('../components/RoomMessageInput', () => ({
  default: ({ room, quotedTarget, onClearQuote }) => (
    <div>
      input:{room?.id ?? 'none'}
      {quotedTarget && (
        <>
          <span data-testid="staged">staged:{quotedTarget.id}</span>
          <button type="button" onClick={onClearQuote}>clear-staged</button>
        </>
      )}
    </div>
  ),
}))
```

Append tests:

```jsx
describe('ChatPage — quote-reply staging', () => {
  it('clicking Reply on a message stages the quotedTarget in the input', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    expect(screen.queryByTestId('staged')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('fire-reply'))
    expect(screen.getByText('staged:m-orig')).toBeInTheDocument()
  })

  it('clicking the chip\'s clear button clears quotedTarget', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.click(screen.getByText('fire-reply'))
    fireEvent.click(screen.getByText('clear-staged'))
    expect(screen.queryByTestId('staged')).not.toBeInTheDocument()
  })

  it('switching rooms clears quotedTarget', () => {
    const { rerender } = render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.click(screen.getByText('fire-reply'))
    expect(screen.getByText('staged:m-orig')).toBeInTheDocument()
    rerender(<ChatPage selectedRoom={dm} onSelectRoom={() => {}} />)
    expect(screen.queryByTestId('staged')).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx`
Expected: FAIL — staged element never appears (no state plumbing).

- [ ] **Step 3: Wire the state in `ChatPage.jsx`**

In `chat-frontend/src/pages/ChatPage.jsx`, add to imports if missing:

```jsx
import { useEffect, useState } from 'react'
```

Inside the component body, after the existing `useState` calls:

```jsx
const [quotedTarget, setQuotedTarget] = useState(null)
```

Add an effect to clear on room change (next to the existing room-change effect):

```jsx
useEffect(() => {
  setQuotedTarget(null)
}, [selectedRoom?.id])
```

Add a `handleReply` callback:

```jsx
const handleReply = (msg) => {
  // Build a chip-friendly snapshot. content is plain text — markdown rendering
  // is explicitly out of scope per the spec.
  setQuotedTarget({
    id: msg.id,
    senderName: msg.sender?.engName || msg.sender?.account || msg.userAccount || 'Unknown',
    content: msg.content || msg.msg || '',
  })
}
```

Replace the existing `<RoomMessageArea>` and `<RoomMessageInput>` usages with:

```jsx
<RoomMessageArea
  room={selectedRoom}
  onThread={() => { /* wired in Chapter 7 */ }}
  onReply={handleReply}
/>
<RoomMessageInput
  room={selectedRoom}
  quotedTarget={quotedTarget}
  onClearQuote={() => setQuotedTarget(null)}
/>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "feat(chat-frontend): wire quote-reply staging in main feed via ChatPage"
```

### Task 5.2: End-to-end test — Reply → publish carries `quotedParentMessageId`

This integration test exercises the *real* `RoomMessageInput` (not mocked) plus the staging chip — verifying the `quotedTarget` flows all the way through to the publish call.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.test.jsx`

- [ ] **Step 1: Add a focused integration test**

Append to `chat-frontend/src/pages/ChatPage.test.jsx`:

```jsx
import { vi as viE2E } from 'vitest'

describe('ChatPage — quote-reply E2E (real RoomMessageInput)', () => {
  it('publish call carries quotedParentMessageId after Reply staging', async () => {
    viE2E.resetModules()
    const publish = viE2E.fn()

    viE2E.doMock('../context/NatsContext', () => ({
      useNats: () => ({ user: { account: 'alice', siteId: 's1' }, publish }),
    }))
    viE2E.doMock('../lib/idgen', () => ({ generateMessageID: () => '12345678901234567890' }))
    viE2E.doMock('uuid', () => ({ v4: () => 'req-1' }))
    viE2E.doMock('../components/RoomMessageArea', () => ({
      default: ({ onReply }) => (
        <button
          type="button"
          onClick={() => onReply?.({ id: 'orig', sender: { account: 'alice' }, content: 'hello' })}
        >
          stage
        </button>
      ),
    }))
    viE2E.doMock('../components/InRoomSearch', () => ({ default: () => null }))
    viE2E.doMock('../components/ManageMembersDialog', () => ({ default: () => null }))
    viE2E.doMock('../components/LeaveRoomButton', () => ({ default: () => null }))
    viE2E.doMock('../context/RoomEventsContext', () => ({
      useRoomSummaries: () => ({ jumpToMessage: viE2E.fn() }),
    }))

    const { default: FreshChatPage } = await import('./ChatPage')
    render(<FreshChatPage selectedRoom={channel} onSelectRoom={() => {}} />)

    fireEvent.click(screen.getByText('stage'))
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'a reply' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(publish).toHaveBeenCalledWith(
      'chat.user.alice.room.r1.s1.msg.send',
      {
        id: '12345678901234567890',
        content: 'a reply',
        requestId: 'req-1',
        quotedParentMessageId: 'orig',
      }
    )
  })
})
```

- [ ] **Step 2: Run the test**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx -t "quote-reply E2E"`
Expected: PASS (asserts the wired chain end-to-end).

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "test(chat-frontend): E2E quote-reply staging → publish payload"
```

### Task 5.3: Click-to-jump verification (in-bubble `QuotedBlock`)

Click-to-jump already routes through `MessageList → MessageRow → QuotedBlock.onClick` → `RoomMessageArea.onJumpToMessage` → `jumpToMessage(roomId, messageId)` (added in Task 3.6). Add a focused integration test that exercises it through real components to lock the contract.

**Files:**
- Create: `chat-frontend/src/components/RoomMessageArea.quoted.test.jsx`

- [ ] **Step 1: Write the test**

Create `chat-frontend/src/components/RoomMessageArea.quoted.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import RoomMessageArea from './RoomMessageArea'
import { BUFFER_MODE } from '../lib/roomEventsReducer'

const jumpToMessage = vi.fn(async () => {})
vi.mock('../context/NatsContext', () => ({
  useNats: () => ({ user: { account: 'alice', siteId: 's1' }, publish: vi.fn() }),
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: () => ({
    messages: [
      {
        id: 'reply-1',
        content: 'reply text',
        createdAt: '2026-05-13T10:30:00Z',
        sender: { account: 'alice' },
        quotedParentMessage: { id: 'orig-1', senderName: 'bob', content: 'the original' },
      },
    ],
    hasLoadedHistory: true,
    historyError: null,
    loadHistory: vi.fn(async () => {}),
    bufferMode: BUFFER_MODE.LIVE,
    pendingCount: 0,
    focusMessageId: null,
    resetToLiveTail: vi.fn(),
    jumpToMessage,
    dispatch: vi.fn(),
  }),
}))

const room = { id: 'r1', name: 'general', type: 'channel', siteId: 's1', userCount: 1 }

describe('RoomMessageArea — click-to-jump', () => {
  beforeEach(() => jumpToMessage.mockClear())

  it('clicking the in-bubble QuotedBlock fires jumpToMessage(room.id, snapshot.id)', () => {
    const { container } = render(<RoomMessageArea room={room} onThread={() => {}} onReply={() => {}} />)
    const bubble = container.querySelector('.quoted-block-bubble')
    expect(bubble).not.toBeNull()
    fireEvent.click(bubble)
    expect(jumpToMessage).toHaveBeenCalledWith('r1', 'orig-1')
  })
})
```

- [ ] **Step 2: Run the test**

Run: `cd chat-frontend && npx vitest run src/components/RoomMessageArea.quoted.test.jsx`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/components/RoomMessageArea.quoted.test.jsx
git commit -m "test(chat-frontend): E2E click-to-jump from in-bubble QuotedBlock"
```

---

## Chapter 6 — Thread state plumbing

Goal: build the reducer and context that own thread state. **No UI yet** — the next chapter mounts the panel. By the end of this chapter, `useThreadEvents()` is callable anywhere under `MainApp` and accepts `openThread(parent)`, `closeThread()`, `sendReply(content, options)`, `retryReply(id)`, `dismissReply(id)`.

### Task 6.1: Create `threadEventsReducer.js`

State shape and actions per spec section "State management" (`docs/superpowers/specs/2026-05-13-thread-panel-design.md`, lines about `threadEventsReducer.js`).

**Files:**
- Create: `chat-frontend/src/lib/threadEventsReducer.js`
- Create: `chat-frontend/src/lib/threadEventsReducer.test.js`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/lib/threadEventsReducer.test.js`:

```js
import { describe, it, expect } from 'vitest'
import { threadEventsReducer, initialState } from './threadEventsReducer'

const parent = { roomId: 'r1', siteId: 's1', messageId: 'p1', createdAtMs: 1000 }

describe('threadEventsReducer — OPEN_THREAD', () => {
  it('sets activeParent and flags loading', () => {
    const out = threadEventsReducer(initialState, { type: 'OPEN_THREAD', parent })
    expect(out.activeParent).toEqual(parent)
    expect(out.historyLoading).toBe(true)
    expect(out.messages).toEqual([])
    expect(out.hasLoadedHistory).toBe(false)
    expect(out.historyError).toBe(null)
  })

  it('short-circuits when the same parent is already active', () => {
    const seed = { ...initialState, activeParent: parent, messages: [{ id: 'r1' }] }
    const out = threadEventsReducer(seed, { type: 'OPEN_THREAD', parent })
    expect(out).toBe(seed)
  })

  it('switches to a different parent and clears prior state', () => {
    const seed = { ...initialState, activeParent: parent, messages: [{ id: 'old' }], hasLoadedHistory: true }
    const next = { ...parent, messageId: 'p2' }
    const out = threadEventsReducer(seed, { type: 'OPEN_THREAD', parent: next })
    expect(out.activeParent).toEqual(next)
    expect(out.messages).toEqual([])
    expect(out.hasLoadedHistory).toBe(false)
    expect(out.historyLoading).toBe(true)
  })
})

describe('threadEventsReducer — CLOSE_THREAD', () => {
  it('resets to initialState', () => {
    const seed = { ...initialState, activeParent: parent, messages: [{ id: 'x' }] }
    expect(threadEventsReducer(seed, { type: 'CLOSE_THREAD' })).toEqual(initialState)
  })
})

describe('threadEventsReducer — HISTORY_LOADED', () => {
  const open = threadEventsReducer(initialState, { type: 'OPEN_THREAD', parent })

  it('hydrates messages from the response', () => {
    const out = threadEventsReducer(open, {
      type: 'HISTORY_LOADED',
      parentId: 'p1',
      resp: { messages: [{ id: 'r1' }, { id: 'r2' }], hasNext: false, nextCursor: null },
    })
    expect(out.messages).toEqual([{ id: 'r1' }, { id: 'r2' }])
    expect(out.hasLoadedHistory).toBe(true)
    expect(out.historyLoading).toBe(false)
    expect(out.historyError).toBe(null)
    expect(out.hasNext).toBe(false)
    expect(out.nextCursor).toBe(null)
  })

  it('ignores results for a non-active parent', () => {
    const out = threadEventsReducer(open, {
      type: 'HISTORY_LOADED',
      parentId: 'other',
      resp: { messages: [{ id: 'r1' }], hasNext: false, nextCursor: null },
    })
    expect(out).toBe(open)
  })

  it('preserves any optimistic _local rows when merging history', () => {
    const seeded = { ...open, messages: [{ id: 'opt', _local: true, content: 'mine' }] }
    const out = threadEventsReducer(seeded, {
      type: 'HISTORY_LOADED',
      parentId: 'p1',
      resp: { messages: [{ id: 'r-from-server' }], hasNext: false, nextCursor: null },
    })
    const ids = out.messages.map((m) => m.id)
    expect(ids).toContain('opt')
    expect(ids).toContain('r-from-server')
  })
})

describe('threadEventsReducer — HISTORY_FAILED', () => {
  it('sets historyError, clears historyLoading', () => {
    const open = threadEventsReducer(initialState, { type: 'OPEN_THREAD', parent })
    const out = threadEventsReducer(open, { type: 'HISTORY_FAILED', parentId: 'p1', error: 'nope' })
    expect(out.historyError).toBe('nope')
    expect(out.historyLoading).toBe(false)
  })

  it('ignores failures for a non-active parent', () => {
    const open = threadEventsReducer(initialState, { type: 'OPEN_THREAD', parent })
    const out = threadEventsReducer(open, { type: 'HISTORY_FAILED', parentId: 'other', error: 'x' })
    expect(out).toBe(open)
  })
})

describe('threadEventsReducer — REPLY_SENT_LOCAL', () => {
  it('appends an optimistic message with _local: true', () => {
    const open = threadEventsReducer(initialState, { type: 'OPEN_THREAD', parent })
    const out = threadEventsReducer(open, {
      type: 'REPLY_SENT_LOCAL',
      message: { id: 'opt', content: 'hi', _local: true },
    })
    expect(out.messages).toEqual([{ id: 'opt', content: 'hi', _local: true }])
  })

  it('dedupes by id (no double-append)', () => {
    const open = threadEventsReducer(initialState, { type: 'OPEN_THREAD', parent })
    const once = threadEventsReducer(open, { type: 'REPLY_SENT_LOCAL', message: { id: 'opt', _local: true } })
    const twice = threadEventsReducer(once, { type: 'REPLY_SENT_LOCAL', message: { id: 'opt', _local: true } })
    expect(twice.messages).toHaveLength(1)
  })
})

describe('threadEventsReducer — REPLY_SEND_FAILED / REPLY_RETRIED / REPLY_DISMISSED', () => {
  const open = threadEventsReducer(initialState, { type: 'OPEN_THREAD', parent })
  const sent = threadEventsReducer(open, {
    type: 'REPLY_SENT_LOCAL',
    message: { id: 'opt', _local: true, content: 'x' },
  })

  it('REPLY_SEND_FAILED marks _status: "failed" on the matching id', () => {
    const out = threadEventsReducer(sent, { type: 'REPLY_SEND_FAILED', messageId: 'opt', error: 'nope' })
    expect(out.messages[0]._status).toBe('failed')
  })

  it('REPLY_RETRIED clears _status on the matching id', () => {
    const failed = threadEventsReducer(sent, { type: 'REPLY_SEND_FAILED', messageId: 'opt', error: 'nope' })
    const out = threadEventsReducer(failed, { type: 'REPLY_RETRIED', messageId: 'opt' })
    expect(out.messages[0]._status).toBeUndefined()
  })

  it('REPLY_DISMISSED removes the row', () => {
    const failed = threadEventsReducer(sent, { type: 'REPLY_SEND_FAILED', messageId: 'opt', error: 'nope' })
    const out = threadEventsReducer(failed, { type: 'REPLY_DISMISSED', messageId: 'opt' })
    expect(out.messages).toEqual([])
  })
})

describe('threadEventsReducer — RESET', () => {
  it('returns to initialState', () => {
    const seed = { ...initialState, activeParent: parent, messages: [{ id: 'x' }] }
    expect(threadEventsReducer(seed, { type: 'RESET' })).toEqual(initialState)
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/lib/threadEventsReducer.test.js`
Expected: FAIL — `Cannot find module './threadEventsReducer'`.

- [ ] **Step 3: Write the reducer**

Create `chat-frontend/src/lib/threadEventsReducer.js`:

```js
import { mergeById } from './messageBuffer'

export const initialState = {
  activeParent: null,    // { roomId, siteId, messageId, createdAtMs }
  messages: [],
  hasLoadedHistory: false,
  historyLoading: false,
  historyError: null,
  nextCursor: null,
  hasNext: false,
}

function setMessage(messages, messageId, patch) {
  const idx = messages.findIndex((m) => m.id === messageId)
  if (idx < 0) return messages
  const out = [...messages]
  out[idx] = { ...out[idx], ...patch }
  return out
}

function unsetStatus(messages, messageId) {
  const idx = messages.findIndex((m) => m.id === messageId)
  if (idx < 0) return messages
  const next = { ...messages[idx] }
  delete next._status
  const out = [...messages]
  out[idx] = next
  return out
}

export function threadEventsReducer(state, action) {
  switch (action.type) {
    case 'OPEN_THREAD': {
      const p = action.parent
      if (state.activeParent && state.activeParent.messageId === p.messageId) {
        return state
      }
      return {
        ...initialState,
        activeParent: p,
        historyLoading: true,
      }
    }
    case 'CLOSE_THREAD':
      return initialState
    case 'HISTORY_LOADING': {
      if (!state.activeParent || state.activeParent.messageId !== action.parentId) return state
      return { ...state, historyLoading: true }
    }
    case 'HISTORY_LOADED': {
      if (!state.activeParent || state.activeParent.messageId !== action.parentId) return state
      const merged = mergeById(state.messages, action.resp.messages || [])
      return {
        ...state,
        messages: merged,
        hasLoadedHistory: true,
        historyLoading: false,
        historyError: null,
        hasNext: !!action.resp.hasNext,
        nextCursor: action.resp.nextCursor ?? null,
      }
    }
    case 'HISTORY_FAILED': {
      if (!state.activeParent || state.activeParent.messageId !== action.parentId) return state
      return { ...state, historyError: action.error, historyLoading: false }
    }
    case 'REPLY_SENT_LOCAL': {
      const msg = action.message
      if (state.messages.some((m) => m.id === msg.id)) return state
      return { ...state, messages: [...state.messages, msg] }
    }
    case 'REPLY_SEND_FAILED':
      return { ...state, messages: setMessage(state.messages, action.messageId, { _status: 'failed' }) }
    case 'REPLY_RETRIED':
      return { ...state, messages: unsetStatus(state.messages, action.messageId) }
    case 'REPLY_DISMISSED':
      return { ...state, messages: state.messages.filter((m) => m.id !== action.messageId) }
    case 'RESET':
      return initialState
    default:
      return state
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd chat-frontend && npx vitest run src/lib/threadEventsReducer.test.js`
Expected: PASS (all cases).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/threadEventsReducer.js chat-frontend/src/lib/threadEventsReducer.test.js
git commit -m "feat(chat-frontend): add threadEventsReducer"
```

### Task 6.2: Create `ThreadEventsContext.jsx`

Wraps the reducer, fires the `msg.thread` RPC on `openThread`, exposes `openThread / closeThread / sendReply / retryReply / dismissReply`. Race-discard pattern mirrors `RoomEventsContext`.

`sendReply(content, { quotedParentMessageId })`:
1. Generate `id = generateMessageID()` and optimistic local message.
2. Dispatch `REPLY_SENT_LOCAL { message: { ...optimistic, _local: true } }`.
3. Publish `msgSend(account, roomId, siteId)` with `threadParentMessageId`, `threadParentMessageCreatedAt`, and (optional) `quotedParentMessageId`.
4. On error, dispatch `REPLY_SEND_FAILED { messageId: id, error }`.
5. On success (publish acks), no further dispatch — the row stays as `_local` until it's seen via thread reopen.

`retryReply(messageId)`:
1. Dispatch `REPLY_RETRIED { messageId }`.
2. Re-publish using the same `id` and payload (read from current state).
3. On error, dispatch `REPLY_SEND_FAILED` again.

`dismissReply(messageId)`: dispatch `REPLY_DISMISSED`.

Also: on `user` going null (logout), dispatch `RESET`.

**Files:**
- Create: `chat-frontend/src/context/ThreadEventsContext.jsx`
- Create: `chat-frontend/src/context/ThreadEventsContext.test.jsx`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/context/ThreadEventsContext.test.jsx`:

```jsx
import { render, screen, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ThreadEventsProvider, useThreadEvents } from './ThreadEventsContext'

const request = vi.fn()
const publish = vi.fn()
vi.mock('./NatsContext', () => ({
  useNats: () => ({
    user: { account: 'alice', siteId: 's1' },
    request, publish,
  }),
}))
vi.mock('../lib/idgen', () => ({ generateMessageID: () => 'OPT-000000000000000000' }))
vi.mock('uuid', () => ({ v4: () => 'req-uuid' }))

function Probe() {
  const t = useThreadEvents()
  return (
    <div>
      <span>active:{t.activeParent?.messageId ?? 'none'}</span>
      <span>count:{t.messages.length}</span>
      <span>loaded:{String(t.hasLoadedHistory)}</span>
      <span>loading:{String(t.historyLoading)}</span>
      <span>error:{t.historyError ?? 'none'}</span>
      <button type="button" onClick={() => t.openThread({ roomId: 'r1', siteId: 's1', messageId: 'p1', createdAtMs: 1000 })}>open</button>
      <button type="button" onClick={() => t.closeThread()}>close</button>
      <button type="button" onClick={() => t.sendReply('hi', {})}>send</button>
      <button type="button" onClick={() => t.sendReply('q-hi', { quotedParentMessageId: 'q-id' })}>send-quote</button>
      <button type="button" onClick={() => t.retryReply('OPT-000000000000000000')}>retry</button>
      <button type="button" onClick={() => t.dismissReply('OPT-000000000000000000')}>dismiss</button>
    </div>
  )
}

const setup = () =>
  render(<ThreadEventsProvider><Probe /></ThreadEventsProvider>)

describe('ThreadEventsContext', () => {
  beforeEach(() => { request.mockReset(); publish.mockReset() })

  it('openThread sets activeParent and fires msg.thread RPC; on success dispatches HISTORY_LOADED', async () => {
    request.mockResolvedValueOnce({ messages: [{ id: 'r1' }, { id: 'r2' }], hasNext: false, nextCursor: null })
    setup()
    expect(screen.getByText('active:none')).toBeInTheDocument()
    await act(async () => {
      screen.getByText('open').click()
    })
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.s1.msg.thread',
      { threadMessageId: 'p1', limit: 50 }
    )
    expect(screen.getByText('active:p1')).toBeInTheDocument()
    expect(screen.getByText('count:2')).toBeInTheDocument()
    expect(screen.getByText('loaded:true')).toBeInTheDocument()
    expect(screen.getByText('loading:false')).toBeInTheDocument()
  })

  it('openThread RPC failure dispatches HISTORY_FAILED', async () => {
    request.mockRejectedValueOnce(new Error('boom'))
    setup()
    await act(async () => { screen.getByText('open').click() })
    expect(screen.getByText('error:boom')).toBeInTheDocument()
    expect(screen.getByText('loading:false')).toBeInTheDocument()
  })

  it('opening the same parent twice short-circuits (no second RPC)', async () => {
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    setup()
    await act(async () => { screen.getByText('open').click() })
    request.mockClear()
    await act(async () => { screen.getByText('open').click() })
    expect(request).not.toHaveBeenCalled()
  })

  it('closeThread resets state', async () => {
    request.mockResolvedValue({ messages: [{ id: 'r1' }], hasNext: false, nextCursor: null })
    setup()
    await act(async () => { screen.getByText('open').click() })
    await act(async () => { screen.getByText('close').click() })
    expect(screen.getByText('active:none')).toBeInTheDocument()
    expect(screen.getByText('count:0')).toBeInTheDocument()
  })

  it('sendReply optimistically appends and publishes msg.send with thread parent fields', async () => {
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    publish.mockResolvedValue()
    setup()
    await act(async () => { screen.getByText('open').click() })
    await act(async () => { screen.getByText('send').click() })
    expect(screen.getByText('count:1')).toBeInTheDocument()
    expect(publish).toHaveBeenCalledWith(
      'chat.user.alice.room.r1.s1.msg.send',
      {
        id: 'OPT-000000000000000000',
        content: 'hi',
        requestId: 'req-uuid',
        threadParentMessageId: 'p1',
        threadParentMessageCreatedAt: 1000,
      }
    )
  })

  it('sendReply with quotedParentMessageId carries the field in the payload', async () => {
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    publish.mockResolvedValue()
    setup()
    await act(async () => { screen.getByText('open').click() })
    await act(async () => { screen.getByText('send-quote').click() })
    const call = publish.mock.calls[0]
    expect(call[1].quotedParentMessageId).toBe('q-id')
  })

  it('sendReply publish failure tags _status=failed on the optimistic row', async () => {
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    publish.mockRejectedValue(new Error('nope'))
    setup()
    await act(async () => { screen.getByText('open').click() })
    await act(async () => { screen.getByText('send').click() })
    // Probe doesn't expose per-row state, but a count:1 + ability to retry/dismiss
    // is sufficient for plumbing. The reducer test already verifies _status.
    expect(screen.getByText('count:1')).toBeInTheDocument()
  })

  it('dismissReply removes the row', async () => {
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    publish.mockRejectedValue(new Error('nope'))
    setup()
    await act(async () => { screen.getByText('open').click() })
    await act(async () => { screen.getByText('send').click() })
    await act(async () => { screen.getByText('dismiss').click() })
    expect(screen.getByText('count:0')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/context/ThreadEventsContext.test.jsx`
Expected: FAIL — module doesn't exist yet.

- [ ] **Step 3: Write the context**

Create `chat-frontend/src/context/ThreadEventsContext.jsx`:

```jsx
import { createContext, useCallback, useContext, useEffect, useReducer, useRef } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from './NatsContext'
import { generateMessageID } from '../lib/idgen'
import { msgSend, msgThread } from '../lib/subjects'
import { threadEventsReducer, initialState } from '../lib/threadEventsReducer'

const ThreadEventsContext = createContext(null)

export function ThreadEventsProvider({ children }) {
  const { user, request, publish } = useNats()
  const [state, dispatch] = useReducer(threadEventsReducer, initialState)
  const generationRef = useRef(0)
  const stateRef = useRef(state)
  stateRef.current = state

  // Reset on logout.
  useEffect(() => {
    if (!user) dispatch({ type: 'RESET' })
  }, [user])

  const openThread = useCallback(
    (parent) => {
      // Short-circuit if it's already the same parent (mirrors reducer guard).
      if (stateRef.current.activeParent?.messageId === parent.messageId) return
      const myGen = ++generationRef.current
      dispatch({ type: 'OPEN_THREAD', parent })
      if (!user) return
      const subj = msgThread(user.account, parent.roomId, parent.siteId)
      request(subj, { threadMessageId: parent.messageId, limit: 50 })
        .then((resp) => {
          if (myGen !== generationRef.current) return
          dispatch({ type: 'HISTORY_LOADED', parentId: parent.messageId, resp })
        })
        .catch((err) => {
          if (myGen !== generationRef.current) return
          dispatch({
            type: 'HISTORY_FAILED',
            parentId: parent.messageId,
            error: err?.message ?? String(err),
          })
        })
    },
    [user, request]
  )

  const closeThread = useCallback(() => {
    generationRef.current++
    dispatch({ type: 'CLOSE_THREAD' })
  }, [])

  const publishReply = useCallback(
    (id, content, opts) => {
      const parent = stateRef.current.activeParent
      if (!parent || !user) return Promise.reject(new Error('no active thread'))
      const payload = {
        id,
        content,
        requestId: uuidv4(),
        threadParentMessageId: parent.messageId,
        threadParentMessageCreatedAt: parent.createdAtMs,
      }
      if (opts?.quotedParentMessageId) payload.quotedParentMessageId = opts.quotedParentMessageId
      return Promise.resolve(publish(msgSend(user.account, parent.roomId, parent.siteId), payload))
    },
    [user, publish]
  )

  const sendReply = useCallback(
    async (content, opts) => {
      const parent = stateRef.current.activeParent
      if (!parent || !user || !content || !content.trim()) return
      const id = generateMessageID()
      const optimistic = {
        id,
        content: content.trim(),
        createdAt: new Date().toISOString(),
        sender: { account: user.account },
        threadParentMessageId: parent.messageId,
        threadParentMessageCreatedAt: new Date(parent.createdAtMs).toISOString(),
        _local: true,
      }
      if (opts?.quotedParentMessageId) {
        optimistic.quotedParentMessage = {
          id: opts.quotedParentMessageId,
          senderName: opts.quotedSnapshot?.senderName,
          content: opts.quotedSnapshot?.content,
        }
      }
      dispatch({ type: 'REPLY_SENT_LOCAL', message: optimistic })
      try {
        await publishReply(id, content.trim(), opts)
      } catch (err) {
        dispatch({ type: 'REPLY_SEND_FAILED', messageId: id, error: err?.message ?? String(err) })
      }
    },
    [user, publishReply]
  )

  const retryReply = useCallback(
    async (messageId) => {
      const row = stateRef.current.messages.find((m) => m.id === messageId)
      if (!row) return
      dispatch({ type: 'REPLY_RETRIED', messageId })
      try {
        await publishReply(
          messageId,
          row.content,
          row.quotedParentMessage ? { quotedParentMessageId: row.quotedParentMessage.id } : undefined
        )
      } catch (err) {
        dispatch({ type: 'REPLY_SEND_FAILED', messageId, error: err?.message ?? String(err) })
      }
    },
    [publishReply]
  )

  const dismissReply = useCallback((messageId) => {
    dispatch({ type: 'REPLY_DISMISSED', messageId })
  }, [])

  const value = {
    activeParent: state.activeParent,
    messages: state.messages,
    hasLoadedHistory: state.hasLoadedHistory,
    historyLoading: state.historyLoading,
    historyError: state.historyError,
    openThread,
    closeThread,
    sendReply,
    retryReply,
    dismissReply,
  }

  return <ThreadEventsContext.Provider value={value}>{children}</ThreadEventsContext.Provider>
}

export function useThreadEvents() {
  const ctx = useContext(ThreadEventsContext)
  if (!ctx) throw new Error('useThreadEvents must be used inside ThreadEventsProvider')
  return ctx
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd chat-frontend && npx vitest run src/context/ThreadEventsContext.test.jsx`
Expected: PASS (8 tests).

- [ ] **Step 5: Run the full suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/context/ThreadEventsContext.jsx chat-frontend/src/context/ThreadEventsContext.test.jsx
git commit -m "feat(chat-frontend): add ThreadEventsContext"
```

### Task 6.3: Mount `ThreadEventsProvider` under `RoomEventsProvider`

Wire the provider in `App.jsx`. Order: `NatsProvider` → `RoomEventsProvider` → `ThreadEventsProvider` → `MainApp`. Nothing in `MainApp` consumes it yet (Chapter 7), but the hook becomes safely callable from descendants.

**Files:**
- Modify: `chat-frontend/src/App.jsx`

- [ ] **Step 1: Add the import and wrap MainApp**

In `chat-frontend/src/App.jsx`:

```jsx
import { ThreadEventsProvider } from './context/ThreadEventsContext'
```

Change the connected branch:

```jsx
  return (
    <RoomEventsProvider>
      <ThreadEventsProvider>
        <MainApp />
      </ThreadEventsProvider>
    </RoomEventsProvider>
  )
```

- [ ] **Step 2: Run the full suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS — `MainApp` and its descendants don't consume the new context yet, so nothing should break.

- [ ] **Step 3: Manual smoke check**

Run: `cd chat-frontend && npm run dev`
Sign in, click around. No visible changes. If there's a runtime error, it'll be in the console — fix before committing.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/App.jsx
git commit -m "feat(chat-frontend): mount ThreadEventsProvider in App"
```

---

## Chapter 7 — Thread panel UI

Goal: render the thread panel and wire the Thread hover icon to open it. By the end of this chapter, the user can click 💬 on any message → the right rail appears → the parent is shown plus existing replies (oldest-first) → typing in the thread input publishes a reply. Loading / error / empty states are rendered. Failed optimistic rows render with ⟳ + ✕ buttons. The panel auto-scrolls on open and on own-replies.

Layout under `MainApp`: when `useThreadEvents().activeParent !== null`, mount `<ThreadRightBar>` as a third column in `.app-row`. Otherwise it's not in the DOM at all.

### Task 7.1: Create `ThreadMessageInput.jsx`

Mirror of `RoomMessageInput`, but uses `useThreadEvents().sendReply` instead of `publish(msg.send)`. Receives `quotedTarget` + `onClearQuote` from the parent (`ThreadRightBar`) since the Reply icon for thread replies stages there.

**Files:**
- Create: `chat-frontend/src/components/ThreadMessageInput.jsx`
- Create: `chat-frontend/src/components/ThreadMessageInput.test.jsx`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/components/ThreadMessageInput.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import ThreadMessageInput from './ThreadMessageInput'

const sendReply = vi.fn(async () => {})
vi.mock('../context/ThreadEventsContext', () => ({
  useThreadEvents: () => ({ sendReply, activeParent: { messageId: 'p1' } }),
}))

describe('ThreadMessageInput', () => {
  beforeEach(() => sendReply.mockClear())

  it('renders with placeholder "Reply…"', () => {
    render(<ThreadMessageInput />)
    expect(screen.getByPlaceholderText('Reply…')).toBeInTheDocument()
  })

  it('Enter calls sendReply with content and clears the textbox', () => {
    render(<ThreadMessageInput />)
    const input = screen.getByPlaceholderText('Reply…')
    fireEvent.change(input, { target: { value: 'in-thread' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(sendReply).toHaveBeenCalledWith('in-thread', {})
    expect(input).toHaveValue('')
  })

  it('does not send empty / whitespace', () => {
    render(<ThreadMessageInput />)
    fireEvent.keyDown(screen.getByPlaceholderText('Reply…'), { key: 'Enter' })
    expect(sendReply).not.toHaveBeenCalled()
  })

  it('passes quotedTarget id through sendReply opts and renders the chip', () => {
    const onClearQuote = vi.fn()
    render(
      <ThreadMessageInput
        quotedTarget={{ id: 'q1', senderName: 'bob', content: 'orig' }}
        onClearQuote={onClearQuote}
      />
    )
    expect(screen.getByText('bob')).toBeInTheDocument()
    const input = screen.getByPlaceholderText('Reply…')
    fireEvent.change(input, { target: { value: 'reply' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(sendReply).toHaveBeenCalledWith('reply', { quotedParentMessageId: 'q1' })
  })

  it('clears the staged quote chip after a successful send', async () => {
    sendReply.mockResolvedValue()
    const onClearQuote = vi.fn()
    render(
      <ThreadMessageInput
        quotedTarget={{ id: 'q1', senderName: 'bob', content: 'orig' }}
        onClearQuote={onClearQuote}
      />
    )
    const input = screen.getByPlaceholderText('Reply…')
    fireEvent.change(input, { target: { value: 'reply' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    // Wait for the awaited sendReply promise to settle.
    await Promise.resolve()
    expect(onClearQuote).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/components/ThreadMessageInput.test.jsx`
Expected: FAIL — module missing.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/ThreadMessageInput.jsx`:

```jsx
import { useState } from 'react'
import { useThreadEvents } from '../context/ThreadEventsContext'
import MessageInputForm from './messages/MessageInputForm'

export default function ThreadMessageInput({ quotedTarget, onClearQuote }) {
  const { sendReply, activeParent } = useThreadEvents()
  const [text, setText] = useState('')

  const handleSubmit = async () => {
    if (!text.trim() || !activeParent) return
    const opts = quotedTarget ? { quotedParentMessageId: quotedTarget.id } : {}
    setText('')
    await sendReply(text.trim(), opts)
    onClearQuote?.()
  }

  return (
    <MessageInputForm
      value={text}
      onChange={setText}
      onSubmit={handleSubmit}
      placeholder="Reply…"
      disabled={!activeParent}
      quotedTarget={quotedTarget}
      onClearQuote={onClearQuote}
    />
  )
}
```

- [ ] **Step 4: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/ThreadMessageInput.test.jsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/ThreadMessageInput.jsx chat-frontend/src/components/ThreadMessageInput.test.jsx
git commit -m "feat(chat-frontend): add ThreadMessageInput container"
```

### Task 7.2: Create `ThreadMessageArea.jsx`

Container that reads thread state (`activeParent`, `messages`, `hasLoadedHistory`, `historyLoading`, `historyError`) and renders the parent message first followed by the reply list. Passes `onReply` upward to `ThreadRightBar` (parent), and routes failed-reply actions to `retryReply` / `dismissReply` from context.

The parent itself is rendered by reading the live message from `useRoomEvents(activeParent.roomId).messages.find(...)` and falling back to a thin synthetic if the parent isn't buffered.

**Files:**
- Create: `chat-frontend/src/components/ThreadMessageArea.jsx`
- Create: `chat-frontend/src/components/ThreadMessageArea.test.jsx`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/components/ThreadMessageArea.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import ThreadMessageArea from './ThreadMessageArea'

const activeParent = { roomId: 'r1', siteId: 's1', messageId: 'p1', createdAtMs: 1000 }
const retryReply = vi.fn()
const dismissReply = vi.fn()

vi.mock('../context/ThreadEventsContext', () => ({
  useThreadEvents: () => ({
    activeParent,
    messages: [
      { id: 'reply-1', content: 'first reply', createdAt: '2026-05-13T10:01:00Z', sender: { account: 'bob' } },
      { id: 'reply-2', content: 'optimistic', _local: true, _status: 'failed', sender: { account: 'alice' } },
    ],
    hasLoadedHistory: true,
    historyLoading: false,
    historyError: null,
    retryReply, dismissReply,
  }),
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: () => ({
    messages: [
      { id: 'p1', content: 'parent body', createdAt: '2026-05-13T10:00:00Z', sender: { account: 'alice' } },
    ],
  }),
}))
vi.mock('../context/NatsContext', () => ({
  useNats: () => ({ user: { account: 'alice', siteId: 's1' }, publish: vi.fn() }),
}))
vi.mock('./messages/MessageList', () => ({
  default: ({ messages, emptyText, context, onReply, onRetry, onDismiss, historyLoading, historyError }) => (
    <div data-testid="list">
      <span>context:{context}</span>
      <span>count:{messages.length}</span>
      <span>loading:{String(!!historyLoading)}</span>
      <span>error:{historyError ?? 'none'}</span>
      <span>empty:{emptyText ?? 'none'}</span>
      {messages.map((m) => (
        <div key={m.id} data-row={m.id}>{m.content || '[deleted]'}{m._status === 'failed' ? ' (failed)' : ''}</div>
      ))}
      <button type="button" onClick={() => onReply?.({ id: 'reply-1', sender: { account: 'bob' }, content: 'first reply' })}>fire-reply</button>
      <button type="button" onClick={() => onRetry?.('reply-2')}>fire-retry</button>
      <button type="button" onClick={() => onDismiss?.('reply-2')}>fire-dismiss</button>
    </div>
  ),
}))

describe('ThreadMessageArea', () => {
  it('renders the parent as the first row, then the replies', () => {
    render(<ThreadMessageArea onReply={() => {}} />)
    const ids = Array.from(document.querySelectorAll('[data-row]')).map((el) => el.getAttribute('data-row'))
    expect(ids).toEqual(['p1', 'reply-1', 'reply-2'])
  })

  it('passes context="thread" to MessageList', () => {
    render(<ThreadMessageArea onReply={() => {}} />)
    expect(screen.getByText('context:thread')).toBeInTheDocument()
  })

  it('forwards onReply with the reply payload', () => {
    const onReply = vi.fn()
    render(<ThreadMessageArea onReply={onReply} />)
    fireEvent.click(screen.getByText('fire-reply'))
    expect(onReply).toHaveBeenCalled()
  })

  it('forwards retry / dismiss to ThreadEventsContext', () => {
    render(<ThreadMessageArea onReply={() => {}} />)
    fireEvent.click(screen.getByText('fire-retry'))
    expect(retryReply).toHaveBeenCalledWith('reply-2')
    fireEvent.click(screen.getByText('fire-dismiss'))
    expect(dismissReply).toHaveBeenCalledWith('reply-2')
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/components/ThreadMessageArea.test.jsx`
Expected: FAIL — module missing.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/ThreadMessageArea.jsx`:

```jsx
import { useEffect, useRef } from 'react'
import { useThreadEvents } from '../context/ThreadEventsContext'
import { useRoomEvents } from '../context/RoomEventsContext'
import { useNats } from '../context/NatsContext'
import MessageList from './messages/MessageList'

export default function ThreadMessageArea({ onReply }) {
  const { activeParent, messages, hasLoadedHistory, historyLoading, historyError, retryReply, dismissReply } = useThreadEvents()
  const { messages: roomMessages } = useRoomEvents(activeParent?.roomId ?? null)
  const { user } = useNats()
  const bottomRef = useRef(null)

  // Resolve parent live from main feed buffer; fall back to a thin stub if scrolled out.
  const parent = activeParent
    ? roomMessages.find((m) => m.id === activeParent.messageId) ?? {
        id: activeParent.messageId,
        createdAt: new Date(activeParent.createdAtMs).toISOString(),
        content: '',
      }
    : null

  const combined = parent ? [parent, ...messages] : messages

  // Auto-scroll on history load and on every own optimistic append.
  useEffect(() => {
    if (!hasLoadedHistory) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [hasLoadedHistory])

  useEffect(() => {
    // Pin-to-bottom on append-while-pinned. Cheap: always scroll on append;
    // any "user scrolled up" finesse is out of scope for v1.
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages.length])

  if (!activeParent) return null

  const empty = hasLoadedHistory && !historyLoading && !historyError && messages.length === 0

  return (
    <div className="thread-message-area">
      <MessageList
        messages={combined}
        hasLoadedHistory={hasLoadedHistory}
        historyLoading={historyLoading}
        historyError={historyError}
        context="thread"
        currentUserAccount={user?.account}
        emptyText={empty ? 'No replies yet — be the first to reply' : undefined}
        onReply={onReply}
        onRetry={retryReply}
        onDismiss={dismissReply}
        bottomRef={bottomRef}
        ariaLive="polite"
      />
    </div>
  )
}
```

- [ ] **Step 4: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/ThreadMessageArea.test.jsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/ThreadMessageArea.jsx chat-frontend/src/components/ThreadMessageArea.test.jsx
git commit -m "feat(chat-frontend): add ThreadMessageArea container"
```

### Task 7.3: Extend `MessageList` to forward retry/dismiss + parent context

`MessageList` needs to receive `onRetry`, `onDismiss`, and pass them into `MessageRow`. Also: when rendering a row whose `id === parent.messageId` and the list is `context="thread"`, override the row's `context` to `"thread-parent"` so `MessageActions` hides Thread + Reply.

**Files:**
- Modify: `chat-frontend/src/components/messages/MessageList.jsx`

- [ ] **Step 1: Update `MessageList` props and per-row rendering**

In `chat-frontend/src/components/messages/MessageList.jsx`, extend destructure with `onRetry`, `onDismiss`, `parentMessageId`. Per-row render becomes:

```jsx
{messages.map((msg) => {
  const isParent = context === 'thread' && parentMessageId === msg.id
  const rowContext = isParent ? 'thread-parent' : context
  return (
    <MessageRow
      key={msg.id}
      message={msg}
      context={rowContext}
      isOwn={!!currentUserAccount && msg.sender?.account === currentUserAccount}
      editing={editingMessageId === msg.id}
      onThread={onThread}
      onReply={onReply}
      onEdit={onEdit}
      onEditSubmit={onEditSubmit}
      onEditCancel={onEditCancel}
      onDelete={onDelete}
      onJumpToMessage={onJumpToMessage}
      onRetry={onRetry}
      onDismiss={onDismiss}
    />
  )
})}
```

Update `ThreadMessageArea` to pass `parentMessageId={activeParent.messageId}` to `<MessageList>`.

- [ ] **Step 2: Add the failing tests in MessageList**

Append to `chat-frontend/src/components/messages/MessageList.test.jsx`:

```jsx
it('marks the parent row context as thread-parent inside a thread list', () => {
  vi.mock('./MessageRow', () => ({
    default: ({ message, context: c }) => <div data-testid={`row-${message.id}`}>ctx:{c}</div>,
  }))
  const list = [{ id: 'p1' }, { id: 'r1' }]
  render(
    <MessageList
      messages={list}
      hasLoadedHistory
      context="thread"
      parentMessageId="p1"
    />
  )
  expect(screen.getByTestId('row-p1').textContent).toBe('ctx:thread-parent')
  expect(screen.getByTestId('row-r1').textContent).toBe('ctx:thread')
})
```

(If the existing top-of-file mock for `MessageRow` differs, harmonise — the test should re-mock per-it via `vi.doMock` to avoid leaking.)

- [ ] **Step 3: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageList.test.jsx`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/messages/MessageList.jsx chat-frontend/src/components/messages/MessageList.test.jsx chat-frontend/src/components/ThreadMessageArea.jsx
git commit -m "feat(chat-frontend): MessageList honors parentMessageId for thread-parent rows"
```

### Task 7.4: Extend `MessageRow` to render failed-row UI (⟳ + ✕)

When `message._status === 'failed'`, render a small toolbar on the row with ⟳ (retry) and ✕ (dismiss) buttons that call `onRetry(message.id)` / `onDismiss(message.id)`.

**Files:**
- Modify: `chat-frontend/src/components/messages/MessageRow.jsx`
- Modify: `chat-frontend/src/components/messages/MessageRow.test.jsx`

- [ ] **Step 1: Append failing tests**

Append to `chat-frontend/src/components/messages/MessageRow.test.jsx`:

```jsx
describe('MessageRow — failed-row UI', () => {
  it('renders Retry and Dismiss when _status is failed', () => {
    render(
      <MessageRow
        message={{ ...msg, _status: 'failed', _local: true }}
        context="thread"
        onRetry={() => {}}
        onDismiss={() => {}}
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /dismiss/i })).toBeInTheDocument()
  })

  it('clicking Retry calls onRetry with message id; Dismiss calls onDismiss', () => {
    const onRetry = vi.fn(); const onDismiss = vi.fn()
    render(
      <MessageRow
        message={{ ...msg, _status: 'failed', _local: true }}
        context="thread"
        onRetry={onRetry}
        onDismiss={onDismiss}
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(onRetry).toHaveBeenCalledWith('m1')
    fireEvent.click(screen.getByRole('button', { name: /dismiss/i }))
    expect(onDismiss).toHaveBeenCalledWith('m1')
  })

  it('does not render Retry/Dismiss when _status is not failed', () => {
    render(
      <MessageRow message={msg} context="thread" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />
    )
    expect(screen.queryByRole('button', { name: /retry/i })).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Add the markup**

In `chat-frontend/src/components/messages/MessageRow.jsx`, after the `<MessageActions … />` and before the closing tag, add:

```jsx
{message._status === 'failed' && (
  <div className="message-row-failed">
    <span className="message-row-failed-label">Failed to send.</span>
    <button type="button" aria-label="Retry sending message" onClick={() => onRetry?.(message.id)}>⟳</button>
    <button type="button" aria-label="Dismiss failed message" onClick={() => onDismiss?.(message.id)}>✕</button>
  </div>
)}
```

Also accept `onRetry`, `onDismiss` in the props destructure.

- [ ] **Step 3: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageRow.test.jsx`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/messages/MessageRow.jsx chat-frontend/src/components/messages/MessageRow.test.jsx
git commit -m "feat(chat-frontend): failed-row Retry / Dismiss buttons on MessageRow"
```

### Task 7.5: Create `ThreadRightBar.jsx`

Header strip with title "Thread" + ✕ close button. Owns the per-thread `quotedTarget` state (lifted up from the inputs so the Reply icon on a thread reply can stage it). Body = `ThreadMessageArea` + `ThreadMessageInput`. Width fixed via CSS class.

**Files:**
- Create: `chat-frontend/src/components/ThreadRightBar.jsx`
- Create: `chat-frontend/src/components/ThreadRightBar.test.jsx`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/components/ThreadRightBar.test.jsx`:

```jsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import ThreadRightBar from './ThreadRightBar'

const closeThread = vi.fn()
vi.mock('../context/ThreadEventsContext', () => ({
  useThreadEvents: () => ({ activeParent: { messageId: 'p1' }, closeThread }),
}))
vi.mock('./ThreadMessageArea', () => ({
  default: ({ onReply }) => (
    <div>
      area
      <button type="button" onClick={() => onReply?.({ id: 'r-orig', sender: { account: 'bob' }, content: 'orig' })}>
        fire-reply
      </button>
    </div>
  ),
}))
vi.mock('./ThreadMessageInput', () => ({
  default: ({ quotedTarget, onClearQuote }) => (
    <div>
      input
      {quotedTarget && (
        <>
          <span data-testid="t-staged">{quotedTarget.id}</span>
          <button type="button" onClick={onClearQuote}>t-clear</button>
        </>
      )}
    </div>
  ),
}))

describe('ThreadRightBar', () => {
  beforeEach(() => closeThread.mockClear())

  it('renders header, area, and input', () => {
    render(<ThreadRightBar />)
    expect(screen.getByText('Thread')).toBeInTheDocument()
    expect(screen.getByText('area')).toBeInTheDocument()
    expect(screen.getByText('input')).toBeInTheDocument()
  })

  it('✕ close button calls closeThread', () => {
    render(<ThreadRightBar />)
    fireEvent.click(screen.getByRole('button', { name: /close thread/i }))
    expect(closeThread).toHaveBeenCalled()
  })

  it('Reply inside the thread stages a quote in the thread input', () => {
    render(<ThreadRightBar />)
    fireEvent.click(screen.getByText('fire-reply'))
    expect(screen.getByTestId('t-staged').textContent).toBe('r-orig')
  })

  it('clearing the chip removes the staged quote', () => {
    render(<ThreadRightBar />)
    fireEvent.click(screen.getByText('fire-reply'))
    fireEvent.click(screen.getByText('t-clear'))
    expect(screen.queryByTestId('t-staged')).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/components/ThreadRightBar.test.jsx`
Expected: FAIL.

- [ ] **Step 3: Write the component**

Create `chat-frontend/src/components/ThreadRightBar.jsx`:

```jsx
import { useState } from 'react'
import { useThreadEvents } from '../context/ThreadEventsContext'
import ThreadMessageArea from './ThreadMessageArea'
import ThreadMessageInput from './ThreadMessageInput'

export default function ThreadRightBar() {
  const { activeParent, closeThread } = useThreadEvents()
  const [quotedTarget, setQuotedTarget] = useState(null)

  if (!activeParent) return null

  const handleReply = (msg) => {
    setQuotedTarget({
      id: msg.id,
      senderName: msg.sender?.engName || msg.sender?.account || msg.userAccount || 'Unknown',
      content: msg.content || msg.msg || '',
    })
  }

  return (
    <aside className="thread-rightbar">
      <header className="thread-header">
        <span className="thread-header-title">Thread</span>
        <button
          type="button"
          className="thread-header-close"
          aria-label="Close thread"
          onClick={closeThread}
        >
          ✕
        </button>
      </header>
      <ThreadMessageArea onReply={handleReply} />
      <ThreadMessageInput
        quotedTarget={quotedTarget}
        onClearQuote={() => setQuotedTarget(null)}
      />
    </aside>
  )
}
```

- [ ] **Step 4: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/ThreadRightBar.test.jsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/components/ThreadRightBar.jsx chat-frontend/src/components/ThreadRightBar.test.jsx
git commit -m "feat(chat-frontend): add ThreadRightBar"
```

### Task 7.6: Mount `ThreadRightBar` in `MainApp` and wire the Thread icon

When `useThreadEvents().activeParent !== null`, render `ThreadRightBar` as the third column in `.app-row`. Also: hook the Thread icon (existing `onThread` prop) so clicking it calls `openThread(parent)` with all required fields.

**Files:**
- Modify: `chat-frontend/src/components/MainApp.jsx`
- Modify: `chat-frontend/src/components/MainApp.test.jsx`
- Modify: `chat-frontend/src/pages/ChatPage.jsx`

- [ ] **Step 1: Failing test on MainApp**

Append to `chat-frontend/src/components/MainApp.test.jsx`:

```jsx
describe('MainApp — ThreadRightBar mount', () => {
  it('mounts ThreadRightBar only when an active thread exists', async () => {
    vi.resetModules()
    let activeParent = null
    vi.doMock('../context/ThreadEventsContext', () => ({
      useThreadEvents: () => ({ activeParent }),
    }))
    vi.doMock('./ThreadRightBar', () => ({ default: () => <aside>RIGHT-BAR</aside> }))
    const { default: Re } = await import('./MainApp')
    const { rerender } = render(<Re />)
    expect(screen.queryByText('RIGHT-BAR')).not.toBeInTheDocument()
    activeParent = { messageId: 'p1' }
    rerender(<Re />)
    expect(screen.getByText('RIGHT-BAR')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Update `MainApp.jsx`**

In `chat-frontend/src/components/MainApp.jsx`, add:

```jsx
import { useThreadEvents } from '../context/ThreadEventsContext'
import ThreadRightBar from './ThreadRightBar'
```

Inside the component:

```jsx
  const { activeParent } = useThreadEvents()
```

Render `ThreadRightBar` after `ChatPage` (or `SearchResultsPane`):

```jsx
      <div className="app-row">
        <Sidebar selectedRoomId={selectedRoom?.id ?? null} onSelectRoom={handleSelectRoom} />
        {searchQuery ? (
          <SearchResultsPane … />
        ) : (
          <ChatPage selectedRoom={selectedRoom} onSelectRoom={handleSelectRoom} />
        )}
        {activeParent && <ThreadRightBar />}
      </div>
```

- [ ] **Step 3: Wire the Thread icon in ChatPage**

`ChatPage` currently passes `onThread={() => {}}` to `<RoomMessageArea>`. Change it:

```jsx
import { useThreadEvents } from '../context/ThreadEventsContext'

// inside the component:
const { openThread } = useThreadEvents()

const handleThread = (msg) => {
  if (!selectedRoom || !msg) return
  openThread({
    roomId: selectedRoom.id,
    siteId: selectedRoom.siteId,
    messageId: msg.id,
    createdAtMs: new Date(msg.createdAt).getTime(),
  })
}
```

And pass `onThread={handleThread}` to `<RoomMessageArea>`.

- [ ] **Step 4: Update `ChatPage.test.jsx`**

Add at the top of the file (with the other mocks):

```jsx
const openThread = vi.fn()
vi.mock('../context/ThreadEventsContext', () => ({
  useThreadEvents: () => ({ openThread, activeParent: null }),
}))
```

Add a test inside `describe('ChatPage — quote-reply staging')` or in a new describe block:

```jsx
describe('ChatPage — opening a thread', () => {
  beforeEach(() => openThread.mockClear())

  it('clicking Thread on a message calls openThread with full parent identity', () => {
    // Stub RoomMessageArea to fire onThread on demand.
    vi.doMock('../components/RoomMessageArea', () => ({
      default: ({ onThread }) => (
        <button
          type="button"
          onClick={() =>
            onThread?.({
              id: 'm-thread',
              createdAt: '2026-05-13T10:00:00.000Z',
            })
          }
        >
          fire-thread
        </button>
      ),
    }))
    // Re-import after mock.
    vi.resetModules()
    // ...the imports above re-execute via test file ordering; rely on the
    // existing mocks for RoomMessageArea unless this it() runs in isolation.
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.click(screen.getByText('fire-thread'))
    expect(openThread).toHaveBeenCalledWith({
      roomId: 'r1',
      siteId: channel.siteId,
      messageId: 'm-thread',
      createdAtMs: Date.parse('2026-05-13T10:00:00.000Z'),
    })
  })
})
```

(If the inline `vi.doMock` confuses the rest of the file's mocks, lift the `fire-thread` button into the existing `RoomMessageArea` mock at the top of the file alongside `fire-reply`.)

- [ ] **Step 5: Run the suites**

Run: `cd chat-frontend && npx vitest run src/components/MainApp.test.jsx src/pages/ChatPage.test.jsx`
Expected: PASS.

- [ ] **Step 6: Manual smoke check**

Run: `cd chat-frontend && npm run dev`
Sign in. Click 💬 on a message. Verify:
- Thread side panel appears on the right.
- Parent message is at the top.
- "No replies yet — be the first to reply" shows for new threads.
- Loading state is visible briefly on open.
- Clicking ✕ closes the panel.
- Replying inside the thread works (the new reply appears optimistically).
- The thread reply does NOT appear in the main feed (we haven't added the filter yet — Chapter 8 — so it currently WILL appear; note this in the smoke check but don't fail the chapter).

- [ ] **Step 7: Commit**

```bash
git add chat-frontend/src/components/MainApp.jsx chat-frontend/src/components/MainApp.test.jsx chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "feat(chat-frontend): mount ThreadRightBar in MainApp; wire Thread hover icon"
```

### Task 7.7: CSS for thread panel layout

Width 380 px, vertical flex (header / area / input), border-left, scrollable area in the middle.

**Files:**
- Modify: `chat-frontend/src/styles/index.css`

- [ ] **Step 1: Append CSS**

Append to `chat-frontend/src/styles/index.css`:

```css
.thread-rightbar {
  display: flex;
  flex-direction: column;
  width: 380px;
  flex-shrink: 0;
  border-left: 1px solid var(--border-subtle, #e5e7eb);
  background: var(--bg-surface);
  min-height: 0;
}
.thread-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--bg-surface);
}
.thread-header-title {
  font-weight: 600;
}
.thread-header-close {
  background: transparent;
  border: 0;
  cursor: pointer;
  font-size: 1.1em;
  padding: 2px 6px;
}
.thread-message-area {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.thread-rightbar .message-input-form {
  border-top: 1px solid var(--border-subtle);
}
.message-row-failed {
  display: flex;
  gap: 6px;
  margin-top: 2px;
  color: var(--text-error, #b00020);
  font-size: 0.85em;
}
.message-row-failed button {
  background: transparent;
  border: 0;
  cursor: pointer;
}
```

- [ ] **Step 2: Smoke check**

Run: `cd chat-frontend && npm run dev`
Open a thread; verify the panel renders the full vertical layout with a scrollable middle.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/styles/index.css
git commit -m "style(chat-frontend): thread panel layout + failed-row indicator"
```

---

## Chapter 8 — Cross-context wiring + main-feed filter + tcount badge

Goal: stop thread replies from flickering into the main feed, bump the parent's `tcount` when the local user sends a thread reply, and render a clickable "💬 N replies" badge on parent messages.

### Task 8.1: Filter thread replies out of the main feed live broadcast

`broadcast-worker` publishes every `MessageEvent` to `chat.room.{roomId}.event`, including thread replies (verified in the spec — see `pkg/model/event.go` and `broadcast-worker/handler.go`). Drop them in `MESSAGE_RECEIVED`.

**Files:**
- Modify: `chat-frontend/src/lib/roomEventsReducer.js`
- Modify: `chat-frontend/src/lib/roomEventsReducer.test.js`

- [ ] **Step 1: Append failing test**

Append to `chat-frontend/src/lib/roomEventsReducer.test.js`:

```js
describe('MESSAGE_RECEIVED — thread-reply filter', () => {
  it('drops events whose message.threadParentMessageId is non-empty', () => {
    const seed = {
      ...initialState,
      summaries: [{ id: 'r1', name: 'general', type: 'channel', siteId: 's', userCount: 1, lastMsgAt: null, unreadCount: 0, hasMention: false, mentionAll: false }],
      activeRoomId: 'r1',
      roomState: {
        r1: {
          messages: [{ id: 'm-existing' }],
          hasLoadedHistory: true, historyError: null,
          unreadCount: 0, hasMention: false, mentionAll: false,
          lastMsgAt: null, lastMsgId: null,
          bufferMode: 'live', pendingLiveMessages: [], focusMessageId: null,
        },
      },
    }
    const out = roomEventsReducer(seed, {
      type: 'MESSAGE_RECEIVED',
      event: {
        roomId: 'r1',
        message: { id: 'reply-1', content: 'thread', threadParentMessageId: 'parent-1' },
      },
    })
    expect(out).toBe(seed)
  })

  it('still appends events whose threadParentMessageId is empty', () => {
    const seed = {
      ...initialState,
      summaries: [{ id: 'r1', name: 'general', type: 'channel', siteId: 's', userCount: 1, lastMsgAt: null, unreadCount: 0, hasMention: false, mentionAll: false }],
      activeRoomId: 'r1',
      roomState: {
        r1: {
          messages: [],
          hasLoadedHistory: true, historyError: null,
          unreadCount: 0, hasMention: false, mentionAll: false,
          lastMsgAt: null, lastMsgId: null,
          bufferMode: 'live', pendingLiveMessages: [], focusMessageId: null,
        },
      },
    }
    const out = roomEventsReducer(seed, {
      type: 'MESSAGE_RECEIVED',
      event: { roomId: 'r1', message: { id: 'm-1', content: 'top-level' } },
    })
    expect(out.roomState.r1.messages.map((m) => m.id)).toEqual(['m-1'])
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js -t "thread-reply filter"`
Expected: FAIL — the second test passes but the first fails because today's reducer would append the thread reply.

- [ ] **Step 3: Add the filter**

In `chat-frontend/src/lib/roomEventsReducer.js`, in `case 'MESSAGE_RECEIVED'`, right after the existing empty-event guard:

```js
case 'MESSAGE_RECEIVED': {
  const evt = action.event
  if (!evt.message || !evt.message.id) {
    return state
  }
  // Thread replies are written to thread tables only, but broadcast-worker
  // publishes them on the main subject too. Filter them here so they don't
  // flicker into the main feed.
  if (evt.message.threadParentMessageId) {
    return state
  }
  // …existing logic continues
```

- [ ] **Step 4: Run the tests**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js`
Expected: PASS, including all pre-existing tests.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/roomEventsReducer.js chat-frontend/src/lib/roomEventsReducer.test.js
git commit -m "feat(chat-frontend): filter thread replies out of main feed live broadcast"
```

### Task 8.2: Add `OWN_THREAD_REPLY_SENT` action

Bumps `tcount` on the parent message (and its summary's `lastMsgAt`, but we deliberately do **not** touch the per-room `lastMsgAt` here since the badge doesn't render a timestamp).

**Files:**
- Modify: `chat-frontend/src/lib/roomEventsReducer.js`
- Modify: `chat-frontend/src/lib/roomEventsReducer.test.js`

- [ ] **Step 1: Append failing tests**

```js
describe('OWN_THREAD_REPLY_SENT', () => {
  it('increments tcount on the parent message in roomState[roomId].messages', () => {
    const seed = {
      ...initialState,
      roomState: {
        r1: {
          messages: [{ id: 'p1', content: 'parent', tcount: 0 }],
          hasLoadedHistory: true, historyError: null,
          unreadCount: 0, hasMention: false, mentionAll: false,
          lastMsgAt: null, lastMsgId: null,
          bufferMode: 'live', pendingLiveMessages: [], focusMessageId: null,
        },
      },
    }
    const out = roomEventsReducer(seed, { type: 'OWN_THREAD_REPLY_SENT', roomId: 'r1', parentId: 'p1' })
    expect(out.roomState.r1.messages[0].tcount).toBe(1)
  })

  it('initialises tcount to 1 if previously undefined', () => {
    const seed = {
      ...initialState,
      roomState: { r1: { messages: [{ id: 'p1' }] } },
    }
    const out = roomEventsReducer(seed, { type: 'OWN_THREAD_REPLY_SENT', roomId: 'r1', parentId: 'p1' })
    expect(out.roomState.r1.messages[0].tcount).toBe(1)
  })

  it('is a no-op when the parent isn\'t in the room buffer', () => {
    const seed = { ...initialState, roomState: { r1: { messages: [] } } }
    const out = roomEventsReducer(seed, { type: 'OWN_THREAD_REPLY_SENT', roomId: 'r1', parentId: 'p1' })
    expect(out).toBe(seed)
  })
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js -t "OWN_THREAD_REPLY_SENT"`
Expected: FAIL.

- [ ] **Step 3: Add the action handler**

Before the `default:` clause:

```js
case 'OWN_THREAD_REPLY_SENT': {
  const prev = state.roomState[action.roomId]
  if (!prev) return state
  const idx = prev.messages.findIndex((m) => m.id === action.parentId)
  if (idx < 0) return state
  const tcount = (prev.messages[idx].tcount ?? 0) + 1
  const updatedMsg = { ...prev.messages[idx], tcount }
  const messages = [...prev.messages.slice(0, idx), updatedMsg, ...prev.messages.slice(idx + 1)]
  return {
    ...state,
    roomState: { ...state.roomState, [action.roomId]: { ...prev, messages } },
  }
}
```

- [ ] **Step 4: Run the tests**

Run: `cd chat-frontend && npx vitest run src/lib/roomEventsReducer.test.js -t "OWN_THREAD_REPLY_SENT"`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/roomEventsReducer.js chat-frontend/src/lib/roomEventsReducer.test.js
git commit -m "feat(chat-frontend): roomEventsReducer handles OWN_THREAD_REPLY_SENT"
```

### Task 8.3: Cross-dispatch from `ThreadEventsContext` to `RoomEventsContext`

After a successful `sendReply` publish (no error), `ThreadEventsContext` dispatches `OWN_THREAD_REPLY_SENT` into `RoomEventsContext.dispatch`. This requires `RoomEventsContext.dispatch` to be accessible from the thread context — since both providers live under `App.jsx`, the cleanest path is to read it via a hook in the thread provider's body.

**Files:**
- Modify: `chat-frontend/src/context/ThreadEventsContext.jsx`
- Modify: `chat-frontend/src/context/ThreadEventsContext.test.jsx`
- Modify: `chat-frontend/src/context/RoomEventsContext.jsx` (export a `useRoomDispatch` hook if not already)

- [ ] **Step 1: Ensure a dispatch hook exists**

In `chat-frontend/src/context/RoomEventsContext.jsx`, alongside `useRoomEvents`/`useRoomSummaries`, export:

```js
export function useRoomDispatch() {
  const ctx = useContext(RoomEventsContext)
  if (!ctx) throw new Error('useRoomDispatch must be used inside RoomEventsProvider')
  return ctx.dispatch
}
```

(`dispatch` should already be on the context value from Task 4.7.)

- [ ] **Step 2: Add the failing test**

In `chat-frontend/src/context/ThreadEventsContext.test.jsx`, add at the top of the existing file with the other mocks:

```jsx
const roomDispatch = vi.fn()
vi.mock('./RoomEventsContext', () => ({
  useRoomDispatch: () => roomDispatch,
}))
```

Append:

```jsx
describe('ThreadEventsContext — cross-dispatch OWN_THREAD_REPLY_SENT', () => {
  beforeEach(() => roomDispatch.mockClear())

  it('on successful sendReply, dispatches OWN_THREAD_REPLY_SENT to RoomEventsContext', async () => {
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    publish.mockResolvedValue()
    setup()
    await act(async () => { screen.getByText('open').click() })
    await act(async () => { screen.getByText('send').click() })
    expect(roomDispatch).toHaveBeenCalledWith({
      type: 'OWN_THREAD_REPLY_SENT',
      roomId: 'r1',
      parentId: 'p1',
    })
  })

  it('does NOT dispatch when publish fails', async () => {
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    publish.mockRejectedValue(new Error('nope'))
    setup()
    await act(async () => { screen.getByText('open').click() })
    await act(async () => { screen.getByText('send').click() })
    expect(roomDispatch).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 3: Update `ThreadEventsContext.jsx`**

Import the hook:

```jsx
import { useRoomDispatch } from './RoomEventsContext'
```

Inside `ThreadEventsProvider`:

```jsx
const roomDispatch = useRoomDispatch()
```

Inside `sendReply`, after the `await publishReply` succeeds:

```jsx
try {
  await publishReply(id, content.trim(), opts)
  if (parent) {
    roomDispatch({ type: 'OWN_THREAD_REPLY_SENT', roomId: parent.roomId, parentId: parent.messageId })
  }
} catch (err) {
  dispatch({ type: 'REPLY_SEND_FAILED', messageId: id, error: err?.message ?? String(err) })
}
```

Same for `retryReply`:

```jsx
try {
  await publishReply(messageId, row.content, …)
  if (parent) {
    roomDispatch({ type: 'OWN_THREAD_REPLY_SENT', roomId: parent.roomId, parentId: parent.messageId })
  }
} catch (err) { … }
```

(Read the active parent at the top of each function from `stateRef.current.activeParent`.)

- [ ] **Step 4: Run the tests**

Run: `cd chat-frontend && npx vitest run src/context/ThreadEventsContext.test.jsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/context/ThreadEventsContext.jsx chat-frontend/src/context/ThreadEventsContext.test.jsx chat-frontend/src/context/RoomEventsContext.jsx
git commit -m "feat(chat-frontend): ThreadEventsContext dispatches OWN_THREAD_REPLY_SENT on send success"
```

### Task 8.4: Reply-count badge on `MessageRow`

When `message.tcount > 0`, render a small clickable pill below the content: `💬 {tcount} {tcount === 1 ? 'reply' : 'replies'}`. Click calls `onThread(message)` (same wiring as the hover icon — fires `openThread` upstream).

**Files:**
- Modify: `chat-frontend/src/components/messages/MessageRow.jsx`
- Modify: `chat-frontend/src/components/messages/MessageRow.test.jsx`

- [ ] **Step 1: Append failing tests**

```jsx
describe('MessageRow — reply-count badge', () => {
  it('renders the badge when tcount > 0; clicking calls onThread', () => {
    const onThread = vi.fn()
    render(
      <MessageRow
        message={{ ...msg, tcount: 3 }}
        context="main"
        onThread={onThread}
        onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    const btn = screen.getByRole('button', { name: /3 replies/i })
    fireEvent.click(btn)
    expect(onThread).toHaveBeenCalledWith(expect.objectContaining({ id: 'm1' }))
  })

  it('singular form for tcount === 1', () => {
    render(
      <MessageRow
        message={{ ...msg, tcount: 1 }}
        context="main"
        onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: /^1 reply$/i })).toBeInTheDocument()
  })

  it('no badge when tcount is 0 or missing', () => {
    render(
      <MessageRow message={msg} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />
    )
    expect(screen.queryByRole('button', { name: /replies?/i })).not.toBeInTheDocument()
  })

  it('no badge inside the thread panel even when tcount > 0', () => {
    render(
      <MessageRow message={{ ...msg, tcount: 5 }} context="thread-parent" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />
    )
    expect(screen.queryByRole('button', { name: /replies/i })).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Add the markup**

In `chat-frontend/src/components/messages/MessageRow.jsx`, below the `<div className="message-content">…</div>` and above `<MessageActions />`:

```jsx
{message.tcount > 0 && context !== 'thread' && context !== 'thread-parent' && (
  <button
    type="button"
    className="message-reply-badge"
    onClick={() => onThread?.(message)}
  >
    💬 {message.tcount} {message.tcount === 1 ? 'reply' : 'replies'}
  </button>
)}
```

- [ ] **Step 3: Run the tests**

Run: `cd chat-frontend && npx vitest run src/components/messages/MessageRow.test.jsx`
Expected: PASS.

- [ ] **Step 4: CSS**

Append to `chat-frontend/src/styles/index.css`:

```css
.message-reply-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-top: 4px;
  padding: 2px 8px;
  background: var(--bg-elevated, rgba(0,0,0,0.04));
  border: 1px solid var(--border-subtle, #e5e7eb);
  border-radius: 12px;
  font-size: 0.85em;
  cursor: pointer;
}
.message-reply-badge:hover { background: var(--bg-hover, rgba(0,0,0,0.07)); }
```

- [ ] **Step 5: Manual smoke check**

Run: `cd chat-frontend && npm run dev`
Post a reply in a thread. Watch the parent in the main feed; the "💬 1 reply" badge appears. Click it; thread panel opens.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/components/messages/MessageRow.jsx chat-frontend/src/components/messages/MessageRow.test.jsx chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): reply-count badge on parent messages"
```

### Task 8.5: Close thread on selected-room change

`ChatPage` (or `MainApp`) needs to close the thread when the user navigates to a different room. Without this the thread can outlive its room.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`
- Modify: `chat-frontend/src/pages/ChatPage.test.jsx`

- [ ] **Step 1: Failing test**

Append to `chat-frontend/src/pages/ChatPage.test.jsx`:

```jsx
describe('ChatPage — close thread on room switch', () => {
  it('changing selectedRoom calls closeThread', () => {
    const closeThread = vi.fn()
    vi.doMock('../context/ThreadEventsContext', () => ({
      useThreadEvents: () => ({ openThread: vi.fn(), closeThread, activeParent: { messageId: 'p1' } }),
    }))
    // Re-import after mock.
    return import('./ChatPage').then(({ default: Re }) => {
      const { rerender } = render(<Re selectedRoom={channel} onSelectRoom={() => {}} />)
      rerender(<Re selectedRoom={dm} onSelectRoom={() => {}} />)
      expect(closeThread).toHaveBeenCalled()
    })
  })
})
```

- [ ] **Step 2: Add the effect**

In `chat-frontend/src/pages/ChatPage.jsx`, near the other `useEffect`s:

```jsx
const { openThread, closeThread, activeParent } = useThreadEvents()

useEffect(() => {
  if (activeParent && activeParent.roomId !== selectedRoom?.id) {
    closeThread()
  }
}, [selectedRoom?.id, activeParent, closeThread])
```

- [ ] **Step 3: Run tests**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "feat(chat-frontend): close thread on room switch"
```

---

## Chapter 9 — Accessibility, mutual exclusion, final polish

Goal: close out the spec's accessibility section, enforce mutual exclusion between `InRoomSearch` and `ThreadRightBar`, reset thread state on logout, and run a final integration smoke check end-to-end.

### Task 9.1: Mutual exclusion — InRoomSearch ↔ ThreadRightBar

Spec: opening the thread closes in-room search; opening in-room search closes the thread. Pressing Ctrl-F while a thread is open closes the thread first (so search opens in a fresh state).

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`
- Modify: `chat-frontend/src/pages/ChatPage.test.jsx`

- [ ] **Step 1: Failing tests**

Append to `chat-frontend/src/pages/ChatPage.test.jsx`:

```jsx
describe('ChatPage — mutual exclusion', () => {
  it('opening InRoomSearch (Ctrl-F) closes any open thread', async () => {
    const closeThread = vi.fn()
    vi.doMock('../context/ThreadEventsContext', () => ({
      useThreadEvents: () => ({ openThread: vi.fn(), closeThread, activeParent: { messageId: 'p1' } }),
    }))
    const { default: Re } = await import('./ChatPage')
    render(<Re selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.keyDown(window, { key: 'f', ctrlKey: true })
    expect(closeThread).toHaveBeenCalled()
  })

  it('opening a thread closes InRoomSearch', async () => {
    // Simulate state by toggling InRoomSearch on first then firing onThread.
    // We rely on the existing fire-thread mock in this file.
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.keyDown(window, { key: 'f', ctrlKey: true })
    expect(screen.getByText('in-room-search')).toBeInTheDocument()
    // openThread is dispatched by the existing handler; check that the
    // search panel goes away. (Test verifies the effect, not the cause.)
    fireEvent.click(screen.getByText('fire-thread'))
    expect(screen.queryByText('in-room-search')).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Wire mutual exclusion in `ChatPage.jsx`**

In `chat-frontend/src/pages/ChatPage.jsx`, update the Ctrl-F handler to also close the thread:

```jsx
const handler = (e) => {
  if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.key === 'F')) {
    e.preventDefault()
    if (activeParent) closeThread()
    setInRoomSearchOpen(true)
  } else if (e.key === 'Escape') {
    setInRoomSearchOpen(false)
  }
}
```

And in `handleThread`, close in-room search:

```jsx
const handleThread = (msg) => {
  if (!selectedRoom || !msg) return
  setInRoomSearchOpen(false)
  openThread({
    roomId: selectedRoom.id,
    siteId: selectedRoom.siteId,
    messageId: msg.id,
    createdAtMs: new Date(msg.createdAt).getTime(),
  })
}
```

- [ ] **Step 3: Run tests**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx -t "mutual exclusion"`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "feat(chat-frontend): mutual exclusion InRoomSearch ↔ ThreadRightBar"
```

### Task 9.2: Esc closes the thread panel

When focus is anywhere inside `ThreadRightBar` (and no inner dialog / edit-mode is intercepting), Esc calls `closeThread()`.

**Files:**
- Modify: `chat-frontend/src/components/ThreadRightBar.jsx`
- Modify: `chat-frontend/src/components/ThreadRightBar.test.jsx`

- [ ] **Step 1: Failing test**

Append to `chat-frontend/src/components/ThreadRightBar.test.jsx`:

```jsx
describe('ThreadRightBar — Esc to close', () => {
  it('Esc on the panel closes the thread', () => {
    const { container } = render(<ThreadRightBar />)
    fireEvent.keyDown(container.querySelector('.thread-rightbar'), { key: 'Escape' })
    expect(closeThread).toHaveBeenCalled()
  })

  it('Esc does NOT close when target is an inner input (let inputs handle their own Esc)', () => {
    const { container } = render(<ThreadRightBar />)
    const input = document.createElement('input')
    container.querySelector('.thread-rightbar').appendChild(input)
    closeThread.mockClear()
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(closeThread).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Update `ThreadRightBar.jsx`**

Attach `onKeyDown` on the outer `<aside>`:

```jsx
const handleKeyDown = (e) => {
  if (e.key !== 'Escape') return
  // Let inputs / textareas keep their own Esc semantics (edit cancel, etc).
  const tag = (e.target.tagName || '').toLowerCase()
  if (tag === 'input' || tag === 'textarea') return
  closeThread()
}

return (
  <aside className="thread-rightbar" onKeyDown={handleKeyDown}>
    {/* … */}
  </aside>
)
```

- [ ] **Step 3: Run tests**

Run: `cd chat-frontend && npx vitest run src/components/ThreadRightBar.test.jsx`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/ThreadRightBar.jsx chat-frontend/src/components/ThreadRightBar.test.jsx
git commit -m "feat(chat-frontend): Esc closes the thread panel"
```

### Task 9.3: Focus management on open / close

When the thread panel mounts, move focus to the ✕ button so Esc / Tab are usable immediately. When it unmounts via the local close button, return focus to whatever fired the open.

**Files:**
- Modify: `chat-frontend/src/components/ThreadRightBar.jsx`
- Modify: `chat-frontend/src/components/MainApp.jsx` (or `ChatPage.jsx`) — track the trigger element

- [ ] **Step 1: Focus the ✕ on mount**

In `ThreadRightBar.jsx`:

```jsx
import { useEffect, useRef, useState } from 'react'

// inside the component:
const closeButtonRef = useRef(null)
useEffect(() => {
  closeButtonRef.current?.focus()
}, [])

<button
  type="button"
  ref={closeButtonRef}
  className="thread-header-close"
  aria-label="Close thread"
  onClick={closeThread}
>
  ✕
</button>
```

- [ ] **Step 2: Restore focus on close**

The simplest pattern: `ChatPage` remembers `document.activeElement` before calling `openThread` and restores it after `closeThread`. In `ChatPage.jsx`:

```jsx
const triggerRef = useRef(null)

const handleThread = (msg) => {
  if (!selectedRoom || !msg) return
  triggerRef.current = document.activeElement
  setInRoomSearchOpen(false)
  openThread({…})
}

useEffect(() => {
  if (!activeParent && triggerRef.current) {
    triggerRef.current.focus?.()
    triggerRef.current = null
  }
}, [activeParent])
```

- [ ] **Step 3: Smoke check**

Run: `cd chat-frontend && npm run dev`
Sign in, tab to a message row, press Enter to focus, click 💬 in the action menu (mouse or keyboard). The thread panel opens; the ✕ button has focus. Close it; focus returns to the row.

(Unit-testing focus reliably across providers is fiddly; rely on the manual smoke + the existing button-render unit tests.)

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/ThreadRightBar.jsx chat-frontend/src/pages/ChatPage.jsx
git commit -m "feat(chat-frontend): focus management on thread open/close"
```

### Task 9.4: Logout closes the thread

`ThreadEventsContext` already dispatches `RESET` when `user` becomes null (Task 6.2). Add a sanity test.

**Files:**
- Modify: `chat-frontend/src/context/ThreadEventsContext.test.jsx`

- [ ] **Step 1: Add the test**

Append:

```jsx
describe('ThreadEventsContext — logout', () => {
  it('clears state when user becomes null', async () => {
    let user = { account: 'alice', siteId: 's1' }
    vi.resetModules()
    vi.doMock('./NatsContext', () => ({
      useNats: () => ({ user, request, publish }),
    }))
    const { ThreadEventsProvider: Fresh, useThreadEvents: useFresh } = await import('./ThreadEventsContext')
    function P() {
      const t = useFresh()
      return <span>active:{t.activeParent?.messageId ?? 'none'}</span>
    }
    request.mockResolvedValue({ messages: [], hasNext: false, nextCursor: null })
    const { rerender } = render(<Fresh><P /></Fresh>)
    // Can't trigger openThread from here without exposing; this test just
    // verifies the user→null effect path. Mock now flips user to null.
    user = null
    rerender(<Fresh><P /></Fresh>)
    expect(screen.getByText('active:none')).toBeInTheDocument()
  })
})
```

(The test is somewhat indirect because mocking `useNats` reactively is awkward. If the manipulation is too fragile, replace with a more focused reducer-level test for `RESET`.)

- [ ] **Step 2: Run tests**

Run: `cd chat-frontend && npx vitest run src/context/ThreadEventsContext.test.jsx`
Expected: PASS.

- [ ] **Step 3: Commit (if changes were made)**

If the test passes without code changes (because Task 6.2 already wired the effect), skip the commit. Otherwise commit any minor adjustment.

### Task 9.5: Final integration smoke check + lint

Run the full suite, the linter, and a manual click-through. This is the gate before declaring v1 done.

- [ ] **Step 1: Full chat-frontend test suite**

Run: `cd chat-frontend && npm test -- --run`
Expected: all PASS. Investigate any flakes; do not commit with red tests.

- [ ] **Step 2: Coverage check**

Run: `cd chat-frontend && npx vitest run --coverage`
Expected: ≥ 80 % overall; ≥ 90 % on `lib/threadEventsReducer.js`, `lib/messageBuffer.js`, `context/ThreadEventsContext.jsx`. If anything is below, add the missing tests (especially error paths on reducers / contexts).

- [ ] **Step 3: Lint and format**

Run: `make lint && make fmt`
Expected: clean.

- [ ] **Step 4: Manual end-to-end smoke**

Run: `cd chat-frontend && npm run dev`. Sign in as two users in two browser windows. Verify:

| Scenario | Expected |
|---|---|
| User A posts a top-level message in a channel | Both windows see it in the main feed. |
| User A clicks 💬 on their own message | Right panel opens; "No replies yet…" visible; ✕ has focus. |
| User A posts a reply in the thread input | Reply appears at the bottom of the panel; main feed unchanged; parent now shows "💬 1 reply". |
| User B opens the same parent thread | Sees User A's reply on load. |
| User B posts a reply | Appears in their own thread panel optimistically. User A does NOT see it live (deferred), but on reopening the thread will. |
| User A clicks the "💬 1 reply" badge | Thread panel opens (same as the hover icon). |
| User A clicks the Reply (↩) hover icon on User B's reply in the thread | Quote chip appears above the thread input; Enter sends with `quotedParentMessageId`; the new reply renders with the QuotedBlock inside its bubble. |
| User A clicks the Reply (↩) hover icon on a top-level message in the main feed | Quote chip appears above the **main** input; sending publishes with `quotedParentMessageId`. |
| User A clicks the Edit (✎) icon on their own message | Inline edit input appears; Enter publishes msg.edit; row updates with "(edited)". |
| User A clicks the Delete (🗑) icon | Confirm dialog appears; Confirm publishes msg.delete; row shows "[message deleted]". |
| User A switches rooms while the thread is open | Thread panel closes. Staged quote in main input clears. |
| User A logs out and back in | Thread state is empty. |
| User A presses Ctrl-F while a thread is open | Thread closes, InRoomSearch opens. |
| User A presses Esc inside the thread panel (focus on ✕ or the list) | Thread closes. |

If anything fails, file the bug and fix it before declaring complete. Each fix gets its own commit.

- [ ] **Step 5: Final commit summary (squash optional)**

If desired, before opening the PR, leave commits as-is or `git rebase -i` the chapter-by-chapter commits into chapter-sized squashes. Do NOT force-push without confirming branch hygiene.

---

## Self-review & verification items

The implementer should verify these in the Red phase (each was flagged as an assumption during spec writing):

1. **`msg.edit` and `msg.delete` payload shapes** (`history-service/internal/service/messages.go:322, 401`) — make sure the field names in `RoomMessageArea`'s edit/delete handlers match the handler-side structs.
2. **`message.quotedParentMessage` `deleted` flag** — if the snapshot doesn't expose a `deleted` field, `QuotedBlock` falls back to rendering content as-is (no special placeholder).
3. **`broadcast-worker` DM vs channel routing** (`publishDMEvents` vs `publishChannelEvent`) — confirm the client-side thread filter behaves identically for DM and channel rooms; the filter is on `message.threadParentMessageId`, not on room type, so it should be uniform.

## Spec ↔ plan coverage check

The plan covers every spec section as follows:

| Spec section | Implementing tasks |
|---|---|
| Layout (AppHeader / Sidebar / MainApp / slim ChatPage) | Ch.2 tasks 2.1–2.7 |
| `threadEventsReducer.js` | Ch.6 task 6.1 |
| `ThreadEventsContext.jsx` | Ch.6 task 6.2 + Ch.8 task 8.3 (cross-dispatch) |
| Main reducer filter on thread replies | Ch.8 task 8.1 |
| Tcount bump on own reply | Ch.8 task 8.2 + 8.3 |
| Shared utilities (messageBuffer) | Ch.1 task 1.1 + 1.2 |
| Subject builders (msgThread, msgEdit, msgDelete) | Ch.1 task 1.3 |
| QuotedBlock (chip + bubble + deleted) | Ch.3 task 3.1 + Ch.5 |
| MessageActions (Thread + Reply + Edit + Delete) | Ch.3 task 3.2 + Ch.4 task 4.3 |
| MessageRow (incl. inline edit, deleted, failed UI, reply badge) | Ch.3 task 3.3 + Ch.4 task 4.4 + Ch.7 task 7.4 + Ch.8 task 8.4 |
| MessageList (incl. parent context override, retry/dismiss forwarding) | Ch.3 task 3.4 + Ch.7 task 7.3 |
| MessageInputForm | Ch.3 task 3.5 |
| RoomMessageArea / RoomMessageInput | Ch.3 tasks 3.6 + 3.7 + Ch.4 task 4.6 |
| ThreadMessageArea / ThreadMessageInput | Ch.7 tasks 7.1 + 7.2 |
| ThreadRightBar | Ch.7 task 7.5 |
| Mount in MainApp + Thread icon wiring | Ch.7 task 7.6 |
| Loading / error / empty states | Ch.3 task 3.4 (placeholders) + Ch.7 task 7.2 (thread-specific text) |
| Failed-reply UX | Ch.7 task 7.4 |
| Quote-reply staging (main feed) | Ch.5 task 5.1 |
| Quote-reply staging (thread) | Ch.7 task 7.1 + 7.5 |
| Quote-reply boundary (gatekeeper rule) | Routing rule in `MessageActions` (Ch.4 hides Reply on parent-in-its-own-thread) |
| Click-to-jump-to-original | Ch.3 task 3.3 + Ch.5 task 5.3 |
| Deleted-quoted-message placeholder | Ch.3 task 3.1 |
| Edit RPC + inline replace | Ch.4 tasks 4.4 + 4.6 |
| Delete RPC + confirm dialog | Ch.4 tasks 4.5 + 4.6 |
| InRoomSearch ↔ ThreadRightBar mutual exclusion | Ch.9 task 9.1 |
| Esc-to-close-thread | Ch.9 task 9.2 |
| Focus management | Ch.9 task 9.3 |
| Logout reset | Ch.6 task 6.2 + Ch.9 task 9.4 |
| Room switch closes thread | Ch.8 task 8.5 |
| CSS — new selectors | Ch.2 task 2.7 + Ch.3 task 3.8 + Ch.7 task 7.7 + Ch.8 task 8.4 |
| Reply-count badge | Ch.8 task 8.4 |

Anything explicitly **non-goal** in the spec (tshow checkbox, live thread broadcasts from other users, last-reply-at timestamp on the badge, thread mark-as-read, thread notifications, threads-tab) is **not** in the plan by design.

---

**Plan complete.** Next step is execution. Pick a mode:

- **Subagent-Driven (recommended)** — `superpowers:subagent-driven-development`. One subagent per task; review between tasks. Cleanest context isolation.
- **Inline Execution** — `superpowers:executing-plans`. Batch execution in this session with checkpoints.

Tell me which.
