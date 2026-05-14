import { useEffect, useRef } from 'react'
import MessageRow from './MessageRow'

export default function MessageList({
  messages,
  room,
  hasLoadedHistory,
  historyLoading,
  historyError,
  emptyText,
  context,
  focusMessageId,
  onThread,
  onReply,
  onJumpToMessage,
  bottomRef,
  ariaLive,
}) {
  const listRef = useRef(null)
  const localBottomRef = useRef(null)
  const effectiveBottomRef = bottomRef ?? localBottomRef

  useEffect(() => {
    if (!focusMessageId || !listRef.current) return
    const el = listRef.current.querySelector(`[data-message-id="${focusMessageId}"]`)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block: 'center' })
    el.classList.add('flash-jump')
    const timer = setTimeout(() => el.classList.remove('flash-jump'), 2000)
    return () => clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusMessageId])

  const empty = hasLoadedHistory && !historyLoading && !historyError && messages.length === 0

  return (
    <div
      className="message-list"
      ref={listRef}
      {...(ariaLive ? { 'aria-live': ariaLive } : {})}
    >
      {historyLoading && <div className="message-loading">Loading messages…</div>}
      {historyError && <div className="message-error">{historyError}</div>}
      {messages.map((msg) => (
        <MessageRow
          key={msg.id}
          message={msg}
          room={room}
          context={context}
          onThread={onThread}
          onReply={onReply}
          onJumpToMessage={onJumpToMessage}
        />
      ))}
      {empty && emptyText && <div className="message-empty">{emptyText}</div>}
      <div ref={effectiveBottomRef} />
    </div>
  )
}
