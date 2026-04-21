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
