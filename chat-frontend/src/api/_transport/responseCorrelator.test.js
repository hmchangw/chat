import { describe, it, expect, vi } from 'vitest'
import {
  parseResponseRequestId,
  createResponseCorrelator,
  RESPONSE_TIMEOUT_KIND,
} from './responseCorrelator'

describe('parseResponseRequestId', () => {
  it('extracts the requestId token from a response subject', () => {
    expect(
      parseResponseRequestId('chat.user.alice.response.019e83b8-cbbe-7db7-9c2f-68f8b4f6ced8'),
    ).toBe('019e83b8-cbbe-7db7-9c2f-68f8b4f6ced8')
  })

  it('returns null for a subject without the .response. segment', () => {
    expect(parseResponseRequestId('chat.user.alice.event.room')).toBeNull()
  })

  it('returns null for an empty / missing subject', () => {
    expect(parseResponseRequestId('')).toBeNull()
    expect(parseResponseRequestId(undefined)).toBeNull()
  })
})

describe('createResponseCorrelator', () => {
  it('resolves a waiter with the payload delivered for its requestId', async () => {
    const c = createResponseCorrelator()
    const p = c.waitFor('req-1')
    const matched = c.deliver('req-1', { id: 'msg-1', content: 'hi' })
    expect(matched).toBe(true)
    await expect(p).resolves.toEqual({ id: 'msg-1', content: 'hi' })
  })

  it('routes each reply to the matching requestId only', async () => {
    const c = createResponseCorrelator()
    const p1 = c.waitFor('req-1')
    const p2 = c.waitFor('req-2')
    c.deliver('req-2', { which: 2 })
    c.deliver('req-1', { which: 1 })
    await expect(p1).resolves.toEqual({ which: 1 })
    await expect(p2).resolves.toEqual({ which: 2 })
  })

  it('returns false when delivering to a requestId with no waiter', () => {
    const c = createResponseCorrelator()
    expect(c.deliver('nobody', { x: 1 })).toBe(false)
  })

  it('ignores a duplicate delivery after the waiter already resolved', async () => {
    const c = createResponseCorrelator()
    const p = c.waitFor('req-1')
    expect(c.deliver('req-1', { v: 'first' })).toBe(true)
    await p
    expect(c.deliver('req-1', { v: 'second' })).toBe(false)
  })

  it('rejects a waiter with a timeout-kinded error when no reply arrives', async () => {
    vi.useFakeTimers()
    try {
      const c = createResponseCorrelator()
      const p = c.waitFor('req-1', { timeout: 1000 })
      const assertion = expect(p).rejects.toMatchObject({ kind: RESPONSE_TIMEOUT_KIND })
      await vi.advanceTimersByTimeAsync(1000)
      await assertion
      // A late delivery after timeout must not throw and must report unmatched.
      expect(c.deliver('req-1', { late: true })).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not reject after a delivery clears the timeout', async () => {
    vi.useFakeTimers()
    try {
      const c = createResponseCorrelator()
      const p = c.waitFor('req-1', { timeout: 1000 })
      c.deliver('req-1', { ok: true })
      await expect(p).resolves.toEqual({ ok: true })
      // Advancing past the original timeout must not produce a late rejection.
      await vi.advanceTimersByTimeAsync(2000)
    } finally {
      vi.useRealTimers()
    }
  })

  it('rejectAll rejects every pending waiter', async () => {
    const c = createResponseCorrelator()
    const p1 = c.waitFor('req-1')
    const p2 = c.waitFor('req-2')
    c.rejectAll(new Error('connection closed'))
    await expect(p1).rejects.toThrow('connection closed')
    await expect(p2).rejects.toThrow('connection closed')
    // Registry is emptied — a subsequent delivery finds no waiter.
    expect(c.deliver('req-1', {})).toBe(false)
  })
})
