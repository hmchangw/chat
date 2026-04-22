import { useEffect, useState } from 'react'
import { NatsProvider, useNats } from './context/NatsContext'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'
import { AUTH_MODE } from './lib/authMode'
import { completeSigninIfCallback, popSiteId } from './lib/oidc'

function AppContent() {
  const { connected, connect } = useNats()
  const [callbackState, setCallbackState] = useState(
    AUTH_MODE === 'oidc' ? 'pending' : 'idle',
  )
  const [callbackError, setCallbackError] = useState(null)

  useEffect(() => {
    if (AUTH_MODE !== 'oidc') return
    let cancelled = false
    ;(async () => {
      try {
        const user = await completeSigninIfCallback()
        if (cancelled) return
        if (user) {
          const siteId = popSiteId() || import.meta.env.VITE_DEFAULT_SITE_ID || 'site-A'
          await connect({ ssoToken: user.id_token, siteId })
        }
        setCallbackState('idle')
      } catch (err) {
        if (cancelled) return
        setCallbackError(err.message)
        setCallbackState('idle')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [connect])

  if (callbackState === 'pending') {
    return <div className="login-page"><p>Signing in...</p></div>
  }

  if (!connected) {
    return (
      <>
        {callbackError && (
          <div className="login-error" role="alert">
            SSO callback error: {callbackError}
          </div>
        )}
        <LoginPage />
      </>
    )
  }

  return <ChatPage />
}

export default function App() {
  return (
    <NatsProvider>
      <AppContent />
    </NatsProvider>
  )
}
