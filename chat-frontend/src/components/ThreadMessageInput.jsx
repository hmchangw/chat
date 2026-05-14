import { useEffect, useRef, useState } from 'react'
import { useThreadEvents } from '../context/ThreadEventsContext'
import MessageInputForm from './messages/MessageInputForm'

export default function ThreadMessageInput({ quotedTarget, onClearQuote }) {
  const { sendReply, activeParent } = useThreadEvents()
  const [text, setText] = useState('')
  const inputRef = useRef(null)

  // Auto-focus on staged-quote change so a Reply-in-thread click jumps the
  // cursor straight into the input without an extra click.
  useEffect(() => {
    if (quotedTarget?.id) {
      inputRef.current?.focus()
    }
  }, [quotedTarget?.id])

  const handleSubmit = () => {
    if (!text.trim() || !activeParent) return
    const opts = quotedTarget ? { quotedParentMessageId: quotedTarget.id } : {}
    const content = text.trim()
    setText('')
    sendReply(content, opts)  // sync; thread context handles _status='failed' on throw
    onClearQuote?.()
  }

  return (
    <MessageInputForm
      inputRef={inputRef}
      value={text}
      onChange={setText}
      onSubmit={handleSubmit}
      placeholder="Reply…"
      disabled={!activeParent}
      quotedTarget={quotedTarget}
      onClearQuote={onClearQuote}
    />
  )
}
