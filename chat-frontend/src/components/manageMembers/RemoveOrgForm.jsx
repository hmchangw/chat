import { useEffect, useRef, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberRemove } from '../../lib/subjects'

export default function RemoveOrgForm({ room }) {
  const { user, request } = useNats()
  const [orgId, setOrgId] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)
  const successTimer = useRef(null)

  useEffect(() => {
    return () => {
      if (successTimer.current) clearTimeout(successTimer.current)
    }
  }, [])

  const trimmed = orgId.trim()
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
        orgId: trimmed,
      })
      setOrgId('')
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
      <label htmlFor="removeorg-orgid">Org ID</label>
      <input
        id="removeorg-orgid"
        type="text"
        value={orgId}
        onChange={(e) => setOrgId(e.target.value)}
        placeholder="e.g. eng-frontend"
        disabled={loading}
      />

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Removing...' : 'Remove Org'}
        </button>
      </div>
    </form>
  )
}
