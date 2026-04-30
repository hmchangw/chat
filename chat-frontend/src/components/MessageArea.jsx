import { useEffect, useRef } from 'react'
import { useRoomEvents } from '../context/RoomEventsContext'
import { useNats } from '../context/NatsContext'
import { roomPrefix } from '../lib/roomFormat'

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
  const { user } = useNats()
  const userSiteId = user?.siteId
  const isRoomRemote = room?.siteId && userSiteId && room.siteId !== userSiteId
  const bottomRef = useRef(null)

  useEffect(() => {
    if (!room) return
    loadHistory().catch(() => {})
  }, [room, loadHistory])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

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
        {isRoomRemote && (
          <span className="room-badge-remote" title={`Federated from ${room.siteId}`}>
            {room.siteId}
          </span>
        )}
        <span className="message-area-members">{room.userCount} members</span>
      </div>
      <div className="message-list">
        {!hasLoadedHistory && !historyError && <div className="message-loading">Loading messages...</div>}
        {historyError && <div className="message-error">{historyError}</div>}
        {messages.map((msg) => {
          const isMsgRemote = msg.siteId && userSiteId && msg.siteId !== userSiteId
          return (
            <div key={msg.id} className="message">
              <span className="message-sender">{senderName(msg)}</span>
              {isMsgRemote && (
                <span className="message-badge-remote" title={`From ${msg.siteId}`}>
                  {msg.siteId}
                </span>
              )}
              <span className="message-time">{formatTime(msg.createdAt)}</span>
              <div className="message-content">{messageContent(msg)}</div>
            </div>
          )
        })}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}
