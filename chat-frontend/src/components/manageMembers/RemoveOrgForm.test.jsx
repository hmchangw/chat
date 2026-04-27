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
