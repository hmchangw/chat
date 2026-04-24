import { useEffect, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { searchMessages, searchRooms } from '../lib/subjects'
import { roomPrefix } from '../lib/roomFormat'

const TAB_MESSAGES = 'messages'
const TAB_ROOMS = 'rooms'
const PAGE_SIZE = 20
const DEBOUNCE_MS = 300

function formatTimestamp(value) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export default function SearchDialog({ currentRoom, onClose, onSelectRoom }) {
  const { user, request } = useNats()
  const [tab, setTab] = useState(TAB_MESSAGES)
  const [text, setText] = useState('')
  const [scopeToRoom, setScopeToRoom] = useState(false)
  const [roomScope, setRoomScope] = useState('all')
  const [results, setResults] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  useEffect(() => {
    setResults(null)
    setError(null)
    const q = text.trim()
    if (!q || !user) {
      setLoading(false)
      return
    }

    let cancelled = false
    setLoading(true)

    const timer = setTimeout(async () => {
      try {
        let subject
        let body
        if (tab === TAB_MESSAGES) {
          subject = searchMessages(user.account)
          body = { searchText: q, size: PAGE_SIZE }
          if (scopeToRoom && currentRoom) {
            body.roomIds = [currentRoom.id]
          }
        } else {
          subject = searchRooms(user.account)
          body = { searchText: q, scope: roomScope, size: PAGE_SIZE }
        }
        const resp = await request(subject, body)
        if (!cancelled) setResults(resp)
      } catch (err) {
        if (!cancelled) setError(err.message)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }, DEBOUNCE_MS)

    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [text, tab, scopeToRoom, roomScope, currentRoom, user, request])

  const handlePickRoom = (hit) => {
    onSelectRoom({
      id: hit.roomId,
      name: hit.roomName,
      type: hit.roomType,
      siteId: hit.siteId,
    })
    onClose()
  }

  const handlePickMessage = (hit) => {
    onSelectRoom({
      id: hit.roomId,
      siteId: hit.siteId,
    })
    onClose()
  }

  const hits = results?.results ?? []
  const total = results?.total ?? 0

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div
        className="dialog search-dialog"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="search-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={tab === TAB_MESSAGES}
            className={
              'search-tab' +
              (tab === TAB_MESSAGES ? ' search-tab-active' : '')
            }
            onClick={() => setTab(TAB_MESSAGES)}
          >
            Messages
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tab === TAB_ROOMS}
            className={
              'search-tab' + (tab === TAB_ROOMS ? ' search-tab-active' : '')
            }
            onClick={() => setTab(TAB_ROOMS)}
          >
            Rooms
          </button>
        </div>

        <input
          className="search-input"
          type="text"
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={
            tab === TAB_MESSAGES ? 'Search messages…' : 'Search rooms…'
          }
          aria-label="Search"
          autoFocus
        />

        {tab === TAB_MESSAGES && currentRoom && (
          <label className="search-option">
            <input
              type="checkbox"
              checked={scopeToRoom}
              onChange={(e) => setScopeToRoom(e.target.checked)}
            />
            Only in {roomPrefix(currentRoom.type)}
            {currentRoom.name}
          </label>
        )}

        {tab === TAB_ROOMS && (
          <select
            className="search-scope"
            aria-label="Room scope"
            value={roomScope}
            onChange={(e) => setRoomScope(e.target.value)}
          >
            <option value="all">All</option>
            <option value="channel">Channels</option>
            <option value="dm">DMs</option>
          </select>
        )}

        <div className="search-results">
          {loading && <div className="search-loading">Searching…</div>}
          {error && <div className="search-error">{error}</div>}
          {!loading && !error && results && hits.length === 0 && (
            <div className="search-empty">No results</div>
          )}
          {!loading && !error && hits.length > 0 && (
            <>
              <div className="search-meta">
                {total} result{total === 1 ? '' : 's'}
              </div>
              {tab === TAB_MESSAGES ? (
                <ul className="search-list">
                  {hits.map((hit) => (
                    <li
                      key={hit.messageId}
                      className="search-item search-item-message"
                      onClick={() => handlePickMessage(hit)}
                    >
                      <div className="search-item-header">
                        <span className="search-item-sender">
                          {hit.userAccount}
                        </span>
                        <span className="search-item-time">
                          {formatTimestamp(hit.createdAt)}
                        </span>
                      </div>
                      <div className="search-item-room">in {hit.roomId}</div>
                      <div className="search-item-content">{hit.content}</div>
                    </li>
                  ))}
                </ul>
              ) : (
                <ul className="search-list">
                  {hits.map((hit) => (
                    <li
                      key={hit.roomId}
                      className="search-item search-item-room"
                      onClick={() => handlePickRoom(hit)}
                    >
                      <span className="search-item-name">
                        {roomPrefix(hit.roomType)}
                        {hit.roomName}
                      </span>
                      <span className="search-item-site">{hit.siteId}</span>
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
        </div>

        <div className="dialog-actions">
          <button
            type="button"
            className="dialog-cancel"
            onClick={onClose}
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
