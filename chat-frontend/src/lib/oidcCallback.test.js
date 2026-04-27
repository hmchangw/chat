import { describe, it, expect } from 'vitest'
import { isOidcCallbackUrl, stripOidcCallbackParams } from './oidcCallback'

describe('isOidcCallbackUrl', () => {
  it('returns true when both code and state are present', () => {
    expect(isOidcCallbackUrl('http://localhost:3000/?code=abc&state=xyz')).toBe(true)
  })

  it('returns true when code, state, and session_state are present', () => {
    const url = 'http://localhost:3000/?code=abc&state=xyz&session_state=s1'
    expect(isOidcCallbackUrl(url)).toBe(true)
  })

  it('returns true for error callbacks', () => {
    const url = 'http://localhost:3000/?error=access_denied&state=xyz'
    expect(isOidcCallbackUrl(url)).toBe(true)
  })

  it('returns false when only code is present without state', () => {
    expect(isOidcCallbackUrl('http://localhost:3000/?code=abc')).toBe(false)
  })

  it('returns false for a plain URL', () => {
    expect(isOidcCallbackUrl('http://localhost:3000/')).toBe(false)
  })
})

describe('stripOidcCallbackParams', () => {
  it('removes code, state, session_state, error, error_description', () => {
    const url =
      'http://localhost:3000/?code=abc&state=xyz&session_state=s1&error=e&error_description=d&keep=yes'
    expect(stripOidcCallbackParams(url)).toBe('http://localhost:3000/?keep=yes')
  })

  it('returns URL without query when no params remain', () => {
    const url = 'http://localhost:3000/?code=abc&state=xyz'
    expect(stripOidcCallbackParams(url)).toBe('http://localhost:3000/')
  })

  it('preserves the path', () => {
    const url = 'http://localhost:3000/chat?code=abc&state=xyz'
    expect(stripOidcCallbackParams(url)).toBe('http://localhost:3000/chat')
  })
})
