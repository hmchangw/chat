import { describe, it, expect } from 'vitest'
import { b64decode, deriveAesKey } from './roomcrypto'

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

  it('produces a key that decrypts ciphertext from the matching encryptor', async () => {
    const priv = new Uint8Array(32)
    priv.fill(0x07)
    const k1 = await deriveAesKey(priv)
    const k2 = await deriveAesKey(priv)
    const nonce = new Uint8Array(12)
    crypto.getRandomValues(nonce)
    const plaintext = new TextEncoder().encode('hello')
    // Derive a separate encryption-capable key from the same IKM for the test.
    const encKey = await crypto.subtle.deriveKey(
      { name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(0), info: new TextEncoder().encode('room-message-encryption-v2') },
      await crypto.subtle.importKey('raw', priv, 'HKDF', false, ['deriveKey']),
      { name: 'AES-GCM', length: 256 },
      false,
      ['encrypt'],
    )
    const ct = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv: nonce, tagLength: 128 }, encKey, plaintext))
    // Decrypt with k1 and k2 — both must succeed.
    const pt1 = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: nonce, tagLength: 128 }, k1, ct))
    const pt2 = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: nonce, tagLength: 128 }, k2, ct))
    expect(new TextDecoder().decode(pt1)).toBe('hello')
    expect(new TextDecoder().decode(pt2)).toBe('hello')
  })

  it('rejects a private key of wrong length', async () => {
    await expect(deriveAesKey(new Uint8Array(31))).rejects.toThrow(/32 bytes/)
  })
})
