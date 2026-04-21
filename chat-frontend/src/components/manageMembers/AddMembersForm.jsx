import { useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberAdd } from '../../lib/subjects'

function parseList(input) {
  return input
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

export default function AddMembersForm({ room }) {
  const { user, request } = useNats()
  const [accounts, setAccounts] = useState('')
  const [orgs, setOrgs] = useState('')
  const [channels, setChannels] = useState('')
  const [shareHistory, setShareHistory] = useState(true)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)

  const users = parseList(accounts)
  const orgList = parseList(orgs)
  const channelList = parseList(channels)
  const canSubmit = users.length + orgList.length + channelList.length > 0

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!canSubmit || !user) return
    setLoading(true)
    setError(null)
    setSuccess(false)
    try {
      await request(memberAdd(user.account, room.id, room.siteId), {
        roomId: room.id,
        users,
        orgs: orgList,
        channels: channelList,
        history: { mode: shareHistory ? 'all' : 'none' },
      })
      setAccounts('')
      setOrgs('')
      setChannels('')
      setSuccess(true)
      setTimeout(() => setSuccess(false), 3000)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleSubmit}>
      <label htmlFor="add-accounts">Accounts (comma-separated)</label>
      <input
        id="add-accounts"
        type="text"
        value={accounts}
        onChange={(e) => setAccounts(e.target.value)}
        placeholder="e.g. bob, charlie"
        disabled={loading}
      />

      <label htmlFor="add-orgs">Orgs (comma-separated)</label>
      <input
        id="add-orgs"
        type="text"
        value={orgs}
        onChange={(e) => setOrgs(e.target.value)}
        placeholder="e.g. eng-frontend"
        disabled={loading}
      />

      <label htmlFor="add-channels">Channels (comma-separated room IDs)</label>
      <input
        id="add-channels"
        type="text"
        value={channels}
        onChange={(e) => setChannels(e.target.value)}
        placeholder="e.g. r-existing"
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
      {success && <div className="dialog-success">Accepted</div>}

      <div className="dialog-actions">
        <button type="submit" disabled={loading || !canSubmit}>
          {loading ? 'Adding...' : 'Add'}
        </button>
      </div>
    </form>
  )
}
