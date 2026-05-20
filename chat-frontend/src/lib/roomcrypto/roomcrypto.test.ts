import { describe, it, expect } from 'vitest'
import { b64decode } from './roomcrypto'

describe('b64decode', () => {
  it('decodes a known base64 string', () => {
    expect(Array.from(b64decode('aGVsbG8='))).toEqual([104, 101, 108, 108, 111])
  })

  it('decodes an empty string to an empty Uint8Array', () => {
    expect(b64decode('').length).toBe(0)
  })

  it('round-trips with btoa', () => {
    const original = new Uint8Array([1, 2, 3, 250])
    const encoded = btoa(String.fromCharCode(...original))
    expect(Array.from(b64decode(encoded))).toEqual([1, 2, 3, 250])
  })
})
