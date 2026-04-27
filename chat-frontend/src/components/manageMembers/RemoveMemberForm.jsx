import { useEffect, useRef, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberRemove } from '../../lib/subjects'

export default function RemoveMemberForm({ room }) {
  const { user, request } = useNats()
  const [account, setAccount] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)
  const successTimer = useRef(null)

  useEffect(() => {
    return () => {
      if (successTimer.current) clearTimeout(successTimer.current)
    }
  }, [])

  const trimmed = account.trim()
  const canSubmit = trimmed.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    setSuccess(false)
    try {
      await request(memberRemove(user.account, room.id, room.siteId), {
        roomId: room.id,
        account: trimmed,
      })
      setAccount('')
      setSuccess(true)
      if (successTimer.current) clearTimeout(successTimer.current)
      successTimer.current = setTimeout(() => setSuccess(false), 3000)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleSubmit}>
      <label htmlFor="remove-account">Account</label>
      <input
        id="remove-account"
        type="text"
        value={account}
        onChange={(e) => setAccount(e.target.value)}
        placeholder="e.g. bob"
        disabled={loading}
      />

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Removing...' : 'Remove'}
        </button>
      </div>
    </form>
  )
}
