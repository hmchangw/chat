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

  it('auto-dismisses the success banner after 3 seconds', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      setup()
      fireEvent.change(screen.getByLabelText(/Accounts/i), { target: { value: 'bob' } })
      fireEvent.click(screen.getByRole('button', { name: /^Add$/ }))
      expect(await screen.findByText('Accepted')).toBeInTheDocument()
      vi.advanceTimersByTime(3000)
      await waitFor(() => expect(screen.queryByText('Accepted')).not.toBeInTheDocument())
    } finally {
      vi.useRealTimers()
    }
  })
})
