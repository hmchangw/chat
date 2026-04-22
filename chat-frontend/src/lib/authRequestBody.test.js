import { describe, it, expect } from 'vitest'
import { buildAuthRequestBody } from './authRequestBody'

describe('buildAuthRequestBody', () => {
  it('builds dev body when given an account', () => {
    const body = buildAuthRequestBody({ account: 'alice', natsPublicKey: 'UABC' })
    expect(body).toEqual({ account: 'alice', natsPublicKey: 'UABC' })
  })

  it('builds oidc body when given an ssoToken', () => {
    const body = buildAuthRequestBody({ ssoToken: 'tok-123', natsPublicKey: 'UABC' })
    expect(body).toEqual({ ssoToken: 'tok-123', natsPublicKey: 'UABC' })
  })

  it('prefers ssoToken when both are provided', () => {
    const body = buildAuthRequestBody({
      account: 'alice',
      ssoToken: 'tok-123',
      natsPublicKey: 'UABC',
    })
    expect(body).toEqual({ ssoToken: 'tok-123', natsPublicKey: 'UABC' })
  })

  it('throws when neither account nor ssoToken is provided', () => {
    expect(() => buildAuthRequestBody({ natsPublicKey: 'UABC' })).toThrow(
      /account or ssoToken/,
    )
  })

  it('throws when natsPublicKey is missing', () => {
    expect(() => buildAuthRequestBody({ account: 'alice' })).toThrow(/natsPublicKey/)
  })
})
