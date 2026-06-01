import { describe, it, expect, vi } from 'vitest'
import { StringCodec } from 'nats.ws'
import { startUserResponseSubscription } from './userResponses'
import { createResponseCorrelator } from './responseCorrelator'

const sc = StringCodec()

// Minimal duck-typed nats subscription whose async-iterator yields pushed
// messages. Mirrors the fake in asyncJob.test.js but each message carries a
// `subject` (the wildcard router needs it to recover the requestId).
function makeFakeSub() {
  const buf = []
  let resolveWait
  let closed = false
  return {
    push(subject, obj) {
      buf.push({ subject, data: sc.encode(JSON.stringify(obj)) })
      if (resolveWait) { const r = resolveWait; resolveWait = null; r() }
    },
    pushRaw(subject, bytes) {
      buf.push({ subject, data: bytes })
      if (resolveWait) { const r = resolveWait; resolveWait = null; r() }
    },
    unsubscribe() {
      closed = true
      if (resolveWait) { const r = resolveWait; resolveWait = null; r() }
    },
    [Symbol.asyncIterator]() {
      return {
        async next() {
          while (buf.length === 0 && !closed) {
            await new Promise((r) => { resolveWait = r })
          }
          if (buf.length === 0 && closed) return { value: undefined, done: true }
          return { value: buf.shift(), done: false }
        },
      }
    },
  }
}

function makeNc() {
  const sub = makeFakeSub()
  return { sub, subscribe: vi.fn(() => sub) }
}

const flush = () => new Promise((r) => setTimeout(r, 0))

describe('startUserResponseSubscription', () => {
  it('subscribes to the per-account response wildcard subject', () => {
    const nc = makeNc()
    startUserResponseSubscription(nc, 'alice', createResponseCorrelator())
    expect(nc.subscribe).toHaveBeenCalledWith('chat.user.alice.response.>')
  })

  it('routes a received reply to the correlator keyed by the subject requestId', async () => {
    const nc = makeNc()
    const correlator = createResponseCorrelator()
    startUserResponseSubscription(nc, 'alice', correlator)

    const waiting = correlator.waitFor('019e83b8-cbbe-7db7-9c2f-68f8b4f6ced8')
    nc.sub.push(
      'chat.user.alice.response.019e83b8-cbbe-7db7-9c2f-68f8b4f6ced8',
      { id: 'msg-1', content: 'morning team' },
    )
    await expect(waiting).resolves.toEqual({ id: 'msg-1', content: 'morning team' })
  })

  it('skips a malformed-JSON message without tearing down the loop', async () => {
    const nc = makeNc()
    const correlator = createResponseCorrelator()
    const delivered = vi.spyOn(correlator, 'deliver')
    startUserResponseSubscription(nc, 'alice', correlator)

    nc.sub.pushRaw('chat.user.alice.response.req-bad', sc.encode('{not json'))
    await flush()
    const waiting = correlator.waitFor('req-ok')
    nc.sub.push('chat.user.alice.response.req-ok', { ok: true })
    await expect(waiting).resolves.toEqual({ ok: true })
    // The malformed message must not have reached deliver().
    expect(delivered).not.toHaveBeenCalledWith('req-bad', expect.anything())
  })

  it('returns a handle whose unsubscribe stops the loop', () => {
    const nc = makeNc()
    const handle = startUserResponseSubscription(nc, 'alice', createResponseCorrelator())
    expect(typeof handle.unsubscribe).toBe('function')
    handle.unsubscribe()
  })
})
