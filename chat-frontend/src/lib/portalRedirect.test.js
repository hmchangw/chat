import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { followPortalRedirect } from './portalRedirect'

describe('followPortalRedirect', () => {
  const original = window.location
  let assign

  beforeEach(() => {
    assign = vi.fn()
    Object.defineProperty(window, 'location', { writable: true, value: { ...original, assign } })
  })
  afterEach(() => {
    Object.defineProperty(window, 'location', { writable: true, value: original })
  })

  it('navigates and reports true for a redirect payload', () => {
    expect(followPortalRedirect({ redirectTo: 'https://chat-a.example.com' })).toBe(true)
    expect(assign).toHaveBeenCalledWith('https://chat-a.example.com')
  })

  it('reports false and stays put without redirectTo', () => {
    expect(followPortalRedirect({ natsJwt: 'x' })).toBe(false)
    expect(followPortalRedirect(undefined)).toBe(false)
    expect(assign).not.toHaveBeenCalled()
  })
})
