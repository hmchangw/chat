import { describe, it, expect, vi } from 'vitest'
import { markRoomRead } from './index'
import type { Nats } from '../types'

function fakeNats(request: unknown) {
  return { user: { account: 'alice', siteId: 'site-A' }, request } as unknown as Nats
}

describe('markRoomRead', () => {
  it('requests the message.read subject and resolves on success', async () => {
    const request = vi.fn().mockResolvedValue({})
    await expect(
      markRoomRead(fakeNats(request), { roomId: 'r1', siteId: 'site-A' }),
    ).resolves.toBeUndefined()
    expect(request).toHaveBeenCalledWith(
      'chat.user.alice.request.room.r1.site-A.message.read',
      {},
    )
  })

  it('resolves (does not reject) even when the transport errors', async () => {
    const request = vi.fn().mockRejectedValue(new Error('boom'))
    await expect(
      markRoomRead(fakeNats(request), { roomId: 'r1', siteId: 'site-A' }),
    ).resolves.toBeUndefined()
  })
})
