import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import ChatPage from './ChatPage'

vi.mock('../components/RoomMessageArea', () => ({
  default: ({ room }) => <div>area:{room?.id ?? 'none'}</div>,
}))
vi.mock('../components/RoomMessageInput', () => ({
  default: ({ room }) => <div>input:{room?.id ?? 'none'}</div>,
}))
vi.mock('../components/InRoomSearch', () => ({
  default: ({ onClose }) => (
    <aside role="complementary">
      in-room-search
      <button type="button" onClick={onClose}>close-inroom</button>
    </aside>
  ),
}))
vi.mock('../components/ManageMembersDialog', () => ({
  default: ({ onClose }) => (
    <div role="dialog">
      members-dialog
      <button type="button" onClick={onClose}>close-members</button>
    </div>
  ),
}))
vi.mock('../components/LeaveRoomButton', () => ({
  default: ({ room }) => <button type="button">Leave {room?.name}</button>,
}))
// RoomMembersBadge depends on NatsContext + the member.list RPC; mock it
// down to a deterministic "Members ({count})" button so we can assert on
// the open affordance and verify ChatPage forwards the room/refreshKey props.
vi.mock('../components/RoomMembersBadge', () => ({
  default: ({ room, onOpen, refreshKey }) => (
    <button
      type="button"
      onClick={onOpen}
      data-room-id={room?.id ?? ''}
      data-refresh-key={refreshKey}
    >
      Members ({room?.userCount ?? 0})
    </button>
  ),
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomSummaries: () => ({ jumpToMessage: vi.fn() }),
}))

const channel = { id: 'r1', name: 'general', type: 'channel', userCount: 7 }
const dm = { id: 'r2', name: 'alice & bob', type: 'dm', userCount: 2 }

describe('ChatPage (middle column)', () => {
  it('renders MessageArea and MessageInput with the selected room', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    expect(screen.getByText('area:r1')).toBeInTheDocument()
    expect(screen.getByText('input:r1')).toBeInTheDocument()
  })

  it('renders room-header with room name, RoomMembersBadge, and Leave for channels', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    expect(screen.getByText(/# general/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^members \(7\)$/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /leave/i })).toBeInTheDocument()
  })

  it('renders RoomMembersBadge but hides Leave for DMs', () => {
    render(<ChatPage selectedRoom={dm} onSelectRoom={() => {}} />)
    expect(screen.queryByRole('button', { name: /leave/i })).not.toBeInTheDocument()
  })

  it('clicking the badge opens the members dialog', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /^members \(7\)$/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('Ctrl-F opens InRoomSearch; Esc closes it', () => {
    render(<ChatPage selectedRoom={channel} onSelectRoom={() => {}} />)
    fireEvent.keyDown(window, { key: 'f', ctrlKey: true })
    expect(screen.getByText('in-room-search')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByText('in-room-search')).not.toBeInTheDocument()
  })

  it('renders no room-header when no room is selected', () => {
    render(<ChatPage selectedRoom={null} onSelectRoom={() => {}} />)
    expect(screen.queryByRole('button', { name: /^members/i })).not.toBeInTheDocument()
    expect(screen.getByText('area:none')).toBeInTheDocument()
  })
})
