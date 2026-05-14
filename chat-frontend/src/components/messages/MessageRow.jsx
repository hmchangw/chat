import { useEffect, useState } from 'react'
import MessageActionMenu from '../MessageActionMenu'
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

  if (message.deleted) {
    return (
      <div className="message-row message-row-deleted" data-message-id={message.id} tabIndex={0}>
        <div className="message-content message-content-deleted">[message deleted]</div>
      </div>
    )
  }

  return (
    <div
      className={`message-row${editing ? ' message-row-editing' : ''}`}
      data-message-id={message.id}
      tabIndex={0}
    >
      {message.quotedParentMessage && (
        <QuotedBlock
          variant="bubble"
          snapshot={message.quotedParentMessage}
          onClick={onJumpToMessage}
        />
      )}
      <div className="message-header">
        <span className="message-sender">{senderName(message)}</span>
        <span className="message-time">{formatTime(message.createdAt)}</span>
        {message.editedAt && <span className="message-edited"> (edited)</span>}
      </div>
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
        <div className="message-content">{messageContent(message)}</div>
      )}
      {message.tcount > 0 && context !== 'thread' && context !== 'thread-parent' && (
        <button
          type="button"
          className="message-reply-badge"
          onClick={() => onThread?.(message)}
        >
          💬 {message.tcount} {message.tcount === 1 ? 'reply' : 'replies'}
        </button>
      )}
      {!editing && (
        <MessageActions
          message={message}
          context={context}
          isOwn={isOwn}
          onThread={onThread}
          onReply={onReply}
          onEdit={onEdit}
          onDelete={onDelete}
        />
      )}
      {/* Read-receipt kebab — separate from MessageActions; rendered in non-edit state. */}
      {!editing && <MessageActionMenu message={message} room={room} />}
      {message._status === 'failed' && !editing && (
        <div className="message-row-failed">
          <span className="message-row-failed-label">Failed to send.</span>
          <button type="button" aria-label="Retry sending message" onClick={() => onRetry?.(message.id)}>⟳</button>
          <button type="button" aria-label="Dismiss failed message" onClick={() => onDismiss?.(message.id)}>✕</button>
        </div>
      )}
    </div>
  )
}
