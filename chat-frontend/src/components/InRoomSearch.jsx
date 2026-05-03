import { useRef, useEffect, useCallback } from 'react'
import { useNats } from '../context/NatsContext'
import { searchMessages } from '../lib/subjects'
import { useDebouncedSearch } from '../lib/useDebouncedSearch'

export default function InRoomSearch({ roomId, onClose, onJumpToMessage }) {
  const { user, request } = useNats()
  const inputRef = useRef(null)

  const fetcher = useCallback(
    async (q) => {
      const resp = await request(searchMessages(user.account), {
        searchText: q,
        roomIds: [roomId],
        size: 20,
      })
      return resp.results ?? []
    },
    [request, user, roomId]
  )

  const { query, results, loading, onChange } = useDebouncedSearch({
    delay: 300,
    minLen: 2,
    fetcher,
  })

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const handleChange = (e) => {
    onChange(e.target.value)
  }

  const handleClick = (hit) => {
    onJumpToMessage(hit.messageId)
    onClose()
  }

  return (
    <div className="in-room-search">
      <div className="in-room-search-header">
        <input
          ref={inputRef}
          type="text"
          className="in-room-search-input"
          value={query}
          onChange={handleChange}
          placeholder="Search messages..."
          aria-label="Search messages in room"
        />
        {query.length >= 2 && (
          <span className="in-room-search-count">
            {loading ? 'Searching...' : `${results.length} results`}
          </span>
        )}
        <button
          type="button"
          className="in-room-search-close"
          onClick={onClose}
          aria-label="Close search"
        >
          ×
        </button>
      </div>
      {results.length > 0 && (
        <div className="in-room-search-results" role="listbox">
          {results.map((hit) => (
            <div
              key={hit.messageId}
              className="in-room-search-result"
              role="option"
              aria-selected="false"
              onClick={() => handleClick(hit)}
            >
              {hit.content}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
