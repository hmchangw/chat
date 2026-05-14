import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import RoomMessageArea from './RoomMessageArea'
import { BUFFER_MODE } from '../lib/roomEventsReducer'

const loadHistory = vi.fn(async () => {})
const resetToLiveTail = vi.fn()
const jumpToMessage = vi.fn()

vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: (roomId) => ({
    messages: roomId === 'r1' ? [{ id: 'a', content: 'hi', createdAt: '2026-05-13T10:00:00Z' }] : [],
    hasLoadedHistory: roomId === 'r1',
    historyError: null,
    loadHistory,
    bufferMode: BUFFER_MODE.LIVE,
    pendingCount: 0,
    focusMessageId: null,
    resetToLiveTail,
    jumpToMessage,
  }),
}))
vi.mock('./messages/MessageList', () => ({
  default: ({ messages, emptyText, onThread, onReply, onJumpToMessage }) => (
    <div data-testid="list">
      <span>count:{messages.length}</span>
      <button type="button" onClick={() => onThread?.({ id: 'a' })}>fire-thread</button>
      <button type="button" onClick={() => onReply?.({ id: 'a' })}>fire-reply</button>
      <button type="button" onClick={() => onJumpToMessage?.('a')}>fire-jump</button>
      <span>empty:{emptyText}</span>
    </div>
  ),
}))

const room = { id: 'r1', name: 'general', type: 'channel', siteId: 's', userCount: 1 }

describe('RoomMessageArea', () => {
  beforeEach(() => {
    loadHistory.mockClear()
    resetToLiveTail.mockClear()
    jumpToMessage.mockClear()
  })

  it('calls loadHistory once the room is set', () => {
    render(<RoomMessageArea room={room} onThread={() => {}} onReply={() => {}} />)
    expect(loadHistory).toHaveBeenCalled()
  })

  it('renders a "select a room" placeholder when room is null', () => {
    render(<RoomMessageArea room={null} onThread={() => {}} onReply={() => {}} />)
    expect(screen.getByText(/select a room/i)).toBeInTheDocument()
  })

  it('forwards onThread / onReply to MessageList', () => {
    const onThread = vi.fn()
    const onReply = vi.fn()
    render(<RoomMessageArea room={room} onThread={onThread} onReply={onReply} />)
    fireEvent.click(screen.getByText('fire-thread'))
    expect(onThread).toHaveBeenCalledWith({ id: 'a' })
    fireEvent.click(screen.getByText('fire-reply'))
    expect(onReply).toHaveBeenCalledWith({ id: 'a' })
  })

  it('routes onJumpToMessage to the per-room jumpToMessage(msgId) curry', () => {
    // useRoomEvents(roomId).jumpToMessage is already bound to the room,
    // so it takes (messageId) only. Asserting the single-arg shape.
    render(<RoomMessageArea room={room} onThread={() => {}} onReply={() => {}} />)
    fireEvent.click(screen.getByText('fire-jump'))
    expect(jumpToMessage).toHaveBeenCalledWith('a')
  })
})
