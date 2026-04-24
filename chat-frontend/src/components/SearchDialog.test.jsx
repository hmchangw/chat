import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import SearchDialog from './SearchDialog'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

function flushDebounceAndMicrotasks() {
  return act(async () => {
    vi.advanceTimersByTime(500)
    await Promise.resolve()
    await Promise.resolve()
  })
}

describe('SearchDialog', () => {
  let request

  beforeEach(() => {
    vi.useFakeTimers()
    request = vi.fn()
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request,
    })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  it('does not request until the user types', () => {
    render(
      <SearchDialog
        currentRoom={null}
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )
    expect(request).not.toHaveBeenCalled()
  })

  it('debounces and issues a message search on typing', async () => {
    request.mockResolvedValue({
      total: 1,
      results: [
        {
          messageId: 'm1',
          roomId: 'r1',
          siteId: 'site-A',
          userId: 'u1',
          userAccount: 'bob',
          content: 'hello world',
          createdAt: '2026-04-17T12:00:00Z',
        },
      ],
    })

    render(
      <SearchDialog
        currentRoom={null}
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )

    fireEvent.change(screen.getByLabelText('Search'), {
      target: { value: 'hello' },
    })

    await flushDebounceAndMicrotasks()

    expect(request).toHaveBeenCalledTimes(1)
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.search.messages',
      { searchText: 'hello', size: 20 },
    )
    expect(screen.getByText('hello world')).toBeInTheDocument()
    expect(screen.getByText('bob')).toBeInTheDocument()
  })

  it('scopes messages to current room when the option is checked', async () => {
    request.mockResolvedValue({ total: 0, results: [] })

    render(
      <SearchDialog
        currentRoom={{ id: 'r1', name: 'general', type: 'group' }}
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.change(screen.getByLabelText('Search'), {
      target: { value: 'hi' },
    })

    await flushDebounceAndMicrotasks()

    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.search.messages',
      { searchText: 'hi', size: 20, roomIds: ['r1'] },
    )
  })

  it('switches to the rooms tab and sends the selected scope', async () => {
    request.mockResolvedValue({
      total: 1,
      results: [
        {
          roomId: 'r1',
          roomName: 'general',
          roomType: 'channel',
          userAccount: 'alice',
          siteId: 'site-A',
          joinedAt: '2026-04-17T10:00:00Z',
        },
      ],
    })

    render(
      <SearchDialog
        currentRoom={null}
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole('tab', { name: 'Rooms' }))
    fireEvent.change(screen.getByLabelText('Room scope'), {
      target: { value: 'channel' },
    })
    fireEvent.change(screen.getByLabelText('Search'), {
      target: { value: 'gen' },
    })

    await flushDebounceAndMicrotasks()

    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.search.rooms',
      { searchText: 'gen', scope: 'channel', size: 20 },
    )
    expect(screen.getByText(/# general/)).toBeInTheDocument()
  })

  it('selects a room hit and closes the dialog', async () => {
    request.mockResolvedValue({
      total: 1,
      results: [
        {
          roomId: 'r1',
          roomName: 'general',
          roomType: 'channel',
          userAccount: 'alice',
          siteId: 'site-A',
          joinedAt: '2026-04-17T10:00:00Z',
        },
      ],
    })

    const onSelectRoom = vi.fn()
    const onClose = vi.fn()

    render(
      <SearchDialog
        currentRoom={null}
        onClose={onClose}
        onSelectRoom={onSelectRoom}
      />,
    )

    fireEvent.click(screen.getByRole('tab', { name: 'Rooms' }))
    fireEvent.change(screen.getByLabelText('Search'), {
      target: { value: 'gen' },
    })
    await flushDebounceAndMicrotasks()

    fireEvent.click(screen.getByText(/# general/))

    expect(onSelectRoom).toHaveBeenCalledWith({
      id: 'r1',
      name: 'general',
      type: 'channel',
      siteId: 'site-A',
    })
    expect(onClose).toHaveBeenCalled()
  })

  it('selects a message hit by forwarding its roomId', async () => {
    request.mockResolvedValue({
      total: 1,
      results: [
        {
          messageId: 'm1',
          roomId: 'r1',
          siteId: 'site-A',
          userAccount: 'bob',
          content: 'hi',
          createdAt: '2026-04-17T12:00:00Z',
        },
      ],
    })

    const onSelectRoom = vi.fn()
    const onClose = vi.fn()

    render(
      <SearchDialog
        currentRoom={null}
        onClose={onClose}
        onSelectRoom={onSelectRoom}
      />,
    )

    fireEvent.change(screen.getByLabelText('Search'), {
      target: { value: 'hi' },
    })
    await flushDebounceAndMicrotasks()

    fireEvent.click(screen.getByText('hi'))

    expect(onSelectRoom).toHaveBeenCalledWith({
      id: 'r1',
      siteId: 'site-A',
    })
    expect(onClose).toHaveBeenCalled()
  })

  it('shows an empty state when there are no results', async () => {
    request.mockResolvedValue({ total: 0, results: [] })
    render(
      <SearchDialog
        currentRoom={null}
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )
    fireEvent.change(screen.getByLabelText('Search'), {
      target: { value: 'zzz' },
    })
    await flushDebounceAndMicrotasks()
    expect(screen.getByText('No results')).toBeInTheDocument()
  })

  it('surfaces backend errors', async () => {
    request.mockRejectedValue(new Error('boom'))
    render(
      <SearchDialog
        currentRoom={null}
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )
    fireEvent.change(screen.getByLabelText('Search'), {
      target: { value: 'zzz' },
    })
    await flushDebounceAndMicrotasks()
    expect(screen.getByText('boom')).toBeInTheDocument()
  })

  it('closes when the overlay is clicked', () => {
    const onClose = vi.fn()
    const { container } = render(
      <SearchDialog
        currentRoom={null}
        onClose={onClose}
        onSelectRoom={vi.fn()}
      />,
    )
    fireEvent.click(container.querySelector('.dialog-overlay'))
    expect(onClose).toHaveBeenCalled()
  })
})
