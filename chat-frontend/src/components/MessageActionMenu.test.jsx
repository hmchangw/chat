import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
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

describe('MessageActionMenu open/close', () => {
  const msg = { id: 'm1', sender: { account: 'alice' } }

  function setup({ request = vi.fn().mockReturnValue(new Promise(() => {})) } = {}) {
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    return render(<MessageActionMenu message={msg} room={room} />)
  }

  it('opens the popover when the kebab is clicked', () => {
    setup()
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    expect(screen.getByRole('menu')).toBeInTheDocument()
  })

  it('closes the popover when the kebab is clicked again (toggle)', () => {
    setup()
    const kebab = screen.getByRole('button', { name: /Message actions/i })
    fireEvent.click(kebab)
    fireEvent.click(kebab)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('closes the popover when the user clicks outside', () => {
    setup()
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    expect(screen.getByRole('menu')).toBeInTheDocument()
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('closes the popover when Escape is pressed', () => {
    setup()
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('does not close when clicking inside the popover', () => {
    setup()
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    fireEvent.mouseDown(screen.getByRole('menu'))
    expect(screen.getByRole('menu')).toBeInTheDocument()
  })
})
