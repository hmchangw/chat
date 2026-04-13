import { createContext, useContext, useRef, useState, useCallback } from 'react'
import { connect as natsConnect, StringCodec } from 'nats.ws'

const NatsContext = createContext(null)

const sc = StringCodec()

export function NatsProvider({ children }) {
  const ncRef = useRef(null)
  const [connected, setConnected] = useState(false)
  const [user, setUser] = useState(null)
  const [error, setError] = useState(null)

  const authUrl = import.meta.env.VITE_AUTH_URL || 'http://localhost:8080'
  const natsUrl = import.meta.env.VITE_NATS_URL || 'ws://localhost:4223'

  const connectToNats = useCallback(async (account, siteId) => {
    setError(null)

    // 1. Authenticate with auth-service (dev mode)
    const { createUser } = await import('nkeys.js')
    const nkey = createUser()
    const natsPublicKey = new TextDecoder().decode(nkey.getPublicKey())

    const authResp = await fetch(`${authUrl}/auth`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ account, natsPublicKey }),
    })

    if (!authResp.ok) {
      const body = await authResp.json().catch(() => ({}))
      throw new Error(body.error || `Auth failed: ${authResp.status}`)
    }

    const { natsJwt, user: userInfo } = await authResp.json()

    // 2. Connect to NATS via WebSocket with JWT
    const nc = await natsConnect({
      servers: natsUrl,
      authenticator: {
        authenticate(nonce) {
          const sig = nkey.sign(new TextEncoder().encode(nonce))
          return { nkey: natsPublicKey, sig, jwt: natsJwt }
        },
      },
    })

    ncRef.current = nc
    setUser({ ...userInfo, siteId })
    setConnected(true)

    // Handle unexpected disconnection
    nc.closed().then((err) => {
      if (err) {
        setError(`Disconnected: ${err.message}`)
      }
      setConnected(false)
    })
  }, [authUrl, natsUrl])

  const request = useCallback(async (subject, data = {}) => {
    if (!ncRef.current) throw new Error('Not connected')
    const payload = sc.encode(JSON.stringify(data))
    const resp = await ncRef.current.request(subject, payload, { timeout: 5000 })
    const parsed = JSON.parse(sc.decode(resp.data))
    if (parsed.error) throw new Error(parsed.error)
    return parsed
  }, [])

  const publish = useCallback((subject, data = {}) => {
    if (!ncRef.current) throw new Error('Not connected')
    const payload = sc.encode(JSON.stringify(data))
    ncRef.current.publish(subject, payload)
  }, [])

  const subscribe = useCallback((subject, callback) => {
    if (!ncRef.current) throw new Error('Not connected')
    const sub = ncRef.current.subscribe(subject)
    ;(async () => {
      for await (const msg of sub) {
        try {
          const data = JSON.parse(sc.decode(msg.data))
          callback(data)
        } catch {
          // skip malformed messages
        }
      }
    })()
    return sub
  }, [])

  const disconnect = useCallback(async () => {
    if (ncRef.current) {
      await ncRef.current.drain()
      ncRef.current = null
    }
    setConnected(false)
    setUser(null)
  }, [])

  return (
    <NatsContext.Provider value={{
      connected, user, error,
      connect: connectToNats, request, publish, subscribe, disconnect,
    }}>
      {children}
    </NatsContext.Provider>
  )
}

export function useNats() {
  const ctx = useContext(NatsContext)
  if (!ctx) throw new Error('useNats must be used within NatsProvider')
  return ctx
}
