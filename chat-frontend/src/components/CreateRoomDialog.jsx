import { useState } from 'react'
import { useNats } from '../context/NatsContext'

export default function CreateRoomDialog({ onClose, onCreated }) {
  const { user, request } = useNats()
  const [name, setName] = useState('')
  const [roomType, setRoomType] = useState('group')
  const [members, setMembers] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!name.trim() || !user) return

    setLoading(true)
    setError(null)

    const account = user.account
    const siteId = user.siteId

    const memberList = members
      .split(',')
      .map((m) => m.trim())
      .filter(Boolean)

    try {
      const room = await request(`chat.user.${account}.request.rooms.create`, {
        name: name.trim(),
        type: roomType,
        createdBy: account,
        createdByAccount: account,
        siteId: siteId,
        members: memberList,
      })
      onCreated(room)
      onClose()
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div className="dialog" onClick={(e) => e.stopPropagation()}>
        <h2>Create Room</h2>
        <form onSubmit={handleSubmit}>
          <label htmlFor="room-name">Room Name</label>
          <input
            id="room-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. frontend-team"
            autoFocus
            disabled={loading}
          />

          <label htmlFor="room-type">Type</label>
          <select
            id="room-type"
            value={roomType}
            onChange={(e) => setRoomType(e.target.value)}
            disabled={loading}
          >
            <option value="group">Group</option>
            <option value="dm">DM</option>
          </select>

          <label htmlFor="room-members">Members (comma-separated accounts)</label>
          <input
            id="room-members"
            type="text"
            value={members}
            onChange={(e) => setMembers(e.target.value)}
            placeholder="e.g. bob, charlie"
            disabled={loading}
          />

          {error && <div className="dialog-error">{error}</div>}

          <div className="dialog-actions">
            <button type="button" className="dialog-cancel" onClick={onClose} disabled={loading}>
              Cancel
            </button>
            <button type="submit" disabled={loading || !name.trim()}>
              {loading ? 'Creating...' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
