import { useEffect, useRef } from 'react'
import { useThreadEvents } from '../context/ThreadEventsContext'
import { useRoomEvents } from '../context/RoomEventsContext'
import { useNats } from '../context/NatsContext'
import MessageList from './messages/MessageList'

export default function ThreadMessageArea({ onReply }) {
  const { activeParent, messages, hasLoadedHistory, historyLoading, historyError, retryReply, dismissReply } = useThreadEvents()
  const { messages: roomMessages } = useRoomEvents(activeParent?.roomId ?? null)
  const { user } = useNats()
  const bottomRef = useRef(null)

  // Resolve parent live from main feed buffer; fall back to a thin stub if scrolled out.
  const parent = activeParent
    ? roomMessages.find((m) => m.id === activeParent.messageId) ?? {
        id: activeParent.messageId,
        createdAt: new Date(activeParent.createdAtMs).toISOString(),
        content: '',
      }
    : null

  const combined = parent ? [parent, ...messages] : messages

  // Auto-scroll on history load and on every own optimistic append.
  useEffect(() => {
    if (!hasLoadedHistory) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [hasLoadedHistory])

  useEffect(() => {
    // Pin-to-bottom on append. Any "user scrolled up" finesse is out of scope for v1.
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages.length])

  if (!activeParent) return null

  const empty = hasLoadedHistory && !historyLoading && !historyError && messages.length === 0

  return (
    <div className="thread-message-area">
      <MessageList
        messages={combined}
        hasLoadedHistory={hasLoadedHistory}
        historyLoading={historyLoading}
        historyError={historyError}
        context="thread"
        currentUserAccount={user?.account}
        emptyText={empty ? 'No replies yet — be the first to reply' : undefined}
        onReply={onReply}
        onRetry={retryReply}
        onDismiss={dismissReply}
        bottomRef={bottomRef}
        ariaLive="polite"
      />
    </div>
  )
}
