import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import RoomList from './RoomList'

vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: vi.fn(),
}))

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useRoomSummaries } from '../context/RoomEventsContext'
import { useNats } from '../context/NatsContext'

beforeEach(() => {
  useNats.mockReturnValue({ user: { account: 'alice', siteId: 'site-A' } })
})

function summary(id, overrides = {}) {
  return {
    id,
    name: id,
    type: 'channel',
    siteId: 'site-A',
    userCount: 2,
    lastMsgAt: '2026-04-17T10:00:00Z',
    unreadCount: 0,
    hasMention: false,
    mentionAll: false,
    ...overrides,
  }
}

describe('RoomList', () => {
  it('renders summaries', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a'), summary('b', { type: 'dm' })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(screen.getByText(/# a/)).toBeInTheDocument()
    expect(screen.getByText(/@ b/)).toBeInTheDocument()
  })

  it('shows unread count badge and bold class when unreadCount > 0', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { unreadCount: 3 })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-item-unread')).toBeInTheDocument()
    expect(container.querySelector('.room-badge-unread').textContent).toBe('3')
  })

  it('shows [@] pill when hasMention', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { hasMention: true, unreadCount: 1 })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-badge-mention')).toBeInTheDocument()
    expect(container.querySelector('.room-badge-mention-all')).not.toBeInTheDocument()
  })

  it('shows [!] pill when mentionAll (takes precedence over [@])', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { hasMention: true, mentionAll: true, unreadCount: 1 })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-badge-mention-all')).toBeInTheDocument()
    expect(container.querySelector('.room-badge-mention')).not.toBeInTheDocument()
  })

  it('shows remote site badge when room.siteId !== user.siteId', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { siteId: 'site-B' })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    const badge = container.querySelector('.room-badge-remote')
    expect(badge).toBeInTheDocument()
    expect(badge.textContent).toContain('site-B')
  })

  it('does not show remote badge when room.siteId matches user.siteId', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { siteId: 'site-A' })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-badge-remote')).not.toBeInTheDocument()
  })

  it('marks remote rooms with .room-item-remote class', () => {
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a', { siteId: 'site-B' })],
      setActiveRoom: vi.fn(),
      error: null,
    })
    const { container } = render(<RoomList selectedRoomId={null} onSelectRoom={vi.fn()} />)
    expect(container.querySelector('.room-item-remote')).toBeInTheDocument()
  })

  it('calls onSelectRoom on click', () => {
    const onSelectRoom = vi.fn()
    const setActiveRoom = vi.fn()
    useRoomSummaries.mockReturnValue({
      summaries: [summary('a')],
      setActiveRoom,
      error: null,
    })
    render(<RoomList selectedRoomId={null} onSelectRoom={onSelectRoom} />)
    fireEvent.click(screen.getByText(/# a/))
    expect(onSelectRoom).toHaveBeenCalledWith(expect.objectContaining({ id: 'a' }))
  })
})
