// decrypt.ts — invoked by integration_test.go via tsx
import { createHmac, createDecipheriv } from 'node:crypto'

type Payload = {
  privateKey: string  // base64 32-byte raw private scalar (high-entropy IKM)
  message: {
    version: number
    nonce: string       // base64
    ciphertext: string  // base64 = content || 16-byte GCM tag
  }
}

function hkdfSha256(ikm: Buffer, info: Buffer, length: number): Buffer {
  // HKDF-Extract(salt=nil, IKM) = HMAC-SHA-256(0^32, IKM).
  const prk = createHmac('sha256', Buffer.alloc(32)).update(ikm).digest()
  const blocks: Buffer[] = []
  let prev = Buffer.alloc(0)
  const n = Math.ceil(length / 32)
  for (let i = 1; i <= n; i++) {
    const h = createHmac('sha256', prk)
    h.update(prev)
    h.update(info)
    h.update(Buffer.from([i]))
    prev = h.digest()
    blocks.push(prev)
  }
  return Buffer.concat(blocks).subarray(0, length)
}

async function main() {
  const raw = await new Promise<string>((resolve, reject) => {
    let chunks = ''
    process.stdin.setEncoding('utf-8')
    process.stdin.on('data', (c) => (chunks += c))
    process.stdin.on('end', () => resolve(chunks))
    process.stdin.on('error', reject)
  })

  const p = JSON.parse(raw) as Payload
  const privateKey = Buffer.from(p.privateKey, 'base64')
  if (privateKey.length !== 32) throw new Error(`expected 32-byte private key, got ${privateKey.length}`)

  const aesKey = hkdfSha256(privateKey, Buffer.from('room-message-encryption-v2'), 32)
  const nonce = Buffer.from(p.message.nonce, 'base64')
  const ciphertext = Buffer.from(p.message.ciphertext, 'base64')

  // Node's createDecipheriv expects ciphertext and auth tag separately.
  const tag = ciphertext.subarray(ciphertext.length - 16)
  const body = ciphertext.subarray(0, ciphertext.length - 16)

  const decipher = createDecipheriv('aes-256-gcm', aesKey, nonce)
  decipher.setAuthTag(tag)
  const plaintext = Buffer.concat([decipher.update(body), decipher.final()])
  process.stdout.write(plaintext.toString('utf-8'))
}

main().catch((err) => {
  process.stderr.write(`${err.stack ?? err.message ?? err}\n`)
  process.exit(1)
})
