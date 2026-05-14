export default function MessageActions({
  message, context, isOwn,
  onThread, onReply, onEdit, onDelete,
}) {
  const showThread = context !== 'thread-parent'
  const showReply = context !== 'thread-parent'
  const showEdit = !!isOwn
  const showDelete = !!isOwn

  return (
    <div className="message-actions" role="toolbar">
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
    </div>
  )
}
