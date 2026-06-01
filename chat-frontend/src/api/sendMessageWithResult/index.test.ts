import { describe, it, expect, vi } from 'vitest'
import { sendMessageWithResult } from './index'
import type { Nats } from '../types'

function fakeNats({ publish, waitForResponse }: Partial<Nats>) {
  return {
    user: { account: 'alice', siteId: 'site-A' },
    publish,
    waitForResponse,
  } as unknown as Nats
}

const args = {
  roomId: 'r1',
  siteId: 'site-A',
  payload: { id: 'm1', content: 'hi', requestId: 'req-1' },
}

describe('sendMessageWithResult', () => {
  it('publishes to the msg.send subject with the payload', () => {
    const publish = vi.fn()
    const waitForResponse = vi.fn().mockReturnValue(new Promise(() => {}))
    sendMessageWithResult(fakeNats({ publish, waitForResponse }), args)
    expect(publish).toHaveBeenCalledWith(
      'chat.user.alice.room.r1.site-A.msg.send',
      args.payload,
    )
  })

  it('registers the response waiter for the payload requestId BEFORE publishing', () => {
    const publish = vi.fn()
    const waitForResponse = vi.fn().mockReturnValue(new Promise(() => {}))
    sendMessageWithResult(fakeNats({ publish, waitForResponse }), args)
    expect(waitForResponse).toHaveBeenCalledWith('req-1', {})
    // Subscribe-before-publish: a fast gatekeeper must not beat the waiter.
    expect(waitForResponse.mock.invocationCallOrder[0])
      .toBeLessThan(publish.mock.invocationCallOrder[0])
  })

  it('resolves with the reply the correlator delivers', async () => {
    const publish = vi.fn()
    const reply = { id: 'm1', content: 'hi', userAccount: 'alice' }
    const waitForResponse = vi.fn().mockResolvedValue(reply)
    await expect(
      sendMessageWithResult(fakeNats({ publish, waitForResponse }), args),
    ).resolves.toEqual(reply)
  })

  it('forwards a timeout override to waitForResponse', () => {
    const publish = vi.fn()
    const waitForResponse = vi.fn().mockReturnValue(new Promise(() => {}))
    sendMessageWithResult(fakeNats({ publish, waitForResponse }), args, { timeout: 5000 })
    expect(waitForResponse).toHaveBeenCalledWith('req-1', { timeout: 5000 })
  })
})
