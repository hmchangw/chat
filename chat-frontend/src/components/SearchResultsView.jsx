import { useEffect, useState } from 'react'
import { usePager } from '../lib/searchPager'
import { roomPrefix } from '../lib/roomFormat'

const TAB_ROOMS = 'rooms'
const TAB_MESSAGES = 'messages'
const SCROLL_THRESHOLD = 80

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

function RoomsTab({ pager, onPick }) {
  const { hits, total, loading, error, done, load } = pager
  const showEmpty = !loading && !error && hits.length === 0
  return (
    <>
      {total > 0 && (
        <div className="search-meta">
          {total} room{total === 1 ? '' : 's'}
        </div>
      )}
      {showEmpty && <div className="search-empty">No rooms found</div>}
      {error && <div className="search-error">{error}</div>}
      <ul className="search-list">
        {hits.map((hit) => (
          <li
            key={hit.roomId}
            className="search-item search-item-room"
            onClick={() => onPick(hit)}
          >
            <span className="search-item-name">
              {roomPrefix(hit.roomType)}
              {hit.roomName}
            </span>
            <span className="search-item-site">{hit.siteId}</span>
          </li>
        ))}
      </ul>
      {loading && <div className="search-loading">Loading…</div>}
      {!loading && !done && hits.length > 0 && (
        <button
          type="button"
          className="search-loadmore"
          onClick={load}
        >
          Load more
        </button>
      )}
    </>
  )
}

function MessagesTab({ pager, onPick }) {
  const { hits, total, loading, error, done, load } = pager
  const showEmpty = !loading && !error && hits.length === 0
  return (
    <>
      {total > 0 && (
        <div className="search-meta">
          {total} message{total === 1 ? '' : 's'}
        </div>
      )}
      {showEmpty && <div className="search-empty">No messages found</div>}
      {error && <div className="search-error">{error}</div>}
      <ul className="search-list">
        {hits.map((hit) => (
          <li
            key={hit.messageId}
            className="search-item search-item-message"
            onClick={() => onPick(hit)}
          >
            <div className="search-item-header">
              <span className="search-item-sender">{hit.userAccount}</span>
              <span className="search-item-time">
                {formatTimestamp(hit.createdAt)}
              </span>
            </div>
            <div className="search-item-room">in {hit.roomId}</div>
            <div className="search-item-content">{hit.content}</div>
          </li>
        ))}
      </ul>
      {loading && <div className="search-loading">Loading…</div>}
      {!loading && !done && hits.length > 0 && (
        <button
          type="button"
          className="search-loadmore"
          onClick={load}
        >
          Load more
        </button>
      )}
    </>
  )
}

export default function SearchResultsView({ text, onClose, onSelectRoom }) {
  const [tab, setTab] = useState(TAB_ROOMS)
  const rooms = usePager(TAB_ROOMS, text)
  const messages = usePager(TAB_MESSAGES, text)

  // Kick off the rooms search on mount. The pager's startedRef guards
  // against StrictMode double-invocation.
  const roomsStart = rooms.start
  const messagesStart = messages.start
  useEffect(() => {
    roomsStart()
  }, [roomsStart])

  // Lazy-load messages only when the user switches to the messages tab.
  useEffect(() => {
    if (tab === TAB_MESSAGES) messagesStart()
  }, [tab, messagesStart])

  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const active = tab === TAB_ROOMS ? rooms : messages

  const handleScroll = (e) => {
    const el = e.currentTarget
    if (active.loading || active.done) return
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - SCROLL_THRESHOLD) {
      active.load()
    }
  }

  const pickRoom = (hit) => {
    onSelectRoom({
      id: hit.roomId,
      name: hit.roomName,
      type: hit.roomType,
      siteId: hit.siteId,
    })
    onClose()
  }

  const pickMessage = (hit) => {
    onSelectRoom({
      id: hit.roomId,
      siteId: hit.siteId,
    })
    onClose()
  }

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div
        className="dialog search-results-view"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="search-results-header">
          <div className="search-results-query">
            Results for &ldquo;{text}&rdquo;
          </div>
          <button
            type="button"
            className="dialog-cancel search-results-close"
            onClick={onClose}
            aria-label="Close"
          >
            ✕
          </button>
        </div>

        <div className="search-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={tab === TAB_ROOMS}
            className={
              'search-tab' + (tab === TAB_ROOMS ? ' search-tab-active' : '')
            }
            onClick={() => setTab(TAB_ROOMS)}
          >
            Rooms{rooms.total > 0 ? ` (${rooms.total})` : ''}
          </button>
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
            Messages{messages.total > 0 ? ` (${messages.total})` : ''}
          </button>
        </div>

        <div
          className="search-results"
          data-testid="search-results-scroll"
          onScroll={handleScroll}
        >
          {tab === TAB_ROOMS ? (
            <RoomsTab pager={rooms} onPick={pickRoom} />
          ) : (
            <MessagesTab pager={messages} onPick={pickMessage} />
          )}
        </div>
      </div>
    </div>
  )
}
