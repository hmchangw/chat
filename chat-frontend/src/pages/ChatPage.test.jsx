import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ChatPage from './ChatPage'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: vi.fn(),
  useRoomEvents: vi.fn(),
}))
vi.mock('../components/RoomList', () => ({
  default: ({ onSelectRoom }) => (
    <div data-testid="room-list">
      <button onClick={() => onSelectRoom({ id: 'r1', name: 'general', type: 'group', siteId: 'site-A' })}>
        pick-group
      </button>
      <button onClick={() => onSelectRoom({ id: 'r2', name: 'bob-dm', type: 'dm', siteId: 'site-A' })}>
        pick-dm
      </button>
    </div>
  ),
}))
vi.mock('../components/MessageArea', () => ({ default: () => <div data-testid="message-area" /> }))
vi.mock('../components/MessageInput', () => ({ default: () => <div data-testid="message-input" /> }))
vi.mock('../components/CreateRoomDialog', () => ({ default: () => null }))

import { useNats } from '../context/NatsContext'
import { useRoomSummaries } from '../context/RoomEventsContext'

beforeEach(() => {
  useNats.mockReset()
  useRoomSummaries.mockReset()
  useNats.mockReturnValue({
    user: { account: 'alice', siteId: 'site-A' },
    request: vi.fn().mockResolvedValue({ status: 'accepted' }),
    disconnect: vi.fn(),
  })
  useRoomSummaries.mockReturnValue({
    summaries: [
      { id: 'r1', name: 'general', type: 'group', siteId: 'site-A', userCount: 2, lastMsgAt: null, unreadCount: 0, hasMention: false, mentionAll: false },
      { id: 'r2', name: 'bob-dm', type: 'dm', siteId: 'site-A', userCount: 2, lastMsgAt: null, unreadCount: 0, hasMention: false, mentionAll: false },
    ],
    setActiveRoom: vi.fn(),
    error: null,
  })
})

describe('ChatPage header buttons', () => {
  it('hides Members and Leave when no room is selected', () => {
    render(<ChatPage />)
    expect(screen.queryByRole('button', { name: /^Members$/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Leave$/ })).not.toBeInTheDocument()
  })

  it('hides Members and Leave on a DM room', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-dm'))
    expect(screen.queryByRole('button', { name: /^Members$/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Leave$/ })).not.toBeInTheDocument()
  })

  it('shows Members and Leave on a group room', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-group'))
    expect(screen.getByRole('button', { name: /^Members$/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^Leave$/ })).toBeInTheDocument()
  })

  it('opens ManageMembersDialog when Members is clicked', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-group'))
    expect(screen.queryByRole('heading', { name: /Manage Members/i })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^Members$/ }))
    expect(screen.getByRole('heading', { name: /Manage Members — general/i })).toBeInTheDocument()
  })

  it('closes ManageMembersDialog when Close is clicked', () => {
    render(<ChatPage />)
    fireEvent.click(screen.getByText('pick-group'))
    fireEvent.click(screen.getByRole('button', { name: /^Members$/ }))
    fireEvent.click(screen.getByRole('button', { name: /^Close$/ }))
    expect(screen.queryByRole('heading', { name: /Manage Members/i })).not.toBeInTheDocument()
  })
})
