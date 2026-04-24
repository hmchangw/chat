import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import MessageArea from './MessageArea'

beforeEach(() => {
  window.HTMLElement.prototype.scrollIntoView = vi.fn()
})

vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: vi.fn(),
}))

import { useRoomEvents } from '../context/RoomEventsContext'

describe('MessageArea', () => {
  it('shows the empty-state when no room is selected', () => {
    useRoomEvents.mockReturnValue({
      messages: [], hasLoadedHistory: false, historyError: null, loadHistory: vi.fn(),
    })
    render(<MessageArea room={null} />)
    expect(screen.getByText(/Select a room/i)).toBeInTheDocument()
  })

  it('renders messages from the provider', () => {
    useRoomEvents.mockReturnValue({
      messages: [
        { id: 'm1', content: 'hello', createdAt: '2026-04-17T12:00:00Z', sender: { account: 'bob', engName: 'Bob' } },
      ],
      hasLoadedHistory: true,
      historyError: null,
      loadHistory: vi.fn().mockResolvedValue(),
    })
    render(<MessageArea room={{ id: 'r1', name: 'general', type: 'channel', userCount: 2 }} />)
    expect(screen.getByText('hello')).toBeInTheDocument()
    expect(screen.getByText('Bob')).toBeInTheDocument()
  })

  it('surfaces historyError', () => {
    useRoomEvents.mockReturnValue({
      messages: [],
      hasLoadedHistory: false,
      historyError: 'boom',
      loadHistory: vi.fn().mockResolvedValue(),
    })
    render(<MessageArea room={{ id: 'r1', name: 'general', type: 'channel', userCount: 2 }} />)
    expect(screen.getByText('boom')).toBeInTheDocument()
  })

  it('calls loadHistory when room changes', () => {
    const loadHistory = vi.fn().mockResolvedValue()
    useRoomEvents.mockReturnValue({
      messages: [], hasLoadedHistory: false, historyError: null, loadHistory,
    })
    render(<MessageArea room={{ id: 'r1', name: 'general', type: 'channel', userCount: 2 }} />)
    expect(loadHistory).toHaveBeenCalled()
  })
})
