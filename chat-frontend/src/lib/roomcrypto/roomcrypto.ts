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
