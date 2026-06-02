import { describe, it, expect, vi } from 'vitest'
import { sendMessage } from './index'

function makeNats() {
  return {
    user: { account: 'alice', siteId: 's1' },
    publishWithAsyncResult: vi.fn().mockResolvedValue({ requestId: 'req-1', result: { id: 'm1' } }),
  }
}

describe('sendMessage', () => {
  it('publishes to the msg.send subject with the payload and the body requestId in opts', () => {
    const nats = makeNats()
    sendMessage(nats as never, {
      roomId: 'r1',
      siteId: 's1',
      payload: { id: 'm1', content: 'hi', requestId: 'req-1' },
    })
    expect(nats.publishWithAsyncResult).toHaveBeenCalledWith(
      'chat.user.alice.room.r1.s1.msg.send',
      { id: 'm1', content: 'hi', requestId: 'req-1' },
      { requestId: 'req-1' },
    )
  })

  it('forwards caller opts merged with the requestId (3-arg shape)', () => {
    const nats = makeNats()
    sendMessage(nats as never, {
      roomId: 'r1',
      siteId: 's1',
      payload: { id: 'm1', content: 'hi', requestId: 'req-1' },
    }, { asyncTimeout: 1000 })
    expect(nats.publishWithAsyncResult).toHaveBeenCalledWith(
      'chat.user.alice.room.r1.s1.msg.send',
      { id: 'm1', content: 'hi', requestId: 'req-1' },
      { asyncTimeout: 1000, requestId: 'req-1' },
    )
  })

  it('returns the promise from publishWithAsyncResult', async () => {
    const nats = makeNats()
    const result = await sendMessage(nats as never, {
      roomId: 'r1',
      siteId: 's1',
      payload: { id: 'm1', content: 'hi', requestId: 'req-1' },
    })
    expect(result).toEqual({ requestId: 'req-1', result: { id: 'm1' } })
  })
})
