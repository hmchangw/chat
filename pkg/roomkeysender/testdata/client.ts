import { connect, type NatsConnection, type Msg } from "nats.ws";

// ---- Types matching Go models ----

interface RoomKeyEvent {
  roomId: string;
  version: number;
  publicKey: string;  // base64-encoded 65-byte uncompressed P-256 point
  privateKey: string; // base64-encoded 32-byte scalar
}

interface EncryptedMessage {
  ephemeralPublicKey: string; // base64-encoded 65-byte uncompressed P-256 point
  nonce: string;              // base64-encoded 12-byte AES-GCM nonce
  ciphertext: string;         // base64-encoded ciphertext + 16-byte GCM tag
}

// ---- Helpers ----

function toBase64Url(buf: Buffer): string {
  return buf.toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
}

// ---- Decryption (matches pkg/roomcrypto algorithm) ----

async function decryptMessage(
  encrypted: EncryptedMessage,
  roomPrivateKeyB64: string,
  roomPublicKeyB64: string,
): Promise<string> {
  const privKeyBytes = Buffer.from(roomPrivateKeyB64, "base64");
  const pubKeyBytes = Buffer.from(roomPublicKeyB64, "base64");

  // Import room private key as JWK for ECDH.
  const jwkPrivate: JsonWebKey = {
    kty: "EC",
    crv: "P-256",
    d: toBase64Url(privKeyBytes),
    x: toBase64Url(pubKeyBytes.slice(1, 33)),
    y: toBase64Url(pubKeyBytes.slice(33, 65)),
  };

  const roomPrivKey = await crypto.subtle.importKey(
    "jwk",
    jwkPrivate,
    { name: "ECDH", namedCurve: "P-256" },
    false,
    ["deriveBits"],
  );

  // Import ephemeral public key from encrypted message.
  const ephKeyBytes = Buffer.from(encrypted.ephemeralPublicKey, "base64");
  const jwkEph: JsonWebKey = {
    kty: "EC",
    crv: "P-256",
    x: toBase64Url(ephKeyBytes.slice(1, 33)),
    y: toBase64Url(ephKeyBytes.slice(33, 65)),
  };

  const ephPubKey = await crypto.subtle.importKey(
    "jwk",
    jwkEph,
    { name: "ECDH", namedCurve: "P-256" },
    false,
    [],
  );

  // ECDH: derive shared secret.
  const sharedSecretBits = await crypto.subtle.deriveBits(
    { name: "ECDH", public: ephPubKey },
    roomPrivKey,
    256,
  );

  // HKDF-SHA256: derive AES-256-GCM key.
  const hkdfKey = await crypto.subtle.importKey("raw", sharedSecretBits, "HKDF", false, [
    "deriveKey",
  ]);

  const aesKey = await crypto.subtle.deriveKey(
    {
      name: "HKDF",
      hash: "SHA-256",
      salt: new Uint8Array(0),
      info: new TextEncoder().encode("room-message-encryption"),
    },
    hkdfKey,
    { name: "AES-GCM", length: 256 },
    false,
    ["decrypt"],
  );

  // AES-256-GCM decrypt.
  const nonce = Buffer.from(encrypted.nonce, "base64");
  const ciphertext = Buffer.from(encrypted.ciphertext, "base64");

  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: nonce },
    aesKey,
    ciphertext,
  );

  return new TextDecoder().decode(plaintext);
}

// ---- Main ----

async function main(): Promise<void> {
  const [natsURL, username, roomID] = process.argv.slice(2);
  if (!natsURL || !username || !roomID) {
    process.stderr.write("usage: tsx client.ts <nats-ws-url> <username> <roomID>\n");
    process.exit(1);
  }

  const keySubject = `chat.user.${username}.event.room.key`;
  const msgSubject = `test.room.${roomID}.msg`;

  // Store received keys indexed by version number.
  const keys = new Map<number, { publicKey: string; privateKey: string }>();

  const nc: NatsConnection = await connect({ servers: natsURL });

  // Subscribe to key events.
  const keySub = nc.subscribe(keySubject);
  // Subscribe to encrypted messages.
  const msgSub = nc.subscribe(msgSubject);

  // Process key events in background.
  (async () => {
    for await (const msg of keySub) {
      const evt: RoomKeyEvent = JSON.parse(new TextDecoder().decode(msg.data));
      keys.set(evt.version, { publicKey: evt.publicKey, privateKey: evt.privateKey });
    }
  })();

  // Process encrypted messages — decrypt first one and exit.
  for await (const msg of msgSub) {
    const versionStr = msg.headers?.get("X-Room-Key-Version");
    if (!versionStr) {
      // No header means the message is not encrypted — print raw and exit.
      process.stdout.write(new TextDecoder().decode(msg.data));
      break;
    }

    const version = parseInt(versionStr, 10);
    const keyPair = keys.get(version);
    if (!keyPair) {
      process.stderr.write(`no key found for version ${version}\n`);
      process.exit(1);
    }

    const encrypted: EncryptedMessage = JSON.parse(new TextDecoder().decode(msg.data));
    const plaintext = await decryptMessage(encrypted, keyPair.privateKey, keyPair.publicKey);

    process.stdout.write(plaintext);
    break;
  }

  await nc.drain();
}

main().catch((err: unknown) => {
  process.stderr.write(`error: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exit(1);
});
