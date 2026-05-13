import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import MessageActionMenu from './MessageActionMenu'
import { readReceipt } from '../lib/subjects'

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

describe('MessageActionMenu read-receipt RPC', () => {
  const msg = { id: 'm1', sender: { account: 'alice' } }

  function deferred() {
    let resolve, reject
    const promise = new Promise((res, rej) => { resolve = res; reject = rej })
    return { promise, resolve, reject }
  }

  it('shows Loading… immediately after opening the menu', () => {
    const d = deferred()
    const request = vi.fn().mockReturnValue(d.promise)
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    render(<MessageActionMenu message={msg} room={room} />)
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    expect(screen.getByText(/Loading…/i)).toBeInTheDocument()
  })

  it('calls the RPC with the correct subject and payload', () => {
    const d = deferred()
    const request = vi.fn().mockReturnValue(d.promise)
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    render(<MessageActionMenu message={msg} room={room} />)
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    expect(request).toHaveBeenCalledWith(
      readReceipt('alice', 'r1', 'siteA'),
      { messageId: 'm1' }
    )
  })

  it('renders "Read by X of Y" once the RPC resolves', async () => {
    const d = deferred()
    const request = vi.fn().mockReturnValue(d.promise)
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    render(<MessageActionMenu message={msg} room={room} />)
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    await act(async () => {
      d.resolve({ readers: [
        { userId: 'u1', account: 'bob', engName: 'Bob', chineseName: '' },
        { userId: 'u2', account: 'carol', engName: 'Carol', chineseName: '' },
      ] })
    })
    expect(await screen.findByText('Read by 2 of 3')).toBeInTheDocument()
  })

  it('clamps Y at 0 for a single-member room', async () => {
    const d = deferred()
    const request = vi.fn().mockReturnValue(d.promise)
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    render(
      <MessageActionMenu
        message={msg}
        room={{ id: 'r1', siteId: 'siteA', userCount: 1 }}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    await act(async () => { d.resolve({ readers: [] }) })
    expect(await screen.findByText('Read by 0 of 0')).toBeInTheDocument()
  })

  it('renders the RPC error message inline', async () => {
    const d = deferred()
    const request = vi.fn().mockReturnValue(d.promise)
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    render(<MessageActionMenu message={msg} room={room} />)
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    await act(async () => { d.reject(new Error('only the message sender can view read receipts')) })
    expect(
      await screen.findByText(/only the message sender can view read receipts/i)
    ).toBeInTheDocument()
  })

  it('refetches the RPC every time the menu is reopened', async () => {
    const request = vi.fn()
      .mockResolvedValueOnce({ readers: [] })
      .mockResolvedValueOnce({ readers: [{ userId: 'u1', account: 'bob', engName: 'Bob', chineseName: '' }] })
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    render(<MessageActionMenu message={msg} room={room} />)
    const kebab = screen.getByRole('button', { name: /Message actions/i })

    fireEvent.click(kebab)
    await waitFor(() => expect(screen.getByText('Read by 0 of 3')).toBeInTheDocument())
    fireEvent.click(kebab) // close
    fireEvent.click(kebab) // reopen
    await waitFor(() => expect(screen.getByText('Read by 1 of 3')).toBeInTheDocument())
    expect(request).toHaveBeenCalledTimes(2)
  })

  it('falls back to user.siteId when room.siteId is missing', () => {
    const d = deferred()
    const request = vi.fn().mockReturnValue(d.promise)
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'siteA' },
      request,
    })
    render(
      <MessageActionMenu
        message={msg}
        room={{ id: 'r1', userCount: 3 }}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /Message actions/i }))
    expect(request).toHaveBeenCalledWith(
      readReceipt('alice', 'r1', 'siteA'),
      { messageId: 'm1' }
    )
  })
})
