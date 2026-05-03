import { useEffect, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { searchRooms, searchMessages } from '../lib/subjects'
import { roomFromSearchHit, searchRoomPrefix } from '../lib/roomFormat'

export default function SearchResultsPane({
  query,
  onClose,
  onSelectRoom,
  onJumpToMessage,
}) {
  const { user, request } = useNats()
  const [activeTab, setActiveTab] = useState('rooms')
  const [roomResults, setRoomResults] = useState([])
  const [roomTotal, setRoomTotal] = useState(0)
  const [msgResults, setMsgResults] = useState([])
  const [msgTotal, setMsgTotal] = useState(0)
  const [roomsLoading, setRoomsLoading] = useState(false)
  const [msgsLoading, setMsgsLoading] = useState(false)

  // Fire rooms + messages searches in parallel as soon as the pane opens
  // (or the query changes). Previously messages were lazy-fetched on tab
  // click, which meant the user saw "0 results" for messages until they
  // clicked even when matches existed. Both totals are visible up front
  // now so the tab badges reflect reality and switching is instant.
  useEffect(() => {
    if (!query || !user) return
    let cancelled = false
    setRoomsLoading(true)
    setMsgsLoading(true)

    request(searchRooms(user.account), {
      searchText: query,
      scope: 'all',
      size: 50,
    })
      .then((resp) => {
        if (cancelled) return
        setRoomResults(resp.results ?? [])
        setRoomTotal(resp.total ?? 0)
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setRoomsLoading(false)
      })

    request(searchMessages(user.account), {
      searchText: query,
      size: 50,
    })
      .then((resp) => {
        if (cancelled) return
        setMsgResults(resp.results ?? [])
        setMsgTotal(resp.total ?? 0)
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setMsgsLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [query, user, request])

  const handleRoomClick = (hit) => {
    onSelectRoom(roomFromSearchHit(hit))
    onClose()
  }

  const handleMessageClick = (hit) => {
    onJumpToMessage(hit.roomId, hit.messageId)
    onClose()
  }

  return (
    <div className="search-results-pane">
      <div className="search-results-header">
        <h2>Search Results: "{query}"</h2>
        <button className="search-results-close" onClick={onClose}>
          ✕
        </button>
      </div>

      <div className="search-results-tabs">
        <button
          className={`tab ${activeTab === 'rooms' ? 'active' : ''}`}
          onClick={() => setActiveTab('rooms')}
          role="tab"
          aria-label="Rooms"
        >
          Rooms ({roomTotal})
        </button>
        <button
          className={`tab ${activeTab === 'messages' ? 'active' : ''}`}
          onClick={() => setActiveTab('messages')}
          role="tab"
          aria-label="Messages"
        >
          Messages ({msgTotal})
        </button>
      </div>

      <div className="search-results-content">
        {activeTab === 'rooms' && (
          <div className="room-results">
            {roomsLoading && <div className="loading">Loading rooms...</div>}
            {!roomsLoading && roomResults.length === 0 && (
              <div className="empty">No rooms found</div>
            )}
            {roomResults.map((hit) => (
              <div
                key={hit.roomId}
                className="result-item"
                onClick={() => handleRoomClick(hit)}
              >
                <span className="result-type">
                  {searchRoomPrefix(hit.roomType)}
                </span>
                <span className="result-name">{hit.roomName}</span>
              </div>
            ))}
          </div>
        )}

        {activeTab === 'messages' && (
          <div className="message-results">
            {msgsLoading && <div className="loading">Loading messages...</div>}
            {!msgsLoading && msgResults.length === 0 && (
              <div className="empty">No messages found</div>
            )}
            {msgResults.map((hit) => (
              <div
                key={hit.messageId}
                className="result-item"
                onClick={() => handleMessageClick(hit)}
              >
                <div className="msg-content">{hit.content}</div>
                <div className="msg-meta">
                  {hit.userAccount} · {new Date(hit.createdAt).toLocaleString()}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
