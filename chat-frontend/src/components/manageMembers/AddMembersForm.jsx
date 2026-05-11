import { useEffect, useRef, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberAdd } from '../../lib/subjects'
import { HISTORY_MODE_ALL, HISTORY_MODE_NONE } from '../../lib/constants'
import { formatAsyncJobError } from '../../lib/asyncJob'
import MemberPicker from '../MemberPicker'

const SUCCESS_BANNER_MS = 3000

export default function AddMembersForm({ room }) {
  const { user, requestWithAsyncResult } = useNats()
  const [users, setUsers] = useState([])
  const [orgs, setOrgs] = useState([])
  const [channels, setChannels] = useState([])
  const [shareHistory, setShareHistory] = useState(true)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)
  const successTimer = useRef(null)

  useEffect(() => {
    return () => {
      if (successTimer.current) clearTimeout(successTimer.current)
    }
  }, [])

  const canSubmit = users.length + orgs.length + channels.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    setSuccess(false)
    try {
      await requestWithAsyncResult(memberAdd(user.account, room.id, room.siteId), {
        roomId: room.id,
        users,
        orgs,
        channels,
        history: { mode: shareHistory ? HISTORY_MODE_ALL : HISTORY_MODE_NONE },
      })
      setUsers([])
      setOrgs([])
      setChannels([])
      setSuccess(true)
      if (successTimer.current) clearTimeout(successTimer.current)
      successTimer.current = setTimeout(() => setSuccess(false), SUCCESS_BANNER_MS)
    } catch (err) {
      setError(formatAsyncJobError(err))
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleSubmit}>
      <MemberPicker
        users={users}
        orgs={orgs}
        channels={channels}
        onUsersChange={setUsers}
        onOrgsChange={setOrgs}
        onChannelsChange={setChannels}
        disabled={loading}
      />

      <label className="dialog-checkbox">
        <input
          type="checkbox"
          checked={shareHistory}
          onChange={(e) => setShareHistory(e.target.checked)}
          disabled={loading}
        />{' '}
        Share room history with new members
      </label>

      {error && <div className="dialog-error">{error}</div>}
      {success && <div className="dialog-success">Added</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Adding…' : 'Add'}
        </button>
      </div>
    </form>
  )
}
