import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

describe('runtimeConfig', () => {
  const originalLocation = window.location

  beforeEach(() => {
    vi.resetModules()
    vi.unstubAllEnvs()
    delete window.__APP_CONFIG__
  })

  afterEach(() => {
    // Restore window.location after tests that overwrote it.
    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    })
  })

  function setHostname(hostname) {
    Object.defineProperty(window, 'location', {
      value: { ...originalLocation, hostname },
      writable: true,
      configurable: true,
    })
  }

  it('DEV_MODE defaults to true when not overridden', async () => {
    const { DEV_MODE } = await import('./runtimeConfig.js')
    expect(DEV_MODE).toBe(true)
  })

  it('DEV_MODE is false when window.__APP_CONFIG__.DEV_MODE = "false"', async () => {
    window.__APP_CONFIG__ = { DEV_MODE: 'false' }
    const { DEV_MODE } = await import('./runtimeConfig.js')
    expect(DEV_MODE).toBe(false)
  })

  it('OIDC_ISSUER_URL defaults to Keycloak chatapp realm', async () => {
    const { OIDC_ISSUER_URL } = await import('./runtimeConfig.js')
    expect(OIDC_ISSUER_URL).toBe('http://localhost:8180/realms/chatapp')
  })

  it('OIDC_CLIENT_ID defaults to nats-chat', async () => {
    const { OIDC_CLIENT_ID } = await import('./runtimeConfig.js')
    expect(OIDC_CLIENT_ID).toBe('nats-chat')
  })

  it('OIDC_ISSUER_URL reads from window.__APP_CONFIG__', async () => {
    window.__APP_CONFIG__ = { OIDC_ISSUER_URL: 'https://custom-keycloak/realms/myrealm' }
    const { OIDC_ISSUER_URL } = await import('./runtimeConfig.js')
    expect(OIDC_ISSUER_URL).toBe('https://custom-keycloak/realms/myrealm')
  })

  describe('Codespaces auto-detection', () => {
    beforeEach(() => {
      // Ensure no env override leaks in from .env.local during the test.
      vi.stubEnv('VITE_AUTH_URL', '')
      vi.stubEnv('VITE_NATS_URL', '')
    })

    it('AUTH_URL derives from Codespaces hostname when no override', async () => {
      setHostname('my-codespace-abc-3000.app.github.dev')
      const { AUTH_URL } = await import('./runtimeConfig.js')
      expect(AUTH_URL).toBe('https://my-codespace-abc-8080.app.github.dev')
    })

    it('NATS_URL derives from Codespaces hostname as wss', async () => {
      setHostname('my-codespace-abc-3000.app.github.dev')
      const { NATS_URL } = await import('./runtimeConfig.js')
      expect(NATS_URL).toBe('wss://my-codespace-abc-9222.app.github.dev')
    })

    it('explicit VITE_AUTH_URL overrides Codespaces derivation', async () => {
      setHostname('my-codespace-abc-3000.app.github.dev')
      vi.stubEnv('VITE_AUTH_URL', 'http://override:1234')
      const { AUTH_URL } = await import('./runtimeConfig.js')
      expect(AUTH_URL).toBe('http://override:1234')
    })

    it('window.__APP_CONFIG__.AUTH_URL overrides Codespaces derivation', async () => {
      setHostname('my-codespace-abc-3000.app.github.dev')
      window.__APP_CONFIG__ = { AUTH_URL: 'https://prod-auth.example.com' }
      const { AUTH_URL } = await import('./runtimeConfig.js')
      expect(AUTH_URL).toBe('https://prod-auth.example.com')
    })

    it('non-Codespaces hostname falls back to localhost defaults', async () => {
      setHostname('localhost')
      const { AUTH_URL, NATS_URL } = await import('./runtimeConfig.js')
      expect(AUTH_URL).toBe('http://localhost:8080')
      expect(NATS_URL).toBe('ws://localhost:9222')
    })
  })
})

