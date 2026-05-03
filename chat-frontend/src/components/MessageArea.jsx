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
  const { messages, hasLoadedHistory, historyError, loadHistory } = useRoomEvents(room?.id ?? null)
  const bottomRef = useRef(null)
  const [ctrlFOpen, setCtrlFOpen] = useState(false)

  useEffect(() => {
    if (!room) return
    loadHistory().catch(() => {})
  }, [room, loadHistory])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

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

  const handleJumpToMessage = (_msgId) => {
    // jump-to-message anchoring is handled elsewhere; close the strip on click.
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
      <div className="message-list">
        {!hasLoadedHistory && !historyError && <div className="message-loading">Loading messages...</div>}
        {historyError && <div className="message-error">{historyError}</div>}
        {messages.map((msg) => (
          <div key={msg.id} className="message">
            <span className="message-sender">{senderName(msg)}</span>
            <span className="message-time">{formatTime(msg.createdAt)}</span>
            <div className="message-content">{messageContent(msg)}</div>
          </div>
        ))}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}
