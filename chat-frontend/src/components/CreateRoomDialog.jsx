import { useState } from 'react'
import { useNats } from '../context/NatsContext'
import { roomCreate } from '../lib/subjects'
import { isDMExistsReply } from '../lib/constants'
import { formatAsyncJobError } from '../lib/asyncJob'
import MemberPicker from './MemberPicker'

// Type inferred server-side from payload shape (name vs. members).
export default function CreateRoomDialog({ onClose, onCreated }) {
  const { user, requestWithAsyncResult } = useNats()
  const [name, setName] = useState('')
  const [users, setUsers] = useState([])
  const [orgs, setOrgs] = useState([])
  const [channels, setChannels] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const trimmedName = name.trim()
  const canSubmit =
    !!trimmedName || users.length + orgs.length + channels.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    try {
      const { sync } = await requestWithAsyncResult(
        roomCreate(user.account, user.siteId),
        { name: trimmedName, users, orgs, channels },
        { treatAsSuccess: isDMExistsReply }
      )
      const roomId = sync.roomId
      // On the dedup branch the server only tells us the existing roomId, not
      // its type — could be either dm or botDM. Default to 'dm'; the canonical
      // type arrives shortly via subscription.update.
      const roomType = sync.roomType || (isDMExistsReply(sync) ? 'dm' : undefined)
      // For DM/BotDM the name is empty; fall back to the counterpart account
      // so the sidebar + header have something to show until the canonical
      // name arrives via subscription.update.
      const displayName = trimmedName || users[0] || ''
      onCreated({ id: roomId, type: roomType, siteId: user.siteId, name: displayName })
      onClose()
    } catch (err) {
      setError(formatAsyncJobError(err))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div className="dialog create-room-dialog" onClick={(e) => e.stopPropagation()}>
        <h2>Create Room</h2>
        <form onSubmit={handleSubmit}>
          <label htmlFor="room-name">Name (channel) or leave empty for a DM</label>
          <input
            id="room-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. frontend-team"
            autoFocus
            disabled={loading}
          />

          <MemberPicker
            users={users}
            orgs={orgs}
            channels={channels}
            onUsersChange={setUsers}
            onOrgsChange={setOrgs}
            onChannelsChange={setChannels}
            disabled={loading}
          />

          {error && <div className="dialog-error">{error}</div>}

          <div className="dialog-actions">
            <button type="button" className="dialog-cancel" onClick={onClose} disabled={loading}>
              Cancel
            </button>
            <button type="submit" disabled={loading || !canSubmit}>
              {loading ? 'Creating…' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
