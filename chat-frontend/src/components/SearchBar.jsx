import { useState, useRef, useEffect } from 'react'
import { useNats } from '../context/NatsContext'
import { searchRooms } from '../lib/subjects'

export default function SearchBar({ onSelectRoom, onEnterSearch }) {
  const { user, request } = useNats()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState([])
  const [, setLoading] = useState(false)
  const [activeIdx, setActiveIdx] = useState(0)
  const debounceRef = useRef(null)
  const inputRef = useRef(null)

  // Ctrl+K / Cmd+K global shortcut
  useEffect(() => {
    const handler = (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
        e.preventDefault()
        inputRef.current?.focus()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
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
        const resp = await request(searchRooms(user.account), {
          searchText: q,
          scope: 'all',
          size: 8,
        })
        setResults(resp.results ?? [])
      } catch {
        setResults([])
      } finally {
        setLoading(false)
      }
      setActiveIdx(0)
    }, 250)
  }

  const handleKeyDown = (e) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActiveIdx((i) => Math.min(i + 1, results.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIdx((i) => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (query.length >= 2) onEnterSearch(query)
    } else if (e.key === 'Escape') {
      setQuery('')
      setResults([])
      inputRef.current?.blur()
    }
  }

  const handleClick = (hit) => {
    onSelectRoom({
      id: hit.roomId,
      name: hit.roomName,
      type: hit.roomType,
      siteId: hit.siteId,
    })
    setQuery('')
    setResults([])
  }

  return (
    <div className="search-bar-wrap">
      <input
        ref={inputRef}
        type="text"
        className="search-bar"
        value={query}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        placeholder="Search rooms..."
        aria-label="Search rooms"
      />
      {query.length >= 2 && results.length > 0 && (
        <div className="search-dropdown" role="listbox">
          {results.map((hit, idx) => (
            <div
              key={hit.roomId}
              className={`search-result ${idx === activeIdx ? 'active' : ''}`}
              onClick={() => handleClick(hit)}
              role="option"
              aria-selected={idx === activeIdx}
            >
              <div className="result-type">
                {hit.roomType === 'c' ? '#' : '@'}
              </div>
              <div className="result-name">{hit.roomName}</div>
            </div>
          ))}
          <div className="search-footer">
            <span>↑↓ navigate · Enter see all · Esc close</span>
            <span>{results.length} rooms</span>
          </div>
        </div>
      )}
    </div>
  )
}
