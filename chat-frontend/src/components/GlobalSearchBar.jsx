import { useEffect, useRef, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { searchRooms } from '../lib/subjects'
import { roomPrefix } from '../lib/roomFormat'

const DEBOUNCE_MS = 250
const DROPDOWN_SIZE = 8

export default function GlobalSearchBar({ onSelectRoom, onSubmit }) {
  const { user, request } = useNats()
  const [text, setText] = useState('')
  const [results, setResults] = useState([])
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const rootRef = useRef(null)

  useEffect(() => {
    const q = text.trim()
    if (!q || !user) {
      setResults([])
      setLoading(false)
      return
    }

    let cancelled = false
    setLoading(true)
    const timer = setTimeout(async () => {
      try {
        const resp = await request(searchRooms(user.account), {
          searchText: q,
          scope: 'all',
          size: DROPDOWN_SIZE,
        })
        if (!cancelled) setResults(resp?.results ?? [])
      } catch {
        if (!cancelled) setResults([])
      } finally {
        if (!cancelled) setLoading(false)
      }
    }, DEBOUNCE_MS)

    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [text, user, request])

  useEffect(() => {
    if (!open) return
    const handler = (e) => {
      if (rootRef.current && !rootRef.current.contains(e.target)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  const submit = () => {
    const q = text.trim()
    if (!q) return
    onSubmit(q)
    setOpen(false)
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      submit()
    } else if (e.key === 'Escape') {
      setOpen(false)
    }
  }

  const handlePick = (hit) => {
    onSelectRoom({
      id: hit.roomId,
      name: hit.roomName,
      type: hit.roomType,
      siteId: hit.siteId,
    })
    setOpen(false)
    setText('')
  }

  const trimmed = text.trim()
  const showDropdown = open && trimmed.length > 0

  return (
    <div className="global-search" ref={rootRef}>
      <input
        type="text"
        className="global-search-input"
        value={text}
        placeholder="Search rooms, people, and messages"
        aria-label="Global search"
        onChange={(e) => {
          setText(e.target.value)
          setOpen(true)
        }}
        onFocus={() => {
          if (trimmed) setOpen(true)
        }}
        onKeyDown={handleKeyDown}
      />
      {showDropdown && (
        <div className="global-search-dropdown" role="listbox">
          {loading && (
            <div className="global-search-loading">Searching…</div>
          )}
          {!loading && results.length === 0 && (
            <div className="global-search-empty">No rooms found</div>
          )}
          {!loading &&
            results.map((hit) => (
              <div
                key={hit.roomId}
                role="option"
                className="global-search-item"
                onClick={() => handlePick(hit)}
              >
                <span className="global-search-item-name">
                  {roomPrefix(hit.roomType)}
                  {hit.roomName}
                </span>
                <span className="global-search-item-site">{hit.siteId}</span>
              </div>
            ))}
          <div className="global-search-submit" onClick={submit}>
            See all results for &ldquo;{trimmed}&rdquo;
          </div>
        </div>
      )}
    </div>
  )
}
