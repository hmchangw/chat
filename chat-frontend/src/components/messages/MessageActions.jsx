export default function MessageActions({ message, context, onThread, onReply }) {
  const showThread = context !== 'thread-parent'
  const showReply = context !== 'thread-parent'

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
    </div>
  )
}
