import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import MessageArea from './MessageArea'

beforeEach(() => {
  window.HTMLElement.prototype.scrollIntoView = vi.fn()
})

vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: vi.fn(),
}))

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useRoomEvents } from '../context/RoomEventsContext'
import { useNats } from '../context/NatsContext'

beforeEach(() => {
  useNats.mockReturnValue({ user: { account: 'alice', siteId: 'site-A' } })
})

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

  it('shows a federated header indicator for a remote room', () => {
    useRoomEvents.mockReturnValue({
      messages: [], hasLoadedHistory: true, historyError: null, loadHistory: vi.fn().mockResolvedValue(),
    })
    const { container } = render(
      <MessageArea room={{ id: 'r1', name: 'general', type: 'channel', siteId: 'site-B', userCount: 2 }} />,
    )
    const badge = container.querySelector('.message-area-header .room-badge-remote')
    expect(badge).toBeInTheDocument()
    expect(badge.textContent).toContain('site-B')
  })

  it('does not show a federated header indicator for a local room', () => {
    useRoomEvents.mockReturnValue({
      messages: [], hasLoadedHistory: true, historyError: null, loadHistory: vi.fn().mockResolvedValue(),
    })
    const { container } = render(
      <MessageArea room={{ id: 'r1', name: 'general', type: 'channel', siteId: 'site-A', userCount: 2 }} />,
    )
    expect(container.querySelector('.message-area-header .room-badge-remote')).not.toBeInTheDocument()
  })

  it('flags individual cross-site messages with a remote badge', () => {
    useRoomEvents.mockReturnValue({
      messages: [
        { id: 'm1', content: 'local',  createdAt: '2026-04-17T12:00:00Z', siteId: 'site-A', sender: { account: 'alice' } },
        { id: 'm2', content: 'remote', createdAt: '2026-04-17T12:01:00Z', siteId: 'site-B', sender: { account: 'bob' } },
      ],
      hasLoadedHistory: true,
      historyError: null,
      loadHistory: vi.fn().mockResolvedValue(),
    })
    const { container } = render(
      <MessageArea room={{ id: 'r1', name: 'general', type: 'channel', siteId: 'site-A', userCount: 2 }} />,
    )
    const badges = container.querySelectorAll('.message .message-badge-remote')
    expect(badges.length).toBe(1)
    expect(badges[0].textContent).toContain('site-B')
  })

  it('does not flag messages without a siteId', () => {
    useRoomEvents.mockReturnValue({
      messages: [
        { id: 'm1', content: 'hi', createdAt: '2026-04-17T12:00:00Z', sender: { account: 'alice' } },
      ],
      hasLoadedHistory: true,
      historyError: null,
      loadHistory: vi.fn().mockResolvedValue(),
    })
    const { container } = render(
      <MessageArea room={{ id: 'r1', name: 'general', type: 'channel', siteId: 'site-A', userCount: 2 }} />,
    )
    expect(container.querySelector('.message-badge-remote')).not.toBeInTheDocument()
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
