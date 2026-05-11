import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import CreateRoomDialog from './CreateRoomDialog'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

function setup(overrides = {}) {
  const requestWithAsyncResult = vi.fn().mockResolvedValue({
    sync: { status: 'accepted', roomId: 'r-new', roomType: 'channel' },
    async: { status: 'ok', roomId: 'r-new', operation: 'room.create' },
  })
  const request = vi.fn()
  useNats.mockReturnValue({
    user: { account: 'alice', siteId: 'site-A' },
    request,
    requestWithAsyncResult,
    ...overrides,
  })
  const onClose = vi.fn()
  const onCreated = vi.fn()
  render(<CreateRoomDialog onClose={onClose} onCreated={onCreated} />)
  return { requestWithAsyncResult, request, onClose, onCreated }
}

describe('CreateRoomDialog', () => {
  beforeEach(() => useNats.mockReset())

  it('does not show a room-type dropdown — type is inferred from inputs', () => {
    setup()
    expect(screen.queryByLabelText(/^Type$/i)).not.toBeInTheDocument()
  })

  it('disables submit until either a name or at least one entity is selected', () => {
    setup()
    const submit = screen.getByRole('button', { name: /Create/i })
    expect(submit).toBeDisabled()
    fireEvent.change(screen.getByLabelText(/Name/i), { target: { value: 'frontend' } })
    expect(submit).not.toBeDisabled()
  })

  it('submits a channel-shaped payload to room.{siteId}.create when a name is given', async () => {
    const { requestWithAsyncResult, onCreated, onClose } = setup()
    fireEvent.change(screen.getByLabelText(/Name/i), { target: { value: 'frontend' } })
    fireEvent.change(screen.getByLabelText(/Users/i), { target: { value: 'bob' } })
    fireEvent.keyDown(screen.getByLabelText(/Users/i), { key: 'Enter' })
    fireEvent.click(screen.getByRole('button', { name: /Create/i }))
    await waitFor(() => expect(requestWithAsyncResult).toHaveBeenCalledTimes(1))
    expect(requestWithAsyncResult).toHaveBeenCalledWith(
      'chat.user.alice.request.room.site-A.create',
      { name: 'frontend', users: ['bob'], orgs: [], channels: [] },
      expect.objectContaining({ treatAsSuccess: expect.any(Function) })
    )
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'r-new', type: 'channel' })
    ))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it('submits a DM-shaped payload (empty name, one user) and surfaces roomType=dm', async () => {
    const requestWithAsyncResult = vi.fn().mockResolvedValue({
      sync: { status: 'accepted', roomId: 'r-dm', roomType: 'dm' },
      async: { status: 'ok', roomId: 'r-dm', operation: 'room.create' },
    })
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request: vi.fn(),
      requestWithAsyncResult,
    })
    const onCreated = vi.fn()
    render(<CreateRoomDialog onClose={vi.fn()} onCreated={onCreated} />)
    fireEvent.change(screen.getByLabelText(/Users/i), { target: { value: 'bob' } })
    fireEvent.keyDown(screen.getByLabelText(/Users/i), { key: 'Enter' })
    fireEvent.click(screen.getByRole('button', { name: /Create/i }))
    await waitFor(() =>
      expect(requestWithAsyncResult).toHaveBeenCalledWith(
        'chat.user.alice.request.room.site-A.create',
        { name: '', users: ['bob'], orgs: [], channels: [] },
        expect.objectContaining({ treatAsSuccess: expect.any(Function) })
      )
    )
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'r-dm', type: 'dm' })
    ))
  })

  it('uses the counterpart account as the optimistic DM display name', async () => {
    // The server will send the canonical name via subscription.update,
    // but until then the room header would render blank if we passed
    // the empty `name` field through. Fall back to the counterpart so
    // the sidebar and header have something to show.
    const { onCreated } = setup({
      requestWithAsyncResult: vi.fn().mockResolvedValue({
        sync: { status: 'accepted', roomId: 'r-dm', roomType: 'dm' },
        async: { status: 'ok', roomId: 'r-dm', operation: 'room.create' },
      }),
    })
    fireEvent.change(screen.getByLabelText(/Users/i), { target: { value: 'bob' } })
    fireEvent.keyDown(screen.getByLabelText(/Users/i), { key: 'Enter' })
    fireEvent.click(screen.getByRole('button', { name: /Create/i }))
    await waitFor(() =>
      expect(onCreated).toHaveBeenCalledWith(expect.objectContaining({ name: 'bob' }))
    )
  })

  it('treats a "dm already exists" reply as success and navigates to the existing room', async () => {
    const requestWithAsyncResult = vi.fn().mockResolvedValue({
      sync: { error: 'dm already exists', roomId: 'r-existing' },
      async: null,
    })
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request: vi.fn(),
      requestWithAsyncResult,
    })
    const onCreated = vi.fn()
    const onClose = vi.fn()
    render(<CreateRoomDialog onClose={onClose} onCreated={onCreated} />)
    fireEvent.change(screen.getByLabelText(/Users/i), { target: { value: 'bob' } })
    fireEvent.keyDown(screen.getByLabelText(/Users/i), { key: 'Enter' })
    fireEvent.click(screen.getByRole('button', { name: /Create/i }))
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'r-existing', type: 'dm' })
    ))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(requestWithAsyncResult.mock.calls[0][2]).toMatchObject({
      treatAsSuccess: expect.any(Function),
    })
  })

  it('shows the server error on a failed create and does not close', async () => {
    const requestWithAsyncResult = vi.fn().mockRejectedValue(new Error('exceeds maximum capacity (50)'))
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request: vi.fn(),
      requestWithAsyncResult,
    })
    const onClose = vi.fn()
    render(<CreateRoomDialog onClose={onClose} onCreated={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(/Name/i), { target: { value: 'huge' } })
    fireEvent.click(screen.getByRole('button', { name: /Create/i }))
    expect(await screen.findByText(/exceeds maximum capacity/i)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
  })
})
