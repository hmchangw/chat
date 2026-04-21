import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import RoomList from './RoomList'

vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: vi.fn(),
}))

import { useRoomSummaries } from '../context/RoomEventsContext'

function summary(id, overrides = {}) {
  return {
    id,
    name: id,
    type: 'group',
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
