// decrypt.ts — invoked by integration_test.go via tsx
import { createDecipheriv, hkdfSync } from 'node:crypto'

type Payload = {
  privateKey: string  // base64 32-byte raw private scalar (high-entropy IKM)
  message: {
    version: number
    nonce: string       // base64
    ciphertext: string  // base64 = content || 16-byte GCM tag
  }
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

  const aesKey = Buffer.from(hkdfSync('sha256', privateKey, Buffer.alloc(0), 'room-message-encryption-v2', 32))
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
