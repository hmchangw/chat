import MessageActionMenu from '../MessageActionMenu'

export default function MessageActions({
  message, room, context, isOwn,
  onThread, onReply, onEdit, onDelete,
}) {
  const showThread = context !== 'thread-parent'
  const showReply = context !== 'thread-parent'
  const showEdit = !!isOwn
  const showDelete = !!isOwn

  return (
    <div className="message-actions" role="toolbar" aria-label="Message actions">
      {showThread && (
        <button
          type="button"
          className="message-action message-action-thread"
          aria-label="Reply in thread"
          onClick={() => onThread?.(message)}
        >
          💬
        </button>
      )}
      {showReply && (
        <button
          type="button"
          className="message-action message-action-reply"
          aria-label="Quote this message"
          onClick={() => onReply?.(message)}
        >
          ↩
        </button>
      )}
      {showEdit && (
        <button
          type="button"
          className="message-action message-action-edit"
          aria-label="Edit message"
          onClick={() => onEdit?.(message)}
        >
          ✎
        </button>
      )}
      {showDelete && (
        <button
          type="button"
          className="message-action message-action-delete"
          aria-label="Delete message"
          onClick={() => onDelete?.(message)}
        >
          🗑
        </button>
      )}
      {/* Read-receipt kebab — only renders on own messages (handled inside
          MessageActionMenu). When rendered, it sits as the last button in
          the toolbar so the whole group reveals/dismisses together. */}
      <MessageActionMenu message={message} room={room} />
    </div>
  )
}
