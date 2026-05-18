import { describe, it, expect, vi } from 'vitest'
import { getUnreadCount } from './index'
import type { Nats } from '../types'

function fakeNats(): Nats {
  return {
    user: { account: 'alice', siteId: 'site-A' },
    request: vi.fn(),
  } as unknown as Nats
}

describe('getUnreadCount', () => {
  it('resolves to the hardcoded mock count', async () => {
    const nats = fakeNats()
    await expect(getUnreadCount(nats)).resolves.toEqual({ count: 42 })
  })

  it('does not hit the transport while mocked', async () => {
    const nats = fakeNats()
    await getUnreadCount(nats)
    expect(nats.request).not.toHaveBeenCalled()
  })
})
