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
