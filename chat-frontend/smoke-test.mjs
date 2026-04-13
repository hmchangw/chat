// Programmatic smoke test for chat-frontend integration
// Tests: auth -> NATS WebSocket connect -> send message -> receive message -> request/reply
import { connect, StringCodec, jwtAuthenticator } from 'nats.ws'
import { createUser } from 'nkeys.js'

const sc = StringCodec()
const AUTH_URL = 'http://localhost:8080'
const NATS_URL = 'ws://localhost:4223'

async function authenticate(account) {
  const nkey = createUser()
  const natsPublicKey = nkey.getPublicKey()

  const resp = await fetch(`${AUTH_URL}/auth`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ account, natsPublicKey }),
  })

  if (!resp.ok) throw new Error(`Auth failed: ${resp.status}`)
  const data = await resp.json()
  return { nkey, natsPublicKey, jwt: data.natsJwt, user: data.user }
}

async function connectNats(auth) {
  const nc = await connect({
    servers: NATS_URL,
    authenticator: jwtAuthenticator(auth.jwt, auth.nkey.getSeed()),
  })
  return nc
}

async function main() {
  console.log('=== Chat Frontend Smoke Test ===\n')

  console.log('1. Authenticating alice...')
  const aliceAuth = await authenticate('alice')
  console.log(`   ✓ Got JWT for ${aliceAuth.user.account} (${aliceAuth.user.email})`)

  console.log('2. Connecting alice to NATS WebSocket...')
  const aliceNc = await connectNats(aliceAuth)
  console.log('   ✓ Connected')

  console.log('3. Authenticating bob...')
  const bobAuth = await authenticate('bob')
  console.log(`   ✓ Got JWT for ${bobAuth.user.account} (${bobAuth.user.email})`)

  console.log('4. Connecting bob to NATS WebSocket...')
  const bobNc = await connectNats(bobAuth)
  console.log('   ✓ Connected')

  console.log('5. Alice subscribes to room events...')
  const roomId = 'test-room-' + Date.now()
  const received = []
  const sub = aliceNc.subscribe(`chat.room.${roomId}.event`)
  ;(async () => {
    for await (const msg of sub) {
      received.push(JSON.parse(sc.decode(msg.data)))
    }
  })()
  console.log(`   ✓ Subscribed to chat.room.${roomId}.event`)

  console.log('6. Bob publishes a message to the room...')
  const testEvent = {
    type: 'new_message',
    roomId,
    message: {
      id: 'msg-' + Date.now(),
      content: 'Hello from bob!',
      sender: { account: 'bob', engName: 'bob' },
      createdAt: new Date().toISOString(),
    },
    timestamp: Date.now(),
  }
  bobNc.publish(`chat.room.${roomId}.event`, sc.encode(JSON.stringify(testEvent)))
  console.log('   ✓ Published')

  console.log('7. Waiting for alice to receive the message...')
  await new Promise(r => setTimeout(r, 500))

  if (received.length > 0) {
    const msg = received[0]
    console.log(`   ✓ Alice received: "${msg.message.content}" from ${msg.message.sender.account}`)
  } else {
    console.log('   ✗ Alice did NOT receive the message')
    process.exitCode = 1
  }

  console.log('8. Testing request/reply pattern...')
  const respSub = aliceNc.subscribe('chat.user.alice.request.rooms.list')
  ;(async () => {
    for await (const msg of respSub) {
      const reply = { rooms: [{ id: roomId, name: 'test-room', type: 'group', userCount: 2 }] }
      msg.respond(sc.encode(JSON.stringify(reply)))
      respSub.unsubscribe()
    }
  })()

  const resp = await aliceNc.request(
    'chat.user.alice.request.rooms.list',
    sc.encode(JSON.stringify({})),
    { timeout: 3000 }
  )
  const rooms = JSON.parse(sc.decode(resp.data))
  if (rooms.rooms && rooms.rooms.length > 0) {
    console.log(`   ✓ Got ${rooms.rooms.length} room(s): "${rooms.rooms[0].name}"`)
  } else {
    console.log('   ✗ Request/reply failed')
    process.exitCode = 1
  }

  console.log('\n9. Cleaning up...')
  sub.unsubscribe()
  await aliceNc.drain()
  await bobNc.drain()
  console.log('   ✓ Connections drained')

  console.log('\n=== Smoke Test Complete ===')
}

main().catch(err => {
  console.error('FAILED:', err.message)
  process.exitCode = 1
})
