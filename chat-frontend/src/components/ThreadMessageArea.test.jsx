import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeAll } from 'vitest'
import ThreadMessageArea from './ThreadMessageArea'

beforeAll(() => {
  window.HTMLElement.prototype.scrollIntoView = vi.fn()
})

const activeParent = { roomId: 'r1', siteId: 's1', messageId: 'p1', createdAtMs: 1000 }
const retryReply = vi.fn()
const dismissReply = vi.fn()

vi.mock('../context/ThreadEventsContext', () => ({
  useThreadEvents: () => ({
    activeParent,
    messages: [
      { id: 'reply-1', content: 'first reply', createdAt: '2026-05-13T10:01:00Z', sender: { account: 'bob' } },
      { id: 'reply-2', content: 'optimistic', _local: true, _status: 'failed', sender: { account: 'alice' } },
    ],
    hasLoadedHistory: true,
    historyLoading: false,
    historyError: null,
    retryReply, dismissReply,
  }),
}))
vi.mock('../context/RoomEventsContext', () => ({
  useRoomEvents: () => ({
    messages: [
      { id: 'p1', content: 'parent body', createdAt: '2026-05-13T10:00:00Z', sender: { account: 'alice' } },
    ],
  }),
}))
vi.mock('../context/NatsContext', () => ({
  useNats: () => ({ user: { account: 'alice', siteId: 's1' }, publish: vi.fn() }),
}))
vi.mock('./messages/MessageList', () => ({
  default: ({ messages, emptyText, context, onReply, onRetry, onDismiss, historyLoading, historyError }) => (
    <div data-testid="list">
      <span>context:{context}</span>
      <span>count:{messages.length}</span>
      <span>loading:{String(!!historyLoading)}</span>
      <span>error:{historyError ?? 'none'}</span>
      <span>empty:{emptyText ?? 'none'}</span>
      {messages.map((m) => (
        <div key={m.id} data-row={m.id}>{m.content || '[deleted]'}{m._status === 'failed' ? ' (failed)' : ''}</div>
      ))}
      <button type="button" onClick={() => onReply?.({ id: 'reply-1', sender: { account: 'bob' }, content: 'first reply' })}>fire-reply</button>
      <button type="button" onClick={() => onRetry?.('reply-2')}>fire-retry</button>
      <button type="button" onClick={() => onDismiss?.('reply-2')}>fire-dismiss</button>
    </div>
  ),
}))

describe('ThreadMessageArea', () => {
  it('renders the parent as the first row, then the replies', () => {
    render(<ThreadMessageArea onReply={() => {}} />)
    const ids = Array.from(document.querySelectorAll('[data-row]')).map((el) => el.getAttribute('data-row'))
    expect(ids).toEqual(['p1', 'reply-1', 'reply-2'])
  })

  it('passes context="thread" to MessageList', () => {
    render(<ThreadMessageArea onReply={() => {}} />)
    expect(screen.getByText('context:thread')).toBeInTheDocument()
  })

  it('forwards onReply with the reply payload', () => {
    const onReply = vi.fn()
    render(<ThreadMessageArea onReply={onReply} />)
    fireEvent.click(screen.getByText('fire-reply'))
    expect(onReply).toHaveBeenCalled()
  })

  it('forwards retry / dismiss to ThreadEventsContext', () => {
    render(<ThreadMessageArea onReply={() => {}} />)
    fireEvent.click(screen.getByText('fire-retry'))
    expect(retryReply).toHaveBeenCalledWith('reply-2')
    fireEvent.click(screen.getByText('fire-dismiss'))
    expect(dismissReply).toHaveBeenCalledWith('reply-2')
  })
})
