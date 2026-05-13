import { useRef, useState } from 'react'
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
  const pickerRef = useRef(null)

  const trimmedName = name.trim()

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!user) return
    // Auto-commit any typed-but-not-Entered text in the picker so users
    // don't have to remember to press Enter before clicking Create. Use the
    // returned values for this submit because the state updates queued by
    // flushAndGetEntries don't land until the next render.
    const { users: finalUsers, orgs: finalOrgs, channels: finalChannels } =
      pickerRef.current?.flushAndGetEntries() ?? { users, orgs, channels }
    if (!trimmedName && finalUsers.length + finalOrgs.length + finalChannels.length === 0) return
    setLoading(true)
    setError(null)
    try {
      const { sync } = await requestWithAsyncResult(
        roomCreate(user.account, user.siteId),
        { name: trimmedName, users: finalUsers, orgs: finalOrgs, channels: finalChannels },
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
      const displayName = trimmedName || finalUsers[0] || ''
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
            ref={pickerRef}
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
            <button type="submit" disabled={loading}>
              {loading ? 'Creating…' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
