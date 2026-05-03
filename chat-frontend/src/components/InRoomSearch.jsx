import { useState, useRef, useEffect } from 'react'
import { useNats } from '../context/NatsContext'
import { searchMessages } from '../lib/subjects'

export default function InRoomSearch({ roomId, onClose, onJumpToMessage }) {
  const { user, request } = useNats()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState([])
  const [, setLoading] = useState(false)
  const debounceRef = useRef(null)
  const inputRef = useRef(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const handleChange = (e) => {
    const q = e.target.value
    setQuery(q)
    clearTimeout(debounceRef.current)

    if (q.length < 2) {
      setResults([])
      return
    }

    debounceRef.current = setTimeout(async () => {
      setLoading(true)
      try {
        const resp = await request(searchMessages(user.account), {
          searchText: q,
          roomIds: [roomId],
          size: 20,
        })
        setResults(resp.results ?? [])
      } catch {
        setResults([])
      } finally {
        setLoading(false)
      }
    }, 300)
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
          <span className="in-room-search-count">{results.length} results</span>
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
