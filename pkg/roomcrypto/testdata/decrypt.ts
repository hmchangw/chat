import { readFileSync } from 'fs';

interface EncryptedMessage {
  ephemeralPublicKey: string; // base64-encoded 65-byte uncompressed P-256 point
  nonce: string;              // base64-encoded 12-byte AES-GCM nonce
  ciphertext: string;         // base64-encoded ciphertext + 16-byte GCM tag
}

interface DecryptPayload {
  privateKey: string;   // base64 of 32-byte P-256 scalar (from Go privKey.Bytes())
  publicKey: string;    // base64 of 65-byte uncompressed point (from Go pubKey.Bytes())
  message: EncryptedMessage;
}

// Convert a Buffer to base64url encoding (required by the JWK spec).
function toBase64Url(buf: Buffer): string {
  return buf.toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

async function decryptMessage(payload: DecryptPayload): Promise<string> {
  const privKeyBytes = Buffer.from(payload.privateKey, 'base64');
  const pubKeyBytes  = Buffer.from(payload.publicKey, 'base64');

  // Go's privKey.Bytes() is the raw 32-byte P-256 scalar.
  // Go's pubKey.Bytes() is the 65-byte uncompressed point: 0x04 || x (32) || y (32).
  // Web Crypto requires JWK format for private key import.
  const jwkPrivate: JsonWebKey = {
    kty: 'EC',
    crv: 'P-256',
    d: toBase64Url(privKeyBytes),               // private scalar
    x: toBase64Url(pubKeyBytes.slice(1, 33)),   // X coordinate
    y: toBase64Url(pubKeyBytes.slice(33, 65)),  // Y coordinate
  };

  const roomPrivKey = await crypto.subtle.importKey(
    'jwk',
    jwkPrivate,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    ['deriveBits'],
  );

  // Import the ephemeral public key from the EncryptedMessage.
  // Public keys must use keyUsages: [] — passing any usage throws DataError.
  const ephKeyBytes = Buffer.from(payload.message.ephemeralPublicKey, 'base64');
  const jwkEph: JsonWebKey = {
    kty: 'EC',
    crv: 'P-256',
    x: toBase64Url(ephKeyBytes.slice(1, 33)),
    y: toBase64Url(ephKeyBytes.slice(33, 65)),
  };

  const ephPubKey = await crypto.subtle.importKey(
    'jwk',
    jwkEph,
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    [],
  );

  // ECDH: derive 32-byte shared secret.
  const sharedSecretBits = await crypto.subtle.deriveBits(
    { name: 'ECDH', public: ephPubKey },
    roomPrivKey,
    256,
  );

  // Import the shared secret as an HKDF key.
  const hkdfKey = await crypto.subtle.importKey(
    'raw',
    sharedSecretBits,
    'HKDF',
    false,
    ['deriveKey'],
  );

  // HKDF-SHA256: derive AES-256-GCM key.
  // salt=new Uint8Array(0) matches Go's nil salt — RFC 5869 §2.2: null salt ≡ zero-length salt.
  const aesKey = await crypto.subtle.deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: new TextEncoder().encode('room-message-encryption'),
    },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['decrypt'],
  );

  // AES-256-GCM decrypt.
  // ciphertext already includes the 16-byte GCM tag appended by Go's gcm.Seal.
  // AAD is omitted — matches Go's nil AAD.
  const nonce      = Buffer.from(payload.message.nonce,      'base64');
  const ciphertext = Buffer.from(payload.message.ciphertext, 'base64');

  const plaintext = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: nonce },
    aesKey,
    ciphertext,
  );

  return new TextDecoder().decode(plaintext);
}

async function main(): Promise<void> {
  const payloadPath = process.argv[2];
  if (!payloadPath) {
    process.stderr.write('usage: tsx decrypt.ts <payload-file>\n');
    process.exit(1);
  }

  const payload: DecryptPayload = JSON.parse(readFileSync(payloadPath, 'utf8'));
  const plaintext = await decryptMessage(payload);
  // Use process.stdout.write (not console.log) to avoid adding an extra newline.
  process.stdout.write(plaintext);
}

main().catch((err: unknown) => {
  process.stderr.write(`error: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exit(1);
});
