import { describe, it, expect, vi } from 'vitest'
import { fetchThreadMessages } from './index'
import type { Nats } from '../types'

function nats(reply: unknown): Nats {
  return {
    user: { account: 'alice', siteId: 's1' },
    request: vi.fn().mockResolvedValue(reply),
    publish: vi.fn(),
    subscribe: vi.fn().mockReturnValue({ unsubscribe: vi.fn() }),
    requestWithAsyncResult: vi.fn(),
  } as unknown as Nats
}

describe('fetchThreadMessages', () => {
  it('reverses the DESC server response into ASC chronological order', async () => {
    // history-service returns thread messages newest-first (Cassandra
    // CLUSTERING ORDER BY created_at DESC). Thread panel renders ASC.
    const n = nats({
      messages: [
        { messageId: 'd', msg: 'd', createdAt: '2026-05-19T10:00:04Z' },
        { messageId: 'c', msg: 'c', createdAt: '2026-05-19T10:00:03Z' },
        { messageId: 'b', msg: 'b', createdAt: '2026-05-19T10:00:02Z' },
        { messageId: 'a', msg: 'a', createdAt: '2026-05-19T10:00:01Z' },
      ],
      hasNext: false,
    })
    const out = await fetchThreadMessages(n, {
      roomId: 'r1',
      siteId: 's1',
      threadMessageId: 'p1',
    })
    expect(out.messages.map((m) => m.id)).toEqual(['a', 'b', 'c', 'd'])
    expect(out.hasNext).toBe(false)
  })

  it('returns empty array when the server returns no messages', async () => {
    const n = nats({ messages: [], hasNext: false })
    const out = await fetchThreadMessages(n, {
      roomId: 'r1',
      siteId: 's1',
      threadMessageId: 'p1',
    })
    expect(out.messages).toEqual([])
  })
})
