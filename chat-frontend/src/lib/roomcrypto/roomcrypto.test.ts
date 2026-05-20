import { describe, it, expect } from 'vitest'
import { b64decode, decryptRoomMessage, deriveAesKey } from './roomcrypto'
import fixture from '../../../test/fixtures/encrypted-message.json'

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

describe('deriveAesKey', () => {
  it('returns a non-extractable AES-GCM CryptoKey usable for decrypt', async () => {
    const priv = new Uint8Array(32)
    priv.fill(0x42)
    const key = await deriveAesKey(priv)
    expect(key.type).toBe('secret')
    expect(key.algorithm).toMatchObject({ name: 'AES-GCM', length: 256 })
    expect(key.usages).toEqual(['decrypt'])
    expect(key.extractable).toBe(false)
  })

  it('rejects a private key of wrong length', async () => {
    await expect(deriveAesKey(new Uint8Array(31))).rejects.toThrow(/32 bytes/)
  })
})

describe('decryptRoomMessage', () => {
  it('decrypts a fixture produced by the Go server encoder', async () => {
    // Cross-language round-trip via the committed fixture. This exercises
    // the full chain (HKDF derive + AES-GCM open) against bytes that the
    // Go server actually emits — stronger than an inline-encrypt test.
    const aesKey = await deriveAesKey(b64decode(fixture.privateKey))
    const plaintext = await decryptRoomMessage(
      b64decode(fixture.message.ciphertext),
      b64decode(fixture.message.nonce),
      aesKey,
    )
    expect(plaintext).toBe(fixture.plaintext)
  })

  it('throws on tag mismatch', async () => {
    const priv = new Uint8Array(32)
    priv.fill(0x11)
    const aesKey = await deriveAesKey(priv)
    const nonce = new Uint8Array(12)
    const bogusCiphertext = new Uint8Array(32) // all-zero bytes; GCM tag fails
    await expect(decryptRoomMessage(bogusCiphertext, nonce, aesKey)).rejects.toBeDefined()
  })
})
