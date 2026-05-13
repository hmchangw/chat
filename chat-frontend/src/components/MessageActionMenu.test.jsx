import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import MessageActionMenu from './MessageActionMenu'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

const room = { id: 'r1', siteId: 'siteA', userCount: 4 }

beforeEach(() => {
  useNats.mockReset()
  useNats.mockReturnValue({
    user: { account: 'alice', siteId: 'siteA' },
    request: vi.fn(),
  })
})

describe('MessageActionMenu render-gating', () => {
  it('renders the kebab button for the current user\'s own message', () => {
    const msg = { id: 'm1', sender: { account: 'alice' } }
    render(<MessageActionMenu message={msg} room={room} />)
    expect(screen.getByRole('button', { name: /Message actions/i })).toBeInTheDocument()
  })

  it('renders nothing for messages sent by other users', () => {
    const msg = { id: 'm1', sender: { account: 'bob' } }
    const { container } = render(<MessageActionMenu message={msg} room={room} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing when there is no signed-in user', () => {
    useNats.mockReturnValue({ user: null, request: vi.fn() })
    const msg = { id: 'm1', sender: { account: 'alice' } }
    const { container } = render(<MessageActionMenu message={msg} room={room} />)
    expect(container).toBeEmptyDOMElement()
  })
})
