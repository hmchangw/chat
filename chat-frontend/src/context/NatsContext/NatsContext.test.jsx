import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'

const setCredentials = vi.fn()
const stop = vi.fn()
const fakeAuthenticator = { tag: 'dynamic-auth' }
vi.mock('./useJwtRefresh', () => ({
  useJwtRefresh: vi.fn(() => ({ authenticator: fakeAuthenticator, setCredentials, stop })),
}))

const natsConnect = vi.fn()
vi.mock('nats.ws', () => ({
  connect: (...a) => natsConnect(...a),
  StringCodec: () => ({ encode: (s) => s, decode: (s) => s }),
  jwtAuthenticator: vi.fn(),
}))

vi.mock('nkeys.js', () => ({
  createUser: () => ({ getPublicKey: () => 'UPUBKEY', getSeed: () => new Uint8Array([7]) }),
}))

import { NatsProvider, useNats } from './NatsContext'
import { DebugProvider } from '@/context/DebugContext'
import { useJwtRefresh } from './useJwtRefresh'

function wrapper({ children }) {
  return (
    <DebugProvider>
      <NatsProvider>{children}</NatsProvider>
    </DebugProvider>
  )
}

// The provider hands its getAuthUrl getter to useJwtRefresh; reading it back
// through the mock observes what the static getter returns.
function lastGetAuthUrl() {
  return useJwtRefresh.mock.calls.at(-1)[0].getAuthUrl
}

describe('NatsProvider connect wiring', () => {
  beforeEach(() => {
    setCredentials.mockReset()
    stop.mockReset()
    natsConnect.mockReset().mockResolvedValue({ closed: () => new Promise(() => {}) })
    global.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ natsJwt: 'JWT123', user: { account: 'alice' } }) }))
  })
  afterEach(() => { vi.restoreAllMocks() })

  it('auths at the static AUTH_URL and connects to the static NATS_URL', async () => {
    const { result } = renderHook(() => useNats(), { wrapper })
    await act(async () => {
      await result.current.connect({ mode: 'sso', ssoToken: 'tok', account: 'alice' })
    })
    expect(global.fetch).toHaveBeenCalledTimes(1)
    expect(global.fetch).toHaveBeenCalledWith('http://localhost:8080/auth', expect.anything())
    expect(setCredentials).toHaveBeenCalledWith({
      jwt: 'JWT123', seed: new Uint8Array([7]), natsPublicKey: 'UPUBKEY', refreshable: true,
    })
    expect(natsConnect).toHaveBeenCalledWith(
      expect.objectContaining({ servers: 'ws://localhost:9222', authenticator: fakeAuthenticator }))
    await waitFor(() => expect(result.current.connected).toBe(true))
    expect(result.current.user.siteId).toBe('site-local')
    expect(lastGetAuthUrl()()).toBe('http://localhost:8080')
  })

  it('drops a stale nc.closed() callback from a superseded connection (generation guard)', async () => {
    let closeFirst
    const firstClosed = new Promise((res) => { closeFirst = res })
    natsConnect
      .mockResolvedValueOnce({ closed: () => firstClosed })
      .mockResolvedValueOnce({ closed: () => new Promise(() => {}) })

    const { result } = renderHook(() => useNats(), { wrapper })

    await act(async () => {
      await result.current.connect({ mode: 'sso', ssoToken: 'tok', account: 'alice' })
    })
    expect(result.current.connected).toBe(true)

    // A newer connect supersedes the first and bumps the connect generation.
    await act(async () => {
      await result.current.connect({ mode: 'sso', ssoToken: 'tok2', account: 'alice' })
    })
    expect(result.current.connected).toBe(true)

    // The first (now stale) link closes with an error. Its long-lived callback
    // must not clobber the live second session's connected/error state.
    await act(async () => {
      closeFirst(new Error('old link dropped'))
      await firstClosed.catch(() => {})
    })

    expect(result.current.connected).toBe(true)
    expect(result.current.error).toBeNull()
  })

  it('rolls back staged credentials when the NATS dial fails', async () => {
    natsConnect.mockRejectedValue(new Error('handshake refused'))
    const { result } = renderHook(() => useNats(), { wrapper })
    let thrown
    await act(async () => {
      try { await result.current.connect({ mode: 'sso', ssoToken: 'tok', account: 'alice' }) } catch (err) { thrown = err }
    })

    expect(thrown.message).toBe('handshake refused')
    expect(setCredentials).toHaveBeenCalledTimes(1)
    expect(stop).toHaveBeenCalledTimes(1)
    expect(result.current.connected).toBe(false)
    // getAuthUrl is static now; it always returns the configured AUTH_URL.
    expect(lastGetAuthUrl()()).toBe('http://localhost:8080')
  })

  it('dev mode auths at AUTH_URL and is non-refreshable', async () => {
    const { result } = renderHook(() => useNats(), { wrapper })
    await act(async () => {
      await result.current.connect({ mode: 'dev', account: 'alice' })
    })
    expect(global.fetch).toHaveBeenCalledWith('http://localhost:8080/auth', expect.anything())
    expect(setCredentials).toHaveBeenCalledWith(expect.objectContaining({ refreshable: false }))
  })

  it('propagates the auth error envelope and never dials NATS', async () => {
    global.fetch = vi.fn(async () => ({
      ok: false,
      json: async () => ({ code: 'unauthenticated', reason: 'sso_token_expired', error: 'SSO token has expired, please re-login' }),
    }))
    const { result } = renderHook(() => useNats(), { wrapper })
    let thrown
    await act(async () => {
      try { await result.current.connect({ mode: 'sso', ssoToken: 'tok', account: 'alice' }) } catch (err) { thrown = err }
    })
    expect(thrown.reason).toBe('sso_token_expired')
    expect(thrown.message).toBe('SSO token has expired, please re-login')
    expect(global.fetch).toHaveBeenCalledTimes(1)
    expect(natsConnect).not.toHaveBeenCalled()
  })

  it('falls back to a status message when the error body is not JSON', async () => {
    global.fetch = vi.fn(async () => ({
      ok: false,
      status: 502,
      json: async () => { throw new Error('not json') },
    }))
    const { result } = renderHook(() => useNats(), { wrapper })
    let thrown
    await act(async () => {
      try { await result.current.connect({ mode: 'sso', ssoToken: 'tok', account: 'alice' }) } catch (err) { thrown = err }
    })
    expect(thrown.message).toBe('Auth failed: 502')
    expect(thrown.code).toBeUndefined()
    expect(natsConnect).not.toHaveBeenCalled()
  })

  it('stops the refresh loop on disconnect', async () => {
    natsConnect.mockResolvedValue({
      closed: () => new Promise(() => {}),
      drain: vi.fn().mockResolvedValue(),
    })
    const { result } = renderHook(() => useNats(), { wrapper })
    await act(async () => {
      await result.current.connect({ mode: 'sso', ssoToken: 'tok', account: 'alice' })
    })
    await act(async () => { await result.current.disconnect() })
    expect(stop).toHaveBeenCalledTimes(1)
  })
})
