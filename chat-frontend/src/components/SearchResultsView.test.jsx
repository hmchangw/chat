import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import SearchResultsView from './SearchResultsView'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

function roomHit(id, overrides = {}) {
  return {
    roomId: id,
    roomName: id,
    roomType: 'channel',
    userAccount: 'alice',
    siteId: 'site-A',
    joinedAt: '2026-04-17T10:00:00Z',
    ...overrides,
  }
}

function messageHit(id, overrides = {}) {
  return {
    messageId: id,
    roomId: 'r1',
    siteId: 'site-A',
    userId: 'u1',
    userAccount: 'bob',
    content: `content for ${id}`,
    createdAt: '2026-04-17T12:00:00Z',
    ...overrides,
  }
}

describe('SearchResultsView', () => {
  let request

  beforeEach(() => {
    request = vi.fn()
    useNats.mockReturnValue({
      user: { account: 'alice', siteId: 'site-A' },
      request,
    })
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('loads the first page of rooms on mount with offset=0', async () => {
    request.mockResolvedValue({
      total: 1,
      results: [roomHit('general')],
    })

    render(
      <SearchResultsView
        text="gen"
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )

    await waitFor(() => {
      expect(request).toHaveBeenCalledWith(
        'chat.user.alice.request.search.rooms',
        { searchText: 'gen', scope: 'all', size: 20, offset: 0 },
      )
    })
    expect(await screen.findByText(/# general/)).toBeInTheDocument()
  })

  it('does not call messages endpoint until the Messages tab is clicked', async () => {
    request.mockImplementation((subject) => {
      if (subject.includes('search.rooms')) {
        return Promise.resolve({ total: 1, results: [roomHit('r1')] })
      }
      return Promise.resolve({ total: 1, results: [messageHit('m1')] })
    })

    render(
      <SearchResultsView
        text="hello"
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )

    await waitFor(() => {
      expect(request).toHaveBeenCalledWith(
        expect.stringContaining('search.rooms'),
        expect.anything(),
      )
    })
    // Messages subject never fired yet.
    expect(
      request.mock.calls.some(([s]) => s.includes('search.messages')),
    ).toBe(false)

    await act(async () => {
      fireEvent.click(screen.getByRole('tab', { name: /Messages/ }))
    })

    await waitFor(() => {
      expect(request).toHaveBeenCalledWith(
        'chat.user.alice.request.search.messages',
        { searchText: 'hello', size: 20, offset: 0 },
      )
    })
    expect(await screen.findByText(/content for m1/)).toBeInTheDocument()
  })

  it('appends the next page when Load more is clicked (rooms)', async () => {
    // First call: 20 hits, total=25 → not done.
    // Second call: 5 hits, total=25 → done.
    request
      .mockResolvedValueOnce({
        total: 25,
        results: Array.from({ length: 20 }, (_, i) => roomHit(`r${i}`)),
      })
      .mockResolvedValueOnce({
        total: 25,
        results: Array.from({ length: 5 }, (_, i) => roomHit(`r${20 + i}`)),
      })

    render(
      <SearchResultsView
        text="room"
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )

    await waitFor(() => {
      expect(screen.getByText(/# r0/)).toBeInTheDocument()
    })
    expect(screen.queryByText(/# r20/)).not.toBeInTheDocument()

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    })

    expect(request).toHaveBeenNthCalledWith(
      2,
      'chat.user.alice.request.search.rooms',
      { searchText: 'room', scope: 'all', size: 20, offset: 20 },
    )
    await waitFor(() => {
      expect(screen.getByText(/# r24/)).toBeInTheDocument()
    })
    // All 25 loaded, Load more disappears.
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('load-more works on the Messages tab too', async () => {
    request.mockImplementation((subject, body) => {
      if (subject.includes('search.rooms')) {
        return Promise.resolve({ total: 0, results: [] })
      }
      if (body.offset === 0) {
        return Promise.resolve({
          total: 21,
          results: Array.from({ length: 20 }, (_, i) => messageHit(`m${i}`)),
        })
      }
      return Promise.resolve({
        total: 21,
        results: [messageHit('m20')],
      })
    })

    render(
      <SearchResultsView
        text="hello"
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )

    await act(async () => {
      fireEvent.click(screen.getByRole('tab', { name: /Messages/ }))
    })
    await waitFor(() => {
      expect(screen.getByText(/content for m0/)).toBeInTheDocument()
    })

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    })

    const messageCalls = request.mock.calls.filter(([s]) =>
      s.includes('search.messages'),
    )
    expect(messageCalls).toHaveLength(2)
    expect(messageCalls[1][1]).toEqual({
      searchText: 'hello',
      size: 20,
      offset: 20,
    })
    await waitFor(() => {
      expect(screen.getByText(/content for m20/)).toBeInTheDocument()
    })
  })

  it('scrolling near the bottom of the list loads the next page', async () => {
    request.mockImplementation((_subject, body) => {
      if (body.offset === 0) {
        return Promise.resolve({
          total: 40,
          results: Array.from({ length: 20 }, (_, i) => roomHit(`r${i}`)),
        })
      }
      return Promise.resolve({
        total: 40,
        results: Array.from({ length: 20 }, (_, i) => roomHit(`r${20 + i}`)),
      })
    })

    render(
      <SearchResultsView
        text="room"
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )
    await waitFor(() => {
      expect(screen.getByText(/# r0/)).toBeInTheDocument()
    })

    const scroller = screen.getByTestId('search-results-scroll')
    // Fake scroller geometry so the threshold check passes.
    Object.defineProperty(scroller, 'scrollTop', { value: 900, configurable: true })
    Object.defineProperty(scroller, 'clientHeight', { value: 400, configurable: true })
    Object.defineProperty(scroller, 'scrollHeight', { value: 1200, configurable: true })

    await act(async () => {
      fireEvent.scroll(scroller)
    })

    expect(request).toHaveBeenCalledTimes(2)
    expect(request.mock.calls[1][1].offset).toBe(20)
  })

  it('picking a room hit forwards it and closes', async () => {
    request.mockResolvedValue({ total: 1, results: [roomHit('general')] })
    const onSelectRoom = vi.fn()
    const onClose = vi.fn()
    render(
      <SearchResultsView
        text="gen"
        onClose={onClose}
        onSelectRoom={onSelectRoom}
      />,
    )
    await waitFor(() => screen.getByText(/# general/))

    fireEvent.click(screen.getByText(/# general/))
    expect(onSelectRoom).toHaveBeenCalledWith({
      id: 'general',
      name: 'general',
      type: 'channel',
      siteId: 'site-A',
    })
    expect(onClose).toHaveBeenCalled()
  })

  it('picking a message hit forwards its roomId and closes', async () => {
    request.mockImplementation((subject) => {
      if (subject.includes('search.rooms')) {
        return Promise.resolve({ total: 0, results: [] })
      }
      return Promise.resolve({ total: 1, results: [messageHit('m1')] })
    })
    const onSelectRoom = vi.fn()
    const onClose = vi.fn()
    render(
      <SearchResultsView
        text="hello"
        onClose={onClose}
        onSelectRoom={onSelectRoom}
      />,
    )
    await act(async () => {
      fireEvent.click(screen.getByRole('tab', { name: /Messages/ }))
    })
    await waitFor(() => screen.getByText(/content for m1/))

    fireEvent.click(screen.getByText(/content for m1/))
    expect(onSelectRoom).toHaveBeenCalledWith({
      id: 'r1',
      siteId: 'site-A',
    })
    expect(onClose).toHaveBeenCalled()
  })

  it('closes on Escape', async () => {
    request.mockResolvedValue({ total: 0, results: [] })
    const onClose = vi.fn()
    render(
      <SearchResultsView
        text="x"
        onClose={onClose}
        onSelectRoom={vi.fn()}
      />,
    )
    // Let the initial rooms load resolve so its state update doesn't race
    // with the Escape keydown we're about to dispatch.
    await waitFor(() => expect(request).toHaveBeenCalled())
    await act(async () => {
      await Promise.resolve()
    })
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })

  it('surfaces errors from the backend', async () => {
    request.mockRejectedValue(new Error('boom'))
    render(
      <SearchResultsView
        text="x"
        onClose={vi.fn()}
        onSelectRoom={vi.fn()}
      />,
    )
    expect(await screen.findByText('boom')).toBeInTheDocument()
  })
})
