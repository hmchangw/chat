# Room Member Management Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add UI support in `chat-frontend/` for room member management — add/remove members, update roles, remove orgs, and self-leave — using the existing backend NATS endpoints.

**Architecture:** One tabbed `ManageMembersDialog` opened from a "Members" header button, plus a separate "Leave" header button. Four small sub-forms, each calling a single NATS request/reply via the existing `NatsContext.request` helper. No changes to `NatsContext`, `RoomEventsContext`, or the reducer; no new subscriptions.

**Tech Stack:** React 19, Vite, nats.ws, Vitest + @testing-library/react.

**Spec:** `docs/superpowers/specs/2026-04-21-room-member-management-frontend-design.md`

---

## Working Directory

All commands run from `/home/user/chat/chat-frontend` unless stated. Tests are run with the repo's existing test runner:

```bash
cd chat-frontend
npx vitest run <path-to-test-file>   # run a single file
npm test                              # run all frontend tests
```

Commits are created from the repo root (`/home/user/chat`).

---

## Task 1: Add subject builders for member management

**Why:** Every form below calls `request(subject, payload)` with one of these three subjects. Shipping the builders first — covered by unit tests — unlocks all downstream tasks and mirrors the Go `pkg/subject/subject.go` exactly.

**Files:**
- Modify: `chat-frontend/src/lib/subjects.js`
- Modify: `chat-frontend/src/lib/subjects.test.js`

- [ ] **Step 1.1: Write the failing tests**

Append the following cases to `chat-frontend/src/lib/subjects.test.js`. Update the import on line 2 to include the three new builders:

```js
import { userRoomEvent, roomEvent, memberAdd, memberRemove, memberRoleUpdate } from './subjects'
```

Add these `it` blocks inside the existing `describe('subjects', ...)`:

```js
it('memberAdd builds the add-member request subject', () => {
  expect(memberAdd('alice', 'r1', 'site-A')).toBe(
    'chat.user.alice.request.room.r1.site-A.member.add'
  )
})

it('memberRemove builds the remove-member request subject', () => {
  expect(memberRemove('alice', 'r1', 'site-A')).toBe(
    'chat.user.alice.request.room.r1.site-A.member.remove'
  )
})

it('memberRoleUpdate builds the role-update request subject', () => {
  expect(memberRoleUpdate('alice', 'r1', 'site-A')).toBe(
    'chat.user.alice.request.room.r1.site-A.member.role-update'
  )
})
```

- [ ] **Step 1.2: Run the tests and confirm they FAIL**

Run: `cd chat-frontend && npx vitest run src/lib/subjects.test.js`

Expected output contains `SyntaxError` or `ReferenceError` for `memberAdd` / `memberRemove` / `memberRoleUpdate` — the builders don't exist yet.

- [ ] **Step 1.3: Add the three builders**

Append to `chat-frontend/src/lib/subjects.js` (keep existing exports unchanged):

```js
export function memberAdd(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.member.add`
}

export function memberRemove(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.member.remove`
}

export function memberRoleUpdate(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.member.role-update`
}
```

- [ ] **Step 1.4: Run tests and confirm they PASS**

Run: `cd chat-frontend && npx vitest run src/lib/subjects.test.js`

Expected: all five cases pass (the two pre-existing + the three new ones).

- [ ] **Step 1.5: Commit**

```bash
git add chat-frontend/src/lib/subjects.js chat-frontend/src/lib/subjects.test.js
git commit -m "feat(frontend): add member management subject builders"
```

---

## Task 2: Build `AddMembersForm`

**Why:** First sub-form. Introduces the form pattern (local state for inputs + loading/error/success, call `request`, show inline feedback) that every other form reuses. Also introduces the `.dialog-success` CSS class reused by all four forms.

**Files:**
- Create: `chat-frontend/src/components/manageMembers/AddMembersForm.jsx`
- Create: `chat-frontend/src/components/manageMembers/AddMembersForm.test.jsx`
- Modify: `chat-frontend/src/styles/index.css`

- [ ] **Step 2.1: Create the `manageMembers` directory**

```bash
mkdir -p chat-frontend/src/components/manageMembers
```

- [ ] **Step 2.2: Write the failing test file**

Create `chat-frontend/src/components/manageMembers/AddMembersForm.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddMembersForm from './AddMembersForm'

