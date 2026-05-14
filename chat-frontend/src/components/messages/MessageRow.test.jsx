import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import MessageRow from './MessageRow'

// Mock the existing read-receipt kebab so MessageRow tests don't depend on
// NatsContext (the kebab calls useNats internally). We still want to assert
// it's mounted and receives the right props.
vi.mock('../MessageActionMenu', () => ({
  default: ({ message, room }) => (
    <div data-testid="kebab" data-message-id={message.id} data-room-id={room?.id ?? ''} />
  ),
}))

const msg = {
  id: 'm1',
  content: 'hello world',
  createdAt: '2026-05-13T10:42:00Z',
  sender: { engName: 'Alice', account: 'alice' },
}
const room = { id: 'r1', siteId: 's1', type: 'channel', userCount: 5 }

describe('MessageRow', () => {
  it('renders sender, time, and content', () => {
    render(<MessageRow message={msg} room={room} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />)
    expect(screen.getByText('Alice')).toBeInTheDocument()
    expect(screen.getByText('hello world')).toBeInTheDocument()
    expect(screen.getByText(/\d\d:\d\d/)).toBeInTheDocument()
  })

  it('renders the row with tabindex 0 and data-message-id', () => {
    const { container } = render(
      <MessageRow message={msg} room={room} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />
    )
    const row = container.querySelector('.message-row')
    expect(row).not.toBeNull()
    expect(row.getAttribute('tabindex')).toBe('0')
    expect(row.getAttribute('data-message-id')).toBe('m1')
  })

  it('mounts the read-receipt kebab (MessageActionMenu) and forwards message + room', () => {
    render(<MessageRow message={msg} room={room} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />)
    const kebab = screen.getByTestId('kebab')
    expect(kebab.getAttribute('data-message-id')).toBe('m1')
    expect(kebab.getAttribute('data-room-id')).toBe('r1')
  })

  it('renders an in-bubble QuotedBlock when message.quotedParentMessage is set', () => {
    const quoted = {
      ...msg,
      quotedParentMessage: { id: 'orig', senderName: 'bob', content: 'the original' },
    }
    render(<MessageRow message={quoted} room={room} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={() => {}} />)
    expect(screen.getByText('bob')).toBeInTheDocument()
    expect(screen.getByText('the original')).toBeInTheDocument()
  })

  it('clicking the in-bubble quote fires onJumpToMessage with the original id', () => {
    const onJumpToMessage = vi.fn()
    const quoted = {
      ...msg,
      quotedParentMessage: { id: 'orig', senderName: 'bob', content: 'the original' },
    }
    const { container } = render(
      <MessageRow message={quoted} room={room} context="main" onThread={() => {}} onReply={() => {}} onJumpToMessage={onJumpToMessage} />
    )
    fireEvent.click(container.querySelector('.quoted-block-bubble'))
    expect(onJumpToMessage).toHaveBeenCalledWith('orig')
  })

  it('forwards Thread/Reply clicks via MessageActions', () => {
    const onThread = vi.fn()
    const onReply = vi.fn()
    render(<MessageRow message={msg} room={room} context="main" onThread={onThread} onReply={onReply} onJumpToMessage={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /reply in thread/i }))
    expect(onThread).toHaveBeenCalledWith(msg)
    fireEvent.click(screen.getByRole('button', { name: /quote/i }))
    expect(onReply).toHaveBeenCalledWith(msg)
  })
})
