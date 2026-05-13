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

*Chapter 3 follows — Message component refactor (`QuotedBlock`, `MessageActions`, `MessageRow`, `MessageList`, `MessageInputForm`, and the Room-context containers).*
