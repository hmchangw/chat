/**
 * Decode a standard-base64 string to a Uint8Array.
 *
 * Note: this is base64, not base64url. The server emits standard base64
 * via Go's encoding/json default for []byte fields (StdEncoding).
 */
export function b64decode(s: string): Uint8Array {
  const binary = atob(s)
  const out = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i)
  return out
}

const HKDF_INFO = new TextEncoder().encode('room-message-encryption-v2')
const HKDF_SALT = new Uint8Array(0)

/**
 * Derive an AES-256-GCM CryptoKey from a 32-byte room private key via
 * HKDF-SHA-256 with empty salt and info "room-message-encryption-v2".
 *
 * The returned key is non-extractable and has the single usage 'decrypt'.
 */
export async function deriveAesKey(roomPrivateKey: Uint8Array): Promise<CryptoKey> {
  if (roomPrivateKey.length !== 32) {
    throw new Error(`room private key must be 32 bytes, got ${roomPrivateKey.length}`)
  }
  const ikm = await crypto.subtle.importKey('raw', roomPrivateKey.buffer.slice(roomPrivateKey.byteOffset, roomPrivateKey.byteOffset + roomPrivateKey.byteLength) as ArrayBuffer, 'HKDF', false, ['deriveKey'])
  return crypto.subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt: HKDF_SALT, info: HKDF_INFO },
    ikm,
    { name: 'AES-GCM', length: 256 },
    false,
    ['decrypt'],
  )
}
