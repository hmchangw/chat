import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import RoomMessageInput from './RoomMessageInput'

const sendMessage = vi.fn()
const formatAsyncJobError = vi.fn((e) => (e instanceof Error ? e.message : String(e)))
const dispatch = vi.fn()

vi.mock('@/api', () => ({
  sendMessage: (...a) => sendMessage(...a),
  formatAsyncJobError: (...a) => formatAsyncJobError(...a),
}))
vi.mock('@/context/NatsContext', () => ({
  useNats: () => ({ user: { account: 'alice', siteId: 's1' } }),
}))
vi.mock('@/context/RoomEventsContext', () => ({
  useRoomDispatch: () => dispatch,
}))
vi.mock('@/lib/idgen', () => ({ generateMessageID: () => '12345678901234567890' }))
vi.mock('uuid', () => ({ v4: () => 'req-uuid' }))

const room = { id: 'r1', name: 'general', type: 'channel' }

describe('RoomMessageInput', () => {
  beforeEach(() => {
    sendMessage.mockReset()
    sendMessage.mockResolvedValue({ requestId: 'req-uuid', result: {} })
    dispatch.mockClear()
    formatAsyncJobError.mockClear()
  })

  it('renders the form disabled when no room is selected', () => {
    render(<RoomMessageInput room={null} />)
    expect(screen.getByPlaceholderText(/select a room/i)).toBeDisabled()
  })

  it('calls sendMessage on submit with the correct args', () => {
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(sendMessage).toHaveBeenCalledWith(
      expect.objectContaining({ user: expect.objectContaining({ account: 'alice' }) }),
      {
        roomId: 'r1',
        siteId: 's1',
        payload: { id: '12345678901234567890', content: 'hello', requestId: 'req-uuid' },
      },
    )
  })

  it('clears the text after submit', () => {
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(input).toHaveValue('')
  })

  it('does not send when text is empty or whitespace', () => {
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: '   ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(sendMessage).not.toHaveBeenCalled()
  })

  it('forwards quotedTarget and onClearQuote to MessageInputForm', () => {
    const onClearQuote = vi.fn()
    render(
      <RoomMessageInput
        room={room}
        quotedTarget={{ id: 'q', senderName: 'bob', content: 'orig' }}
        onClearQuote={onClearQuote}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /clear quoted message/i }))
    expect(onClearQuote).toHaveBeenCalled()
  })

  it('includes quotedParentMessageId in the send payload when quotedTarget is set', () => {
    render(
      <RoomMessageInput
        room={room}
        quotedTarget={{ id: 'q123', senderName: 'bob', content: 'orig' }}
        onClearQuote={() => {}}
      />
    )
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'reply' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(sendMessage).toHaveBeenCalledWith(
      expect.anything(),
      {
        roomId: 'r1',
        siteId: 's1',
        payload: {
          id: '12345678901234567890',
          content: 'reply',
          requestId: 'req-uuid',
          quotedParentMessageId: 'q123',
        },
      },
    )
  })

  it('dispatches an optimistic MESSAGE_SENT_LOCAL alongside the send', () => {
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'hi' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: 'MESSAGE_SENT_LOCAL',
      roomId: 'r1',
      message: expect.objectContaining({
        id: '12345678901234567890',
        content: 'hi',
        sender: expect.objectContaining({ account: 'alice' }),
        _local: true,
      }),
    }))
  })

  it('optimistic message carries a server-shaped quotedParentMessage snapshot', () => {
    render(
      <RoomMessageInput
        room={room}
        quotedTarget={{ id: 'q123', senderName: 'bob', content: 'orig' }}
        onClearQuote={() => {}}
      />
    )
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'reply' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    const call = dispatch.mock.calls.find((c) => c[0]?.type === 'MESSAGE_SENT_LOCAL')
    expect(call).toBeDefined()
    expect(call[0].message.quotedParentMessage).toEqual({
      messageId: 'q123',
      sender: { engName: 'bob', account: 'bob' },
      msg: 'orig',
    })
  })

  it('surfaces a send failure via formatAsyncJobError without throwing from the handler', async () => {
    const err = new Error('content exceeds maximum size of 20480 bytes')
    sendMessage.mockRejectedValueOnce(err)
    render(<RoomMessageInput room={room} />)
    const input = screen.getByPlaceholderText(/general/i)
    fireEvent.change(input, { target: { value: 'too big' } })
    expect(() => fireEvent.keyDown(input, { key: 'Enter' })).not.toThrow()
    // Let the rejected send promise settle.
    await Promise.resolve()
    await Promise.resolve()
    expect(formatAsyncJobError).toHaveBeenCalledWith(err)
  })
})
