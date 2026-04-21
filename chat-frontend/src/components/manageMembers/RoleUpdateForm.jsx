import { useEffect, useRef, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberRoleUpdate } from '../../lib/subjects'
import { ROLE_OWNER, ROLE_MEMBER } from '../../lib/constants'

export default function RoleUpdateForm({ room }) {
  const { user, request } = useNats()
  const [account, setAccount] = useState('')
  const [newRole, setNewRole] = useState(ROLE_OWNER)
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
      await request(memberRoleUpdate(user.account, room.id, room.siteId), {
        roomId: room.id,
        account: trimmed,
        newRole,
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
      <label htmlFor="role-account">Account</label>
      <input
        id="role-account"
        type="text"
        value={account}
        onChange={(e) => setAccount(e.target.value)}
        placeholder="e.g. bob"
        disabled={loading}
      />

      <label htmlFor="role-newrole">Role</label>
      <select
        id="role-newrole"
        value={newRole}
        onChange={(e) => setNewRole(e.target.value)}
        disabled={loading}
      >
        <option value={ROLE_OWNER}>Owner</option>
        <option value={ROLE_MEMBER}>Member</option>
      </select>

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Updating...' : 'Update Role'}
        </button>
      </div>
    </form>
  )
}
