import { useEffect, useState } from 'react'
import MessageActions from './MessageActions'
import QuotedBlock from './QuotedBlock'

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

function senderInitial(msg) {
  const name = senderName(msg)
  return (name.charAt(0) || '?').toUpperCase()
}

function messageContent(msg) {
  return msg.content || msg.msg || ''
}

export default function MessageRow({
  message,
  room,
  context,
  isOwn,
  editing,
  onEditSubmit,
  onEditCancel,
  onThread,
  onReply,
  onEdit,
  onDelete,
  onJumpToMessage,
  onRetry,
  onDismiss,
}) {
  const [draft, setDraft] = useState(messageContent(message))

  useEffect(() => {
    setDraft(messageContent(message))
  }, [message, editing])

  // Deleted messages are removed from the feed entirely. (Also filtered at
  // MessageList — this is defense-in-depth in case a caller bypasses the
  // list and renders a row directly.)
  if (message.deleted) return null

  const rowClasses = ['message-row']
  if (editing) rowClasses.push('message-row-editing')
  if (isOwn) rowClasses.push('message-row-own')

  return (
    <div
      className={rowClasses.join(' ')}
      data-message-id={message.id}
      tabIndex={0}
    >
      {/* Avatar only for messages from other users. Own messages don't show
          one — the bubble alone (right-aligned, blue-tinted) is enough to
          identify the sender. */}
      {!isOwn && (
        <div className="message-row-avatar" aria-hidden="true">
          {senderInitial(message)}
        </div>
      )}
      <div className="message-row-body">
        <div className="message-header">
          <span className="message-sender">{senderName(message)}</span>
          <span className="message-time">{formatTime(message.createdAt)}</span>
          {message.editedAt && <span className="message-edited"> (edited)</span>}
        </div>
        {message.quotedParentMessage && (
          <QuotedBlock
            variant="bubble"
            snapshot={message.quotedParentMessage}
            onClick={onJumpToMessage}
          />
        )}
        {/* The bubble + hover-revealed action group share a positioned wrapper.
            Hover/focus on this wrapper is what toggles the actions, NOT hover
            on the whole row — so the visible chrome (avatar / header) stays
            inert as the cursor passes through. */}
        <div className="message-bubble-wrap">
          {editing ? (
            <input
              type="text"
              className="message-edit-input"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  if (draft.trim()) onEditSubmit?.(message, draft.trim())
                } else if (e.key === 'Escape') {
                  e.preventDefault()
                  onEditCancel?.()
                }
              }}
              autoFocus
            />
          ) : (
            <div className="message-bubble">{messageContent(message)}</div>
          )}
          {!editing && (
            <MessageActions
              message={message}
              room={room}
              context={context}
              isOwn={isOwn}
              onThread={onThread}
              onReply={onReply}
              onEdit={onEdit}
              onDelete={onDelete}
            />
          )}
        </div>
        {message.tcount > 0 && context !== 'thread' && context !== 'thread-parent' && (
          <button
            type="button"
            className="message-reply-badge"
            onClick={() => onThread?.(message)}
          >
            💬 {message.tcount} {message.tcount === 1 ? 'reply' : 'replies'}
          </button>
        )}
        {message._status === 'failed' && !editing && (
          <div className="message-row-failed">
            <span className="message-row-failed-label">Failed to send.</span>
            <button type="button" aria-label="Retry sending message" onClick={() => onRetry?.(message.id)}>⟳</button>
            <button type="button" aria-label="Dismiss failed message" onClick={() => onDismiss?.(message.id)}>✕</button>
          </div>
        )}
      </div>
    </div>
  )
}