vi.mock('../../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../../context/NatsContext'

const room = { id: 'r1', siteId: 'site-A', name: 'general' }

function setup(overrides = {}) {
  const request = vi.fn().mockResolvedValue({ status: 'accepted' })
  useNats.mockReturnValue({
    user: { account: 'alice' },
    request,
    ...overrides,
  })
  render(<AddMembersForm room={room} />)
  return { request }
}

describe('AddMembersForm', () => {
  beforeEach(() => {
    useNats.mockReset()
  })

  it('disables submit when all inputs are empty', () => {
    setup()
    expect(screen.getByRole('button', { name: /^Add$/ })).toBeDisabled()
  })

  it('submits parsed lists with mode=all by default', async () => {
    const { request } = setup()
    fireEvent.change(screen.getByLabelText(/Accounts/i), { target: { value: 'bob, charlie' } })
    fireEvent.change(screen.getByLabelText(/Orgs/i), { target: { value: 'eng' } })
    fireEvent.change(screen.getByLabelText(/Channels/i), { target: { value: 'r-x' } })
    fireEvent.click(screen.getByRole('button', { name: /^Add$/ }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.site-A.member.add',
      {
        roomId: 'r1',
        users: ['bob', 'charlie'],
        orgs: ['eng'],
        channels: ['r-x'],
        history: { mode: 'all' },
      }
    )
  })

  it('sends mode=none when share-history is unchecked', async () => {
    const { request } = setup()
    fireEvent.change(screen.getByLabelText(/Accounts/i), { target: { value: 'bob' } })
    fireEvent.click(screen.getByLabelText(/Share room history/i))
    fireEvent.click(screen.getByRole('button', { name: /^Add$/ }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request.mock.calls[0][1].history).toEqual({ mode: 'none' })
  })

  it('shows error banner on request failure', async () => {
    const request = vi.fn().mockRejectedValue(new Error('only owners can add members'))
    setup({ request })
    fireEvent.change(screen.getByLabelText(/Accounts/i), { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: /^Add$/ }))
    expect(await screen.findByText(/only owners/)).toBeInTheDocument()
  })

  it('clears inputs and shows Accepted on success', async () => {
    setup()
    const accounts = screen.getByLabelText(/Accounts/i)
    fireEvent.change(accounts, { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: /^Add$/ }))
    expect(await screen.findByText('Accepted')).toBeInTheDocument()
    expect(accounts.value).toBe('')
  })
})
```

- [ ] **Step 2.3: Run the test and confirm it FAILS**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/AddMembersForm.test.jsx`

Expected: error resolving `./AddMembersForm` — the component file doesn't exist yet.

- [ ] **Step 2.4: Implement `AddMembersForm`**

Create `chat-frontend/src/components/manageMembers/AddMembersForm.jsx`:

```jsx
import { useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberAdd } from '../../lib/subjects'

function parseList(input) {
  return input
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

export default function AddMembersForm({ room }) {
  const { user, request } = useNats()
  const [accounts, setAccounts] = useState('')
  const [orgs, setOrgs] = useState('')
  const [channels, setChannels] = useState('')
  const [shareHistory, setShareHistory] = useState(true)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)

  const users = parseList(accounts)
  const orgList = parseList(orgs)
  const channelList = parseList(channels)
  const canSubmit = users.length + orgList.length + channelList.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    setSuccess(false)
    try {
      await request(memberAdd(user.account, room.id, room.siteId), {
        roomId: room.id,
        users,
        orgs: orgList,
        channels: channelList,
        history: { mode: shareHistory ? 'all' : 'none' },
      })
      setAccounts('')
      setOrgs('')
      setChannels('')
      setSuccess(true)
      setTimeout(() => setSuccess(false), 3000)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleSubmit}>
      <label htmlFor="add-accounts">Accounts (comma-separated)</label>
      <input
        id="add-accounts"
        type="text"
        value={accounts}
        onChange={(e) => setAccounts(e.target.value)}
        placeholder="e.g. bob, charlie"
        disabled={loading}
      />

      <label htmlFor="add-orgs">Orgs (comma-separated)</label>
      <input
        id="add-orgs"
        type="text"
        value={orgs}
        onChange={(e) => setOrgs(e.target.value)}
        placeholder="e.g. eng-frontend"
        disabled={loading}
      />

      <label htmlFor="add-channels">Channels (comma-separated room IDs)</label>
      <input
        id="add-channels"
        type="text"
        value={channels}
        onChange={(e) => setChannels(e.target.value)}
        placeholder="e.g. r-existing"
        disabled={loading}
      />

      <label className="dialog-checkbox">
        <input
          type="checkbox"
          checked={shareHistory}
          onChange={(e) => setShareHistory(e.target.checked)}
          disabled={loading}
        />{' '}
        Share room history with new members
      </label>

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Adding...' : 'Add'}
        </button>
      </div>
    </form>
  )
}
```

- [ ] **Step 2.5: Add the `.dialog-success` and `.dialog-checkbox` CSS**

Append to `chat-frontend/src/styles/index.css` after the existing `.dialog-error` block:

```css
.dialog-success {
  padding: 0.5rem;
  margin-bottom: 1rem;
  background: rgba(67, 181, 129, 0.12);
  color: #43b581;
  border-radius: 4px;
  font-size: 13px;
}

.dialog-checkbox {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 1rem;
  color: #b5bac1;
  font-size: 13px;
  cursor: pointer;
}

.dialog-checkbox input[type='checkbox'] {
  width: auto;
  margin: 0;
  padding: 0;
}
```

- [ ] **Step 2.6: Run the test and confirm it PASSES**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/AddMembersForm.test.jsx`

Expected: all 5 cases pass.

- [ ] **Step 2.7: Commit**

```bash
git add chat-frontend/src/components/manageMembers/AddMembersForm.jsx \
        chat-frontend/src/components/manageMembers/AddMembersForm.test.jsx \
        chat-frontend/src/styles/index.css
git commit -m "feat(frontend): add AddMembersForm component"
```

---

## Task 3: Build `RemoveMemberForm`

**Why:** Second sub-form. Re-uses the form pattern from Task 2 but sends the remove-individual payload. No self-leave path here — that lives in `LeaveRoomButton` (Task 7).

**Files:**
- Create: `chat-frontend/src/components/manageMembers/RemoveMemberForm.jsx`
- Create: `chat-frontend/src/components/manageMembers/RemoveMemberForm.test.jsx`

- [ ] **Step 3.1: Write the failing test file**

Create `chat-frontend/src/components/manageMembers/RemoveMemberForm.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import RemoveMemberForm from './RemoveMemberForm'

vi.mock('../../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../../context/NatsContext'

const room = { id: 'r1', siteId: 'site-A', name: 'general' }

function setup(overrides = {}) {
  const request = vi.fn().mockResolvedValue({ status: 'accepted' })
  useNats.mockReturnValue({
    user: { account: 'alice' },
    request,
    ...overrides,
  })
  render(<RemoveMemberForm room={room} />)
  return { request }
}

describe('RemoveMemberForm', () => {
  beforeEach(() => {
    useNats.mockReset()
  })

  it('disables submit when account input is empty', () => {
    setup()
    expect(screen.getByRole('button', { name: /^Remove$/ })).toBeDisabled()
  })

  it('submits {roomId, account} with the correct subject', async () => {
    const { request } = setup()
    fireEvent.change(screen.getByLabelText(/Account/i), { target: { value: '  bob  ' } })
    fireEvent.click(screen.getByRole('button', { name: /^Remove$/ }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.site-A.member.remove',
      { roomId: 'r1', account: 'bob' }
    )
  })

  it('shows the server error in a banner on rejection', async () => {
    const request = vi.fn().mockRejectedValue(new Error('only owners can remove members'))
    setup({ request })
    fireEvent.change(screen.getByLabelText(/Account/i), { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: /^Remove$/ }))
    expect(await screen.findByText(/only owners/)).toBeInTheDocument()
  })

  it('clears the input and shows Accepted on success', async () => {
    setup()
    const input = screen.getByLabelText(/Account/i)
    fireEvent.change(input, { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: /^Remove$/ }))
    expect(await screen.findByText('Accepted')).toBeInTheDocument()
    expect(input.value).toBe('')
  })
})
```

- [ ] **Step 3.2: Run the test and confirm it FAILS**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/RemoveMemberForm.test.jsx`

Expected: error resolving `./RemoveMemberForm`.

- [ ] **Step 3.3: Implement `RemoveMemberForm`**

Create `chat-frontend/src/components/manageMembers/RemoveMemberForm.jsx`:

```jsx
import { useEffect, useRef, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberRemove } from '../../lib/subjects'

export default function RemoveMemberForm({ room }) {
  const { user, request } = useNats()
  const [account, setAccount] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)
  const successTimer = useRef(null)

  useEffect(() => {
    return () => {
      if (successTimer.current) clearTimeout(successTimer.current)
    }
  }, [])

  const trimmed = account.trim()
  const canSubmit = trimmed.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    setSuccess(false)
    try {
      await request(memberRemove(user.account, room.id, room.siteId), {
        roomId: room.id,
        account: trimmed,
      })
      setAccount('')
      setSuccess(true)
      if (successTimer.current) clearTimeout(successTimer.current)
      successTimer.current = setTimeout(() => setSuccess(false), 3000)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleSubmit}>
      <label htmlFor="remove-account">Account</label>
      <input
        id="remove-account"
        type="text"
        value={account}
        onChange={(e) => setAccount(e.target.value)}
        placeholder="e.g. bob"
        disabled={loading}
      />

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Removing...' : 'Remove'}
        </button>
      </div>
    </form>
  )
}
```

- [ ] **Step 3.4: Run the test and confirm it PASSES**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/RemoveMemberForm.test.jsx`

Expected: all 4 cases pass.

- [ ] **Step 3.5: Commit**

```bash
git add chat-frontend/src/components/manageMembers/RemoveMemberForm.jsx \
        chat-frontend/src/components/manageMembers/RemoveMemberForm.test.jsx
git commit -m "feat(frontend): add RemoveMemberForm component"
```

---

## Task 4: Build `RoleUpdateForm`

**Why:** Third sub-form. Introduces a `<select>` input alongside the account text input.

**Files:**
- Create: `chat-frontend/src/components/manageMembers/RoleUpdateForm.jsx`
- Create: `chat-frontend/src/components/manageMembers/RoleUpdateForm.test.jsx`

- [ ] **Step 4.1: Write the failing test file**

Create `chat-frontend/src/components/manageMembers/RoleUpdateForm.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import RoleUpdateForm from './RoleUpdateForm'

vi.mock('../../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../../context/NatsContext'

const room = { id: 'r1', siteId: 'site-A', name: 'general' }

function setup(overrides = {}) {
  const request = vi.fn().mockResolvedValue({ status: 'accepted' })
  useNats.mockReturnValue({
    user: { account: 'alice' },
    request,
    ...overrides,
  })
  render(<RoleUpdateForm room={room} />)
  return { request }
}

describe('RoleUpdateForm', () => {
  beforeEach(() => {
    useNats.mockReset()
  })

  it('disables submit when account input is empty', () => {
    setup()
    expect(screen.getByRole('button', { name: /Update Role/i })).toBeDisabled()
  })

  it('submits newRole=owner by default', async () => {
    const { request } = setup()
    fireEvent.change(screen.getByLabelText(/Account/i), { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: /Update Role/i }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.site-A.member.role-update',
      { roomId: 'r1', account: 'bob', newRole: 'owner' }
    )
  })

  it('submits newRole=member when the select is changed', async () => {
    const { request } = setup()
    fireEvent.change(screen.getByLabelText(/Account/i), { target: { value: 'bob' } })
    fireEvent.change(screen.getByLabelText(/Role/i), { target: { value: 'member' } })
    fireEvent.click(screen.getByRole('button', { name: /Update Role/i }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request.mock.calls[0][1]).toEqual({ roomId: 'r1', account: 'bob', newRole: 'member' })
  })

  it('shows the server error in a banner on rejection', async () => {
    const request = vi.fn().mockRejectedValue(new Error('cannot demote: you are the last owner'))
    setup({ request })
    fireEvent.change(screen.getByLabelText(/Account/i), { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: /Update Role/i }))
    expect(await screen.findByText(/last owner/)).toBeInTheDocument()
  })

  it('clears the account input and shows Accepted on success', async () => {
    setup()
    const input = screen.getByLabelText(/Account/i)
    fireEvent.change(input, { target: { value: 'bob' } })
    fireEvent.click(screen.getByRole('button', { name: /Update Role/i }))
    expect(await screen.findByText('Accepted')).toBeInTheDocument()
    expect(input.value).toBe('')
  })
})
```

- [ ] **Step 4.2: Run the test and confirm it FAILS**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/RoleUpdateForm.test.jsx`

Expected: error resolving `./RoleUpdateForm`.

- [ ] **Step 4.3: Implement `RoleUpdateForm`**

Create `chat-frontend/src/components/manageMembers/RoleUpdateForm.jsx`:

```jsx
import { useEffect, useRef, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberRoleUpdate } from '../../lib/subjects'

export default function RoleUpdateForm({ room }) {
  const { user, request } = useNats()
  const [account, setAccount] = useState('')
  const [newRole, setNewRole] = useState('owner')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)
  const successTimer = useRef(null)

  useEffect(() => {
    return () => {
      if (successTimer.current) clearTimeout(successTimer.current)
    }
  }, [])

  const trimmed = account.trim()
  const canSubmit = trimmed.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    setSuccess(false)
    try {
      await request(memberRoleUpdate(user.account, room.id, room.siteId), {
        roomId: room.id,
        account: trimmed,
        newRole,
      })
      setAccount('')
      setSuccess(true)
      if (successTimer.current) clearTimeout(successTimer.current)
      successTimer.current = setTimeout(() => setSuccess(false), 3000)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleSubmit}>
      <label htmlFor="role-account">Account</label>
      <input
        id="role-account"
        type="text"
        value={account}
        onChange={(e) => setAccount(e.target.value)}
        placeholder="e.g. bob"
        disabled={loading}
      />

      <label htmlFor="role-newrole">Role</label>
      <select
        id="role-newrole"
        value={newRole}
        onChange={(e) => setNewRole(e.target.value)}
        disabled={loading}
      >
        <option value="owner">Owner</option>
        <option value="member">Member</option>
      </select>

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Updating...' : 'Update Role'}
        </button>
      </div>
    </form>
  )
}
```

- [ ] **Step 4.4: Run the test and confirm it PASSES**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/RoleUpdateForm.test.jsx`

Expected: all 5 cases pass.

- [ ] **Step 4.5: Commit**

```bash
git add chat-frontend/src/components/manageMembers/RoleUpdateForm.jsx \
        chat-frontend/src/components/manageMembers/RoleUpdateForm.test.jsx
git commit -m "feat(frontend): add RoleUpdateForm component"
```

---

## Task 5: Build `RemoveOrgForm`

**Why:** Fourth and final sub-form. Sends the org-removal variant of the remove-member subject.

**Files:**
- Create: `chat-frontend/src/components/manageMembers/RemoveOrgForm.jsx`
- Create: `chat-frontend/src/components/manageMembers/RemoveOrgForm.test.jsx`

- [ ] **Step 5.1: Write the failing test file**

Create `chat-frontend/src/components/manageMembers/RemoveOrgForm.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import RemoveOrgForm from './RemoveOrgForm'

vi.mock('../../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../../context/NatsContext'

const room = { id: 'r1', siteId: 'site-A', name: 'general' }

function setup(overrides = {}) {
  const request = vi.fn().mockResolvedValue({ status: 'accepted' })
  useNats.mockReturnValue({
    user: { account: 'alice' },
    request,
    ...overrides,
  })
  render(<RemoveOrgForm room={room} />)
  return { request }
}

describe('RemoveOrgForm', () => {
  beforeEach(() => {
    useNats.mockReset()
  })

  it('disables submit when org id is empty', () => {
    setup()
    expect(screen.getByRole('button', { name: /Remove Org/i })).toBeDisabled()
  })

  it('submits {roomId, orgId} with the correct subject', async () => {
    const { request } = setup()
    fireEvent.change(screen.getByLabelText(/Org ID/i), { target: { value: '  eng-frontend  ' } })
    fireEvent.click(screen.getByRole('button', { name: /Remove Org/i }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.site-A.member.remove',
      { roomId: 'r1', orgId: 'eng-frontend' }
    )
  })

  it('shows the server error in a banner on rejection', async () => {
    const request = vi.fn().mockRejectedValue(new Error('only owners can remove orgs'))
    setup({ request })
    fireEvent.change(screen.getByLabelText(/Org ID/i), { target: { value: 'eng' } })
    fireEvent.click(screen.getByRole('button', { name: /Remove Org/i }))
    expect(await screen.findByText(/only owners/)).toBeInTheDocument()
  })

  it('clears the input and shows Accepted on success', async () => {
    setup()
    const input = screen.getByLabelText(/Org ID/i)
    fireEvent.change(input, { target: { value: 'eng' } })
    fireEvent.click(screen.getByRole('button', { name: /Remove Org/i }))
    expect(await screen.findByText('Accepted')).toBeInTheDocument()
    expect(input.value).toBe('')
  })
})
```

- [ ] **Step 5.2: Run the test and confirm it FAILS**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/RemoveOrgForm.test.jsx`

Expected: error resolving `./RemoveOrgForm`.

- [ ] **Step 5.3: Implement `RemoveOrgForm`**

Create `chat-frontend/src/components/manageMembers/RemoveOrgForm.jsx`:

```jsx
import { useEffect, useRef, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberRemove } from '../../lib/subjects'

export default function RemoveOrgForm({ room }) {
  const { user, request } = useNats()
  const [orgId, setOrgId] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)
  const successTimer = useRef(null)

  useEffect(() => {
    return () => {
      if (successTimer.current) clearTimeout(successTimer.current)
    }
  }, [])

  const trimmed = orgId.trim()
  const canSubmit = trimmed.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    setSuccess(false)
    try {
      await request(memberRemove(user.account, room.id, room.siteId), {
        roomId: room.id,
        orgId: trimmed,
      })
      setOrgId('')
      setSuccess(true)
      if (successTimer.current) clearTimeout(successTimer.current)
      successTimer.current = setTimeout(() => setSuccess(false), 3000)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleSubmit}>
      <label htmlFor="removeorg-orgid">Org ID</label>
      <input
        id="removeorg-orgid"
        type="text"
        value={orgId}
        onChange={(e) => setOrgId(e.target.value)}
        placeholder="e.g. eng-frontend"
        disabled={loading}
      />

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Removing...' : 'Remove Org'}
        </button>
      </div>
    </form>
  )
}
```

- [ ] **Step 5.4: Run the test and confirm it PASSES**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers/RemoveOrgForm.test.jsx`

Expected: all 4 cases pass.

- [ ] **Step 5.5: Commit**

```bash
git add chat-frontend/src/components/manageMembers/RemoveOrgForm.jsx \
        chat-frontend/src/components/manageMembers/RemoveOrgForm.test.jsx
git commit -m "feat(frontend): add RemoveOrgForm component"
```

---

## Task 6: Build `ManageMembersDialog` shell with tabs

**Why:** Composes the four sub-forms into a single tabbed dialog, adds tab CSS, and provides a Close button.

**Files:**
- Create: `chat-frontend/src/components/ManageMembersDialog.jsx`
- Create: `chat-frontend/src/components/ManageMembersDialog.test.jsx`
- Modify: `chat-frontend/src/styles/index.css`

- [ ] **Step 6.1: Write the failing test file**

Create `chat-frontend/src/components/ManageMembersDialog.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ManageMembersDialog from './ManageMembersDialog'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

const room = { id: 'r1', siteId: 'site-A', name: 'general', type: 'group' }

beforeEach(() => {
  useNats.mockReset()
  useNats.mockReturnValue({
    user: { account: 'alice' },
    request: vi.fn().mockResolvedValue({ status: 'accepted' }),
  })
})

describe('ManageMembersDialog', () => {
  it('shows the Add tab by default', () => {
    render(<ManageMembersDialog room={room} onClose={vi.fn()} />)
    expect(screen.getByRole('tab', { name: /^Add$/ })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByLabelText(/Accounts \(comma-separated\)/i)).toBeInTheDocument()
  })

  it('switches tabs when a tab button is clicked', () => {
    render(<ManageMembersDialog room={room} onClose={vi.fn()} />)

    fireEvent.click(screen.getByRole('tab', { name: /^Remove$/ }))
    expect(screen.getByRole('tab', { name: /^Remove$/ })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByLabelText(/^Account$/i)).toBeInTheDocument()
    expect(screen.queryByLabelText(/Accounts \(comma-separated\)/i)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: /^Role$/ }))
    expect(screen.getByLabelText(/^Role$/i)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: /Remove Org/i }))
    expect(screen.getByLabelText(/Org ID/i)).toBeInTheDocument()
  })

  it('calls onClose when Close is clicked', () => {
    const onClose = vi.fn()
    render(<ManageMembersDialog room={room} onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: /Close/i }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when the overlay is clicked', () => {
    const onClose = vi.fn()
    const { container } = render(<ManageMembersDialog room={room} onClose={onClose} />)
    fireEvent.click(container.querySelector('.dialog-overlay'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('does not call onClose when the dialog body is clicked', () => {
    const onClose = vi.fn()
    const { container } = render(<ManageMembersDialog room={room} onClose={onClose} />)
    fireEvent.click(container.querySelector('.dialog'))
    expect(onClose).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 6.2: Run the test and confirm it FAILS**

Run: `cd chat-frontend && npx vitest run src/components/ManageMembersDialog.test.jsx`

Expected: error resolving `./ManageMembersDialog`.

- [ ] **Step 6.3: Implement `ManageMembersDialog`**

Create `chat-frontend/src/components/ManageMembersDialog.jsx`:

```jsx
import { useState } from 'react'
import AddMembersForm from './manageMembers/AddMembersForm'
import RemoveMemberForm from './manageMembers/RemoveMemberForm'
import RoleUpdateForm from './manageMembers/RoleUpdateForm'
import RemoveOrgForm from './manageMembers/RemoveOrgForm'

const TABS = [
  { id: 'add', label: 'Add', Form: AddMembersForm },
  { id: 'remove', label: 'Remove', Form: RemoveMemberForm },
  { id: 'role', label: 'Role', Form: RoleUpdateForm },
  { id: 'removeOrg', label: 'Remove Org', Form: RemoveOrgForm },
]

export default function ManageMembersDialog({ room, onClose }) {
  const [mode, setMode] = useState('add')
  const active = TABS.find((t) => t.id === mode)
  const ActiveForm = active.Form

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div className="dialog manage-members-dialog" onClick={(e) => e.stopPropagation()}>
        <h2>Manage Members — {room.name}</h2>

        <div className="manage-members-tabs" role="tablist">
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              role="tab"
              aria-selected={mode === t.id}
              className={`manage-members-tab${mode === t.id ? ' manage-members-tab-active' : ''}`}
              onClick={() => setMode(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>

        <ActiveForm room={room} />

        <div className="dialog-actions manage-members-footer">
          <button type="button" className="dialog-cancel" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
```

- [ ] **Step 6.4: Add tab CSS**

Append to `chat-frontend/src/styles/index.css`:

```css
/* Manage Members dialog */
.manage-members-dialog {
  width: 480px;
}

.manage-members-tabs {
  display: flex;
  gap: 0.25rem;
  margin-bottom: 1rem;
  border-bottom: 1px solid #3f4147;
}

.manage-members-tab {
  background: transparent;
  border: none;
  color: #b5bac1;
  padding: 0.5rem 0.75rem;
  font-size: 13px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}

.manage-members-tab:hover {
  color: #dbdee1;
}

.manage-members-tab-active {
  color: #f2f3f5;
  border-bottom-color: #5865f2;
}

.manage-members-footer {
  margin-top: 0.75rem;
}
```

- [ ] **Step 6.5: Run the test and confirm it PASSES**

Run: `cd chat-frontend && npx vitest run src/components/ManageMembersDialog.test.jsx`

Expected: all 5 cases pass.

- [ ] **Step 6.6: Run all existing form tests to confirm nothing broke**

Run: `cd chat-frontend && npx vitest run src/components/manageMembers`

Expected: all tests from Tasks 2-5 still pass (18 total cases).

- [ ] **Step 6.7: Commit**

```bash
git add chat-frontend/src/components/ManageMembersDialog.jsx \
        chat-frontend/src/components/ManageMembersDialog.test.jsx \
        chat-frontend/src/styles/index.css
git commit -m "feat(frontend): add ManageMembersDialog with Add/Remove/Role/Remove Org tabs"
```

---

## Task 7: Build `LeaveRoomButton`

**Why:** Self-leave lives outside the dialog. Renders a single button that confirms and submits the remove-member subject with the current user's own account.

**Files:**
- Create: `chat-frontend/src/components/LeaveRoomButton.jsx`
- Create: `chat-frontend/src/components/LeaveRoomButton.test.jsx`

- [ ] **Step 7.1: Write the failing test file**

Create `chat-frontend/src/components/LeaveRoomButton.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import LeaveRoomButton from './LeaveRoomButton'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

const groupRoom = { id: 'r1', siteId: 'site-A', name: 'general', type: 'group' }
const dmRoom = { id: 'r2', siteId: 'site-A', name: 'bob-dm', type: 'dm' }

function setup(room, overrides = {}) {
  const request = vi.fn().mockResolvedValue({ status: 'accepted' })
  useNats.mockReturnValue({
    user: { account: 'alice' },
    request,
    ...overrides,
  })
  return { request, ...render(<LeaveRoomButton room={room} />) }
}

describe('LeaveRoomButton', () => {
  beforeEach(() => {
    useNats.mockReset()
    vi.restoreAllMocks()
  })

  it('renders nothing when room type is dm', () => {
    const { container } = setup(dmRoom)
    expect(container.firstChild).toBeNull()
  })

  it('submits {roomId, account=self} after the user confirms', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { request } = setup(groupRoom)
    fireEvent.click(screen.getByRole('button', { name: /Leave/i }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.site-A.member.remove',
      { roomId: 'r1', account: 'alice' }
    )
  })

  it('does nothing when the user cancels the confirm', () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { request } = setup(groupRoom)
    fireEvent.click(screen.getByRole('button', { name: /Leave/i }))
    expect(request).not.toHaveBeenCalled()
  })

  it('alerts the user when the request fails', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})
    const request = vi.fn().mockRejectedValue(new Error('cannot leave: you are the last owner'))
    setup(groupRoom, { request })
    fireEvent.click(screen.getByRole('button', { name: /Leave/i }))
    await waitFor(() => expect(alertSpy).toHaveBeenCalledTimes(1))
    expect(alertSpy.mock.calls[0][0]).toMatch(/last owner/)
  })
})
```

- [ ] **Step 7.2: Run the test and confirm it FAILS**

Run: `cd chat-frontend && npx vitest run src/components/LeaveRoomButton.test.jsx`

Expected: error resolving `./LeaveRoomButton`.

- [ ] **Step 7.3: Implement `LeaveRoomButton`**

Create `chat-frontend/src/components/LeaveRoomButton.jsx`:

```jsx
import { useNats } from '../context/NatsContext'
import { memberRemove } from '../lib/subjects'

export default function LeaveRoomButton({ room }) {
  const { user, request } = useNats()

  if (!room || room.type !== 'group') return null

  const handleClick = async () => {
    if (!window.confirm(`Leave "${room.name}"?`)) return
    try {
      await request(memberRemove(user.account, room.id, room.siteId), {
        roomId: room.id,
        account: user.account,
      })
    } catch (err) {
      window.alert(`Failed to leave: ${err.message}`)
    }
  }

  return (
    <button type="button" className="chat-header-logout" onClick={handleClick}>
      Leave
    </button>
  )
}
```

- [ ] **Step 7.4: Run the test and confirm it PASSES**

Run: `cd chat-frontend && npx vitest run src/components/LeaveRoomButton.test.jsx`

Expected: all 4 cases pass.

- [ ] **Step 7.5: Commit**

```bash
git add chat-frontend/src/components/LeaveRoomButton.jsx \
        chat-frontend/src/components/LeaveRoomButton.test.jsx
git commit -m "feat(frontend): add LeaveRoomButton for self-leave"
```

---

## Task 8: Wire `ManageMembersDialog` and `LeaveRoomButton` into `ChatPage`

**Why:** Final integration. Adds a "Members" button + `LeaveRoomButton` to the chat header (only when a `group` room is selected) and mounts `ManageMembersDialog` when the Members button is clicked. Also closes the dialog when the selected room changes or disappears.

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`
- Create: `chat-frontend/src/pages/ChatPage.test.jsx`

- [ ] **Step 8.1: Write the failing test file**

Create `chat-frontend/src/pages/ChatPage.test.jsx`:

```jsx
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ChatPage from './ChatPage'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: vi.fn(),
  useRoomEvents: vi.fn(),
}))
vi.mock('../components/RoomList', () => ({
  default: ({ onSelectRoom }) => (
    <div data-testid="room-list">
      <button onClick={() => onSelectRoom({ id: 'r1', name: 'general', type: 'group', siteId: 'site-A' })}>
        pick-group
      </button>
      <button onClick={() => onSelectRoom({ id: 'r2', name: 'bob-dm', type: 'dm', siteId: 'site-A' })}>
        pick-dm
      </button>
    </div>
  ),
}))
vi.mock('../components/MessageArea', () => ({ default: () => <div data-testid="message-area" /> }))
vi.mock('../components/MessageInput', () => ({ default: () => <div data-testid="message-input" /> }))
vi.mock('../components/CreateRoomDialog', () => ({ default: () => null }))

import { useNats } from '../context/NatsContext'
import { useRoomSummaries } from '../context/RoomEventsContext'

beforeEach(() => {
  useNats.mockReset()
  useRoomSummaries.mockReset()
  useNats.mockReturnValue({
    user: { account: 'alice', siteId: 'site-A' },
    request: vi.fn().mockResolvedValue({ status: 'accepted' }),
    disconnect: vi.fn(),
  })
  useRoomSummaries.mockReturnValue({
    summaries: [
      { id: 'r1', name: 'general', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: null, unreadCount: 0, hasMention: false, mentionAll: false },
      { id: 'r2', name: 'bob-dm', type: 'dm', siteId: 'site-A', userCount: 2, lastMsgAt: null, unreadCount: 0, hasMention: false, mentionAll: false },
    ],
    setActiveRoom: vi.fn(),
    error: null,
  })
})

describe('ChatPage header buttons', () => {
  it('hides Members and Leave when no room is selected', () => {
    render(<ChatPage />)
    expect(screen.queryByRole('button', { name: /^Members$/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Leave$/ })).not.toBeInTheDocument()
  })

  it('hides Members and Leave on a DM room', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-dm'))
    expect(screen.queryByRole('button', { name: /^Members$/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Leave$/ })).not.toBeInTheDocument()
  })

  it('shows Members and Leave on a group room', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-group'))
    expect(screen.getByRole('button', { name: /^Members$/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^Leave$/ })).toBeInTheDocument()
  })

  it('opens ManageMembersDialog when Members is clicked', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-group'))
    expect(screen.queryByRole('heading', { name: /Manage Members/i })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^Members$/ }))
    expect(screen.getByRole('heading', { name: /Manage Members — general/i })).toBeInTheDocument()
  })

  it('closes ManageMembersDialog when Close is clicked', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-group'))
    fireEvent.click(screen.getByRole('button', { name: /^Members$/ }))
    fireEvent.click(screen.getByRole('button', { name: /^Close$/ }))
    expect(screen.queryByRole('heading', { name: /Manage Members/i })).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 8.2: Run the test and confirm it FAILS**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx`

Expected: the first three cases fail — the header does not yet render a Members button for group rooms, and the Members/Leave buttons don't exist.

- [ ] **Step 8.3: Modify `ChatPage.jsx`**

Replace the full contents of `chat-frontend/src/pages/ChatPage.jsx` with:

```jsx
import { useEffect, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { useRoomSummaries } from '../context/RoomEventsContext'
import RoomList from '../components/RoomList'
import MessageArea from '../components/MessageArea'
import MessageInput from '../components/MessageInput'
import CreateRoomDialog from '../components/CreateRoomDialog'
import ManageMembersDialog from '../components/ManageMembersDialog'
import LeaveRoomButton from '../components/LeaveRoomButton'

export default function ChatPage() {
  const { user, disconnect } = useNats()
  const { summaries, setActiveRoom } = useRoomSummaries()
  const [selectedRoom, setSelectedRoom] = useState(null)
  const [showCreateRoom, setShowCreateRoom] = useState(false)
  const [showMembers, setShowMembers] = useState(false)

  // Clear selection and any open member dialog if the selected room disappears from summaries
  useEffect(() => {
    if (selectedRoom && !summaries.some((r) => r.id === selectedRoom.id)) {
      setSelectedRoom(null)
      setActiveRoom(null)
      setShowMembers(false)
    }
  }, [summaries, selectedRoom, setActiveRoom])

  const handleSelectRoom = (room) => {
    setSelectedRoom(room)
    setActiveRoom(room?.id ?? null)
    setShowMembers(false)
  }

  const isGroup = selectedRoom?.type === 'group'

  return (
    <div className="chat-layout">
      <div className="chat-header">
        <span className="chat-header-title">Chat</span>
        {isGroup && (
          <>
            <button
              type="button"
              className="chat-header-logout"
              onClick={() => setShowMembers(true)}
            >
              Members
            </button>
            <LeaveRoomButton room={selectedRoom} />
          </>
        )}
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
      {showMembers && selectedRoom && (
        <ManageMembersDialog
          room={selectedRoom}
          onClose={() => setShowMembers(false)}
        />
      )}
    </div>
  )
}
```

- [ ] **Step 8.4: Run the test and confirm it PASSES**

Run: `cd chat-frontend && npx vitest run src/pages/ChatPage.test.jsx`

Expected: all 5 cases pass.

- [ ] **Step 8.5: Run the full frontend test suite to confirm nothing regressed**

Run: `cd chat-frontend && npm test`

Expected: all tests pass — including existing `RoomList.test.jsx`, `MessageArea.test.jsx`, `RoomEventsContext.test.jsx`, `subjects.test.js`, `roomEventsReducer.test.js`, and the new ones from Tasks 1-8.

- [ ] **Step 8.6: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "feat(frontend): wire Members + Leave buttons into ChatPage header"
```

---

## Task 9: Manual smoke verification

**Why:** Unit tests verify component behaviour in isolation. This task confirms the feature actually works end-to-end against the running backend before calling the work done.

**Files:** none modified. This is a verification task.

- [ ] **Step 9.1: Start the local dev stack**

From the repo root:

```bash
cd docker-local
docker compose up -d
```

Wait until NATS and all backend services are healthy (check `docker compose ps`).

- [ ] **Step 9.2: Start the frontend dev server**

```bash
cd chat-frontend
npm run dev
```

Open the URL printed by Vite (usually `http://localhost:5173`).

- [ ] **Step 9.3: Log in as two test users in two browser windows**

- Window A: account `alice`, site `site-A`.
- Window B: account `bob`, site `site-A`.

- [ ] **Step 9.4: Exercise Add**

In Window A: create a group room `smoke-members` with member `bob`. Click "Members" → Add tab. Enter `charlie` in Accounts, click Add. Confirm the green "Accepted" flash appears and inputs clear. In Window B (Bob) the room's `userCount` in the sidebar should update to 3 via the existing metadata event.

- [ ] **Step 9.5: Exercise Role (promote + demote)**

Switch to the Role tab in Window A. Enter `bob`, select Owner, click Update Role → expect Accepted. Repeat with Role=Member to demote.

Expected negative case: demote `alice` (self) while she is the last owner → error banner `"cannot demote: you are the last owner"`.

- [ ] **Step 9.6: Exercise Remove**

Remove tab in Window A. Enter `charlie`, click Remove → expect Accepted. The room's `userCount` should drop back to 2.

- [ ] **Step 9.7: Exercise Remove Org**

Add an org to the room via the Add tab (use any org ID that resolves to users in your local data set), confirm the count rises, then use the Remove Org tab to remove it and confirm the count drops.

- [ ] **Step 9.8: Exercise self-leave**

In Window B (Bob): click Leave → confirm the dialog → room disappears from Bob's sidebar, Alice sees `userCount` drop to 1.

Expected negative case: leaving a room where you are the last owner → alert `"Failed to leave: cannot leave: you are the last owner"` (or the equivalent server error).

- [ ] **Step 9.9: Verify DM hides Members and Leave**

Create a DM with Bob. Select the DM — the header should show only Logout (no Members, no Leave button).

- [ ] **Step 9.10: Verify errors surface**

Use any invalid input (e.g. add member while you're not an owner of a restricted room, or remove a non-existent account) and confirm the red `.dialog-error` banner renders the server's message.

- [ ] **Step 9.11: Final commit checkpoint**

No code changes expected here. Confirm `git status` is clean and the branch contains the 8 feature commits plus the spec commit.

```bash
cd /home/user/chat
git status
git log --oneline -10
```

Expected `git log` tail (order from Task 1 onward):

```
feat(frontend): wire Members + Leave buttons into ChatPage header
feat(frontend): add LeaveRoomButton for self-leave
feat(frontend): add ManageMembersDialog with Add/Remove/Role/Remove Org tabs
feat(frontend): add RemoveOrgForm component
feat(frontend): add RoleUpdateForm component
feat(frontend): add RemoveMemberForm component
feat(frontend): add AddMembersForm component
feat(frontend): add member management subject builders
docs: add frontend room member management design spec
```

---

## Self-Review Notes

**Spec coverage check (after plan is written):**

| Spec section | Covered by |
|--------------|-----------|
| Subject builders `memberAdd` / `memberRemove` / `memberRoleUpdate` | Task 1 |
| `ManageMembersDialog` tabs + Close | Task 6 |
| `AddMembersForm` with history toggle | Task 2 |
| `RemoveMemberForm` | Task 3 |
| `RoleUpdateForm` | Task 4 |
| `RemoveOrgForm` | Task 5 |
| `LeaveRoomButton` (DM hidden, confirm, alert on error) | Task 7 |
| `ChatPage` header wiring + visibility rules | Task 8 |
| `.dialog-success`, `.dialog-checkbox`, `.manage-members-*` CSS | Tasks 2 and 6 |
| `ChatPage.test.jsx` coverage for group vs DM | Task 8 |
| No new `NatsContext` / `RoomEventsContext` subscriptions | Confirmed by tasks never editing those files |

**Architectural discipline:**
- No task edits `NatsContext.jsx` or `RoomEventsContext.jsx`.
- No task subscribes to `chat.room.{roomID}.event.member` (per spec).
- Each task produces a single commit. Eight feature commits + spec commit = branch history is linear and revertable per-task.
