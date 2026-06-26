import { describe, it, expect, beforeEach, vi } from 'vitest'

describe('runtimeConfig', () => {
  beforeEach(() => {
    vi.resetModules()
    delete window.__APP_CONFIG__
  })

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

  it('AUTH_URL/NATS_URL/SITE_ID default to the local stack', async () => {
    const { AUTH_URL, NATS_URL, SITE_ID } = await import('./runtimeConfig.js')
    expect(AUTH_URL).toBe('http://localhost:8080')
    expect(NATS_URL).toBe('ws://localhost:9222')
    expect(SITE_ID).toBe('site-local')
  })

  it('reads AUTH_URL/NATS_URL/SITE_ID from window.__APP_CONFIG__', async () => {
    window.__APP_CONFIG__ = { AUTH_URL: 'https://auth.a.example.com', NATS_URL: 'wss://nats.a.example.com', SITE_ID: 'site-a' }
    const { AUTH_URL, NATS_URL, SITE_ID } = await import('./runtimeConfig.js')
    expect(AUTH_URL).toBe('https://auth.a.example.com')
    expect(NATS_URL).toBe('wss://nats.a.example.com')
    expect(SITE_ID).toBe('site-a')
  })

  it('no longer exports the retired PORTAL_URL', async () => {
    const mod = await import('./runtimeConfig.js')
    expect(mod.PORTAL_URL).toBeUndefined()
  })
})
