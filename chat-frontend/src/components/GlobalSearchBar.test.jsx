import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import GlobalSearchBar from './GlobalSearchBar'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

function flushDebounce() {
  return act(async () => {
    vi.advanceTimersByTime(500)
    await Promise.resolve()
    await Promise.resolve()
  })
}

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

describe('GlobalSearchBar', () => {
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

  it('does not request on mount without text', () => {
    render(<GlobalSearchBar onSelectRoom={vi.fn()} onSubmit={vi.fn()} />)
    expect(request).not.toHaveBeenCalled()
  })

  it('debounces and searches rooms as the user types', async () => {
    request.mockResolvedValue({ total: 1, results: [roomHit('general')] })

    render(<GlobalSearchBar onSelectRoom={vi.fn()} onSubmit={vi.fn()} />)
    fireEvent.change(screen.getByLabelText('Global search'), {
      target: { value: 'gen' },
    })

    await flushDebounce()

    expect(request).toHaveBeenCalledTimes(1)
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.search.rooms',
      { searchText: 'gen', scope: 'all', size: 8 },
    )
    expect(screen.getByText(/# general/)).toBeInTheDocument()
  })

  it('fires onSelectRoom and clears the bar when a result is clicked', async () => {
    request.mockResolvedValue({ total: 1, results: [roomHit('general')] })
    const onSelectRoom = vi.fn()

    render(
      <GlobalSearchBar onSelectRoom={onSelectRoom} onSubmit={vi.fn()} />,
    )
    const input = screen.getByLabelText('Global search')
    fireEvent.change(input, { target: { value: 'gen' } })
    await flushDebounce()

    fireEvent.click(screen.getByText(/# general/))

    expect(onSelectRoom).toHaveBeenCalledWith({
      id: 'general',
      name: 'general',
      type: 'channel',
      siteId: 'site-A',
    })
    expect(input.value).toBe('')
  })

  it('fires onSubmit when Enter is pressed', async () => {
    request.mockResolvedValue({ total: 0, results: [] })
    const onSubmit = vi.fn()

    render(<GlobalSearchBar onSelectRoom={vi.fn()} onSubmit={onSubmit} />)
    const input = screen.getByLabelText('Global search')
    fireEvent.change(input, { target: { value: 'hello' } })
    await flushDebounce()

    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledWith('hello')
  })

  it('fires onSubmit when "See all results" is clicked', async () => {
    request.mockResolvedValue({ total: 0, results: [] })
    const onSubmit = vi.fn()

    render(<GlobalSearchBar onSelectRoom={vi.fn()} onSubmit={onSubmit} />)
    fireEvent.change(screen.getByLabelText('Global search'), {
      target: { value: 'hello' },
    })
    await flushDebounce()

    fireEvent.click(screen.getByText(/See all results/))
    expect(onSubmit).toHaveBeenCalledWith('hello')
  })

  it('shows empty state when no rooms match', async () => {
    request.mockResolvedValue({ total: 0, results: [] })
    render(<GlobalSearchBar onSelectRoom={vi.fn()} onSubmit={vi.fn()} />)
    fireEvent.change(screen.getByLabelText('Global search'), {
      target: { value: 'zzz' },
    })
    await flushDebounce()
    expect(screen.getByText('No rooms found')).toBeInTheDocument()
  })

  it('closes the dropdown on Escape', async () => {
    request.mockResolvedValue({ total: 1, results: [roomHit('general')] })
    render(<GlobalSearchBar onSelectRoom={vi.fn()} onSubmit={vi.fn()} />)
    const input = screen.getByLabelText('Global search')
    fireEvent.change(input, { target: { value: 'gen' } })
    await flushDebounce()
    expect(screen.getByText(/# general/)).toBeInTheDocument()

    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.queryByText(/# general/)).not.toBeInTheDocument()
  })
})
