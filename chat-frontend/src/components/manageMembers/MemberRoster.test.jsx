import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import MemberRoster from './MemberRoster'

vi.mock('../../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../../context/NatsContext'

const room = { id: 'r1', siteId: 'site-A', name: 'general' }

const baseMembers = [
  { id: 'rm1', rid: 'r1', member: { id: 'u-alice', type: 'individual', account: 'alice', engName: 'Alice A', isOwner: true } },
  { id: 'rm2', rid: 'r1', member: { id: 'u-bob', type: 'individual', account: 'bob', engName: 'Bob B', isOwner: false } },
  { id: 'rm3', rid: 'r1', member: { id: 'org-eng', type: 'org', sectName: 'Engineering', memberCount: 42 } },
]

function setupContext(overrides = {}) {
  const request = vi.fn().mockResolvedValue({ members: baseMembers })
  const requestWithAsyncResult = vi.fn().mockResolvedValue({ sync: { status: 'accepted' }, async: { status: 'ok' } })
  useNats.mockReturnValue({
    user: { account: 'alice', siteId: 'site-A' },
    request,
    requestWithAsyncResult,
    ...overrides,
  })
  return { request, requestWithAsyncResult }
}

describe('MemberRoster', () => {
  beforeEach(() => useNats.mockReset())

  it('does not refetch when the room prop is a new object reference but same id+siteId', async () => {
    const { request } = setupContext()
    const { rerender } = render(<MemberRoster room={{ id: 'r1', siteId: 'site-A', name: 'general' }} />)
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    rerender(<MemberRoster room={{ id: 'r1', siteId: 'site-A', name: 'general' }} />)
    // Empty act flushes any pending effects + microtasks deterministically.
    // If the rerender mistakenly re-fires fetchMembers, it would queue a new
    // call inside this flush; since deps are now `room.id` + `room.siteId`
    // (not the room object), the count must stay at 1.
    await act(async () => {})
    expect(request).toHaveBeenCalledTimes(1)
  })

  it('calls member.list with enrich on mount', async () => {
    const { request } = setupContext()
    render(<MemberRoster room={room} />)
    await waitFor(() =>
      expect(request).toHaveBeenCalledWith(
        'chat.user.alice.request.room.r1.site-A.member.list',
        { enrich: true }
      )
    )
  })

  it('renders individuals and orgs into separate sections', async () => {
    setupContext()
    render(<MemberRoster room={room} />)
    expect(await screen.findByText('Alice A')).toBeInTheDocument()
    expect(screen.getByText('Bob B')).toBeInTheDocument()
    expect(screen.getByText('Engineering')).toBeInTheDocument()
    // Owner badge on alice; not on bob.
    expect(screen.getByText(/Alice A/).closest('li')).toHaveTextContent(/owner/i)
    expect(screen.getByText(/Bob B/).closest('li')).not.toHaveTextContent(/owner/i)
  })

  it('shows a loading indicator before the list resolves', async () => {
    let resolveIt
    const request = vi.fn().mockImplementation(() => new Promise((r) => { resolveIt = r }))
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request,
      requestWithAsyncResult: vi.fn(),
    })
    render(<MemberRoster room={room} />)
    expect(screen.getByText(/loading members/i)).toBeInTheDocument()
    resolveIt({ members: [] })
    await waitFor(() => expect(screen.queryByText(/loading members/i)).not.toBeInTheDocument())
  })

  it('renders an error banner when member.list fails', async () => {
    const request = vi.fn().mockRejectedValue(new Error('not a room member'))
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request,
      requestWithAsyncResult: vi.fn(),
    })
    render(<MemberRoster room={room} />)
    expect(await screen.findByText(/not a room member/i)).toBeInTheDocument()
  })

  it('Promote on a non-owner member sends member.role-update newRole=owner', async () => {
    const { requestWithAsyncResult } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')
    fireEvent.click(screen.getByRole('button', { name: /Promote bob/i }))
    await waitFor(() =>
      expect(requestWithAsyncResult).toHaveBeenCalledWith(
        'chat.user.alice.request.room.r1.site-A.member.role-update',
        { roomId: 'r1', account: 'bob', newRole: 'owner' }
      )
    )
  })

  it('Demote on an owner sends member.role-update newRole=member', async () => {
    const { requestWithAsyncResult } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Alice A')
    fireEvent.click(screen.getByRole('button', { name: /Demote alice/i }))
    await waitFor(() =>
      expect(requestWithAsyncResult).toHaveBeenCalledWith(
        'chat.user.alice.request.room.r1.site-A.member.role-update',
        { roomId: 'r1', account: 'alice', newRole: 'member' }
      )
    )
  })

  it('Remove on an individual sends member.remove with account', async () => {
    const { requestWithAsyncResult } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')
    fireEvent.click(screen.getByRole('button', { name: /Remove bob/i }))
    await waitFor(() =>
      expect(requestWithAsyncResult).toHaveBeenCalledWith(
        'chat.user.alice.request.room.r1.site-A.member.remove',
        { roomId: 'r1', account: 'bob' }
      )
    )
  })

  it('Remove on an org sends member.remove with orgId', async () => {
    const { requestWithAsyncResult } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Engineering')
    fireEvent.click(screen.getByRole('button', { name: /Remove org-eng/i }))
    await waitFor(() =>
      expect(requestWithAsyncResult).toHaveBeenCalledWith(
        'chat.user.alice.request.room.r1.site-A.member.remove',
        { roomId: 'r1', orgId: 'org-eng' }
      )
    )
  })

  it('refetches the roster after a successful action', async () => {
    const { request } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')
    expect(request).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: /Remove bob/i }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
  })

  it('isolates the busy state to the row whose action is in flight (no suffix collisions)', async () => {
    // Regression: previous predicate was `busyKey?.endsWith(`:${account}`)`
    // which (a) disabled all three buttons on the active row at once and
    // (b) cross-disabled `bob` and `dynamicbob` because both end in `:bob`.
    let resolveAction
    const requestWithAsyncResult = vi.fn(
      () => new Promise((r) => { resolveAction = () => r({ sync: { status: 'accepted' }, async: { status: 'ok' } }) })
    )
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request: vi.fn().mockResolvedValue({
        members: [
          { id: 'rm1', rid: 'r1', member: { id: 'u-bob', type: 'individual', account: 'bob', engName: 'Bob', isOwner: false } },
          { id: 'rm2', rid: 'r1', member: { id: 'u-dyn', type: 'individual', account: 'dynamicbob', engName: 'DBob', isOwner: false } },
        ],
      }),
      requestWithAsyncResult,
    })
    render(<MemberRoster room={room} />)
    await screen.findByText('DBob')
    fireEvent.click(screen.getByRole('button', { name: /Promote bob/i }))
    // Promote on bob is in flight — bob's Remove and dynamicbob's buttons must remain enabled.
    expect(screen.getByRole('button', { name: /Promote bob/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /Remove bob/i })).not.toBeDisabled()
    expect(screen.getByRole('button', { name: /Promote dynamicbob/i })).not.toBeDisabled()
    expect(screen.getByRole('button', { name: /Remove dynamicbob/i })).not.toBeDisabled()
    resolveAction()
  })

  it('Remove-by-ID accepts an arbitrary account not in the enriched roster', async () => {
    const { requestWithAsyncResult } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')
    fireEvent.change(screen.getByLabelText(/Remove individual by account/i), { target: { value: 'ghost' } })
    fireEvent.click(screen.getByRole('button', { name: /Remove individual$/i }))
    await waitFor(() =>
      expect(requestWithAsyncResult).toHaveBeenCalledWith(
        'chat.user.alice.request.room.r1.site-A.member.remove',
        { roomId: 'r1', account: 'ghost' }
      )
    )
  })

  it('Remove-by-ID preserves the typed value when the action fails', async () => {
    const requestWithAsyncResult = vi.fn().mockRejectedValue(new Error('only owners can remove members'))
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request: vi.fn().mockResolvedValue({ members: baseMembers }),
      requestWithAsyncResult,
    })
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')
    const input = screen.getByLabelText(/Remove individual by account/i)
    fireEvent.change(input, { target: { value: 'ghost' } })
    fireEvent.click(screen.getByRole('button', { name: /Remove individual$/i }))
    expect(await screen.findByText(/only owners/i)).toBeInTheDocument()
    expect(input.value).toBe('ghost')
  })

  it('Remove-by-ID clears the input on success', async () => {
    const { requestWithAsyncResult } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')
    const input = screen.getByLabelText(/Remove individual by account/i)
    fireEvent.change(input, { target: { value: 'ghost' } })
    fireEvent.click(screen.getByRole('button', { name: /Remove individual$/i }))
    await waitFor(() => expect(requestWithAsyncResult).toHaveBeenCalled())
    await waitFor(() => expect(input.value).toBe(''))
  })

  it('Remove-by-ID accepts an arbitrary org not in the enriched roster', async () => {
    const { requestWithAsyncResult } = setupContext()
    render(<MemberRoster room={room} />)
    await screen.findByText('Engineering')
    fireEvent.change(screen.getByLabelText(/Remove org by id/i), { target: { value: 'ghost-org' } })
    fireEvent.click(screen.getByRole('button', { name: /Remove org$/i }))
    await waitFor(() =>
      expect(requestWithAsyncResult).toHaveBeenCalledWith(
        'chat.user.alice.request.room.r1.site-A.member.remove',
        { roomId: 'r1', orgId: 'ghost-org' }
      )
    )
  })

  it('inline-button callers safely ignore runAction return value on both success and failure', async () => {
    // The Promote/Demote/Remove buttons fire-and-forget; they don't branch on
    // runAction's boolean return. Lock in that ignoring the return value is
    // safe (no unhandled rejection, no post-action side effects fired wrongly).
    const requestWithAsyncResult = vi.fn()
      .mockRejectedValueOnce(new Error('only owners can remove members'))
      .mockResolvedValueOnce({ sync: { status: 'accepted' }, async: { status: 'ok' } })
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request: vi.fn().mockResolvedValue({ members: baseMembers }),
      requestWithAsyncResult,
    })
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')

    // Failure path: error banner appears, button re-enables, no crash.
    fireEvent.click(screen.getByRole('button', { name: /Remove bob/i }))
    expect(await screen.findByText(/only owners/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Remove bob/i })).not.toBeDisabled()

    // Success path right after: no error banner, action proceeds normally.
    fireEvent.click(screen.getByRole('button', { name: /Promote bob/i }))
    await waitFor(() => expect(requestWithAsyncResult).toHaveBeenCalledTimes(2))
  })

  it('surfaces a server error from an action as a banner', async () => {
    const requestWithAsyncResult = vi.fn().mockRejectedValue(new Error('only owners can remove members'))
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request: vi.fn().mockResolvedValue({ members: baseMembers }),
      requestWithAsyncResult,
    })
    render(<MemberRoster room={room} />)
    await screen.findByText('Bob B')
    fireEvent.click(screen.getByRole('button', { name: /Remove bob/i }))
    expect(await screen.findByText(/only owners/i)).toBeInTheDocument()
  })
})
