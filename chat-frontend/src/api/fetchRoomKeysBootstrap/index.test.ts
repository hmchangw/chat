import { describe, it, expect, vi } from 'vitest'
import { fetchRoomKeysBootstrap } from './index'

describe('fetchRoomKeysBootstrap', () => {
  it('issues a request on chat.user.{account}.request.rooms.keys with an empty payload and returns the parsed RoomKeysResponse', async () => {
    const request = vi.fn().mockResolvedValue({ keys: [{ roomId: 'r1', version: 7, privateKey: 'AAAA' }] })
    const nats = { request, user: { account: 'alice', id: '' }, subscribe: vi.fn(), publish: vi.fn(), requestWithAsyncResult: vi.fn(), connected: true, error: null }

    const got = await fetchRoomKeysBootstrap(nats as never)

    expect(request).toHaveBeenCalledWith('chat.user.alice.request.rooms.keys', {})
    expect(got).toEqual({ keys: [{ roomId: 'r1', version: 7, privateKey: 'AAAA' }] })
  })
})
