import { useState, useEffect, useRef } from 'react'
import { useNats } from '../context/NatsContext'

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

function messageId(msg) {
  return msg.id || msg.messageId
}

export default function MessageArea({ room }) {
  const { user, request, subscribe } = useNats()
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const bottomRef = useRef(null)
  const subRef = useRef(null)

  useEffect(() => {
    if (!room || !user) return

    const account = user.account
    const siteId = user.siteId

    setMessages([])
    setError(null)
    setLoading(true)

    // Unsubscribe from previous room
    if (subRef.current) {
      subRef.current.unsubscribe()
      subRef.current = null
    }

    // Subscribe to room events for real-time messages
    const sub = subscribe(`chat.room.${room.id}.event`, (evt) => {
      if (evt.type === 'new_message' && evt.message) {
        setMessages((prev) => {
          const id = messageId(evt.message)
          if (prev.some((m) => messageId(m) === id)) return prev
          return [...prev, evt.message]
        })
      }
    })
    subRef.current = sub

    // Load message history
    request(`chat.user.${account}.request.room.${room.id}.${siteId}.msg.history`, {
      limit: 50,
    })
      .then((resp) => {
        // History comes in descending order — reverse for display
        const hist = (resp.messages || []).reverse()
        setMessages(hist)
      })
      .catch((err) => setError(err.message))
      .finally(() => setLoading(false))

    return () => {
      if (subRef.current) {
        subRef.current.unsubscribe()
        subRef.current = null
      }
    }
  }, [room, user, request, subscribe])

  // Auto-scroll to bottom when messages change
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
          {room.type === 'dm' ? '@ ' : '# '}{room.name}
        </span>
        <span className="message-area-members">{room.userCount} members</span>
      </div>
      <div className="message-list">
        {loading && <div className="message-loading">Loading messages...</div>}
        {error && <div className="message-error">{error}</div>}
        {messages.map((msg) => (
          <div key={messageId(msg)} className="message">
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
