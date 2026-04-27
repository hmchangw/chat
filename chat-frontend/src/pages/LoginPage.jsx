import { useState } from 'react'
import { useNats } from '../context/NatsContext'
import { AUTH_MODE } from '../lib/authMode'
import { signinRedirect } from '../lib/oidc'

export default function LoginPage() {
  const { connect, error: natsError } = useNats()
  const defaultSiteId = import.meta.env.VITE_DEFAULT_SITE_ID || 'site-A'

  const [account, setAccount] = useState('')
  const [siteId, setSiteId] = useState(defaultSiteId)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const handleDevSubmit = async (e) => {
    e.preventDefault()
    if (!account.trim()) return
    setLoading(true)
    setError(null)
    try {
      await connect({ account: account.trim(), siteId: siteId.trim() })
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  const handleOidcSignin = async () => {
    setLoading(true)
    setError(null)
    try {
      await signinRedirect(siteId.trim())
    } catch (err) {
      setError(err.message)
      setLoading(false)
    }
  }

  if (AUTH_MODE === 'oidc') {
    return (
      <div className="login-page">
        <form className="login-form" onSubmit={(e) => e.preventDefault()}>
          <h1>Chat</h1>
          <p className="login-subtitle">SSO Login</p>

          <label htmlFor="siteId">Site ID</label>
          <input
            id="siteId"
            type="text"
            value={siteId}
            onChange={(e) => setSiteId(e.target.value)}
            disabled={loading}
          />

          <button type="button" onClick={handleOidcSignin} disabled={loading}>
            {loading ? 'Redirecting...' : 'Login with SSO'}
          </button>

          {(error || natsError) && (
            <div className="login-error">{error || natsError}</div>
          )}
        </form>
      </div>
    )
  }

  return (
    <div className="login-page">
      <form className="login-form" onSubmit={handleDevSubmit}>
        <h1>Chat</h1>
        <p className="login-subtitle">Dev Mode Login</p>

        <label htmlFor="account">Account</label>
        <input
          id="account"
          type="text"
          value={account}
          onChange={(e) => setAccount(e.target.value)}
          placeholder="e.g. alice"
          autoFocus
          disabled={loading}
        />

        <label htmlFor="siteId">Site ID</label>
        <input
          id="siteId"
          type="text"
          value={siteId}
          onChange={(e) => setSiteId(e.target.value)}
          disabled={loading}
        />

        <button type="submit" disabled={loading || !account.trim()}>
          {loading ? 'Connecting...' : 'Connect'}
        </button>

        {(error || natsError) && (
          <div className="login-error">{error || natsError}</div>
        )}
      </form>
    </div>
  )
}
