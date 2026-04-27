import { describe, it, expect } from 'vitest'
import { resolveAuthMode } from './authMode'

describe('resolveAuthMode', () => {
  it('returns "dev" when VITE_AUTH_MODE is unset', () => {
    expect(resolveAuthMode({})).toBe('dev')
  })

  it('returns "dev" when VITE_AUTH_MODE is empty', () => {
    expect(resolveAuthMode({ VITE_AUTH_MODE: '' })).toBe('dev')
  })

  it('returns "dev" when VITE_AUTH_MODE is "dev"', () => {
    expect(resolveAuthMode({ VITE_AUTH_MODE: 'dev' })).toBe('dev')
  })

  it('returns "oidc" when VITE_AUTH_MODE is "oidc"', () => {
    expect(resolveAuthMode({ VITE_AUTH_MODE: 'oidc' })).toBe('oidc')
  })

  it('throws for any other value', () => {
    expect(() => resolveAuthMode({ VITE_AUTH_MODE: 'saml' })).toThrow(
      /VITE_AUTH_MODE must be "dev" or "oidc"/,
    )
  })
})
