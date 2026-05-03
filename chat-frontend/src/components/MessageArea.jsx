import { useEffect, useRef, useState } from 'react'
import { useRoomEvents } from '../context/RoomEventsContext'
import { roomPrefix } from '../lib/roomFormat'
import InRoomSearch from './InRoomSearch'

function formatTime(dateStr) {
  const d = new Date(dateStr)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function senderName(msg) {
  if (msg.sender) {
    return msg.sender.engName || msg.sender.account || msg.sender.userId || 'Unknown'
  }
  return msg.userAccount || msg.userId || 'Unknown'
}

function messageContent(msg) {
  return msg.content || msg.msg || ''
}

export default function MessageArea({ room }) {
  const {
    messages,
    hasLoadedHistory,
    historyError,
    loadHistory,
    bufferMode,
    pendingCount,
    focusMessageId,
    resetToLiveTail,
    jumpToMessage,
  } = useRoomEvents(room?.id ?? null)
  const bottomRef = useRef(null)
  const listRef = useRef(null)
  const [ctrlFOpen, setCtrlFOpen] = useState(false)

  useEffect(() => {
    if (!room) return
    loadHistory().catch(() => {})
  }, [room, loadHistory])

  useEffect(() => {
    if (bufferMode === 'historical') return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, bufferMode])

  useEffect(() => {
    if (bufferMode === 'live') {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [bufferMode])

  useEffect(() => {
    if (!focusMessageId || !listRef.current) return
    const el = listRef.current.querySelector(
      `[data-message-id="${focusMessageId}"]`
    )
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block: 'center' })
    el.classList.add('flash-jump')
    const timer = setTimeout(() => {
      el.classList.remove('flash-jump')
    }, 2000)
    return () => clearTimeout(timer)
  }, [focusMessageId, messages])

  useEffect(() => {
    if (!room) return
    const handler = (e) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.key === 'F')) {
        e.preventDefault()
        setCtrlFOpen(true)
      } else if (e.key === 'Escape') {
        setCtrlFOpen(false)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [room])

  const handleJumpToMessage = (msgId) => {
    if (jumpToMessage) jumpToMessage(msgId)?.catch?.(() => {})
  }

  if (!room) {
    return (
      <div className="message-area">
        <div className="message-area-empty">Select a room to start chatting</div>
      </div>
    )
  }

  return (
    <div className="message-area">
      <div className="message-area-header">
        <span className="message-area-room-name">
          {roomPrefix(room.type)}{room.name}
        </span>
        <span className="message-area-members">{room.userCount} members</span>
      </div>
      {ctrlFOpen && (
        <InRoomSearch
          roomId={room.id}
          onClose={() => setCtrlFOpen(false)}
          onJumpToMessage={handleJumpToMessage}
        />
      )}
      <div className="message-list" ref={listRef}>
        {!hasLoadedHistory && !historyError && <div className="message-loading">Loading messages...</div>}
        {historyError && <div className="message-error">{historyError}</div>}
        {messages.map((msg) => (
          <div key={msg.id} className="message" data-message-id={msg.id}>
            <span className="message-sender">{senderName(msg)}</span>
            <span className="message-time">{formatTime(msg.createdAt)}</span>
            <div className="message-content">{messageContent(msg)}</div>
          </div>
        ))}
        <div ref={bottomRef} />
      </div>
      {bufferMode === 'historical' && pendingCount > 0 && (
        <div className="jump-latest-pill">
          <button type="button" onClick={() => resetToLiveTail()}>
            Jump to latest ({pendingCount} new)
          </button>
        </div>
      )}
    </div>
  )
}
