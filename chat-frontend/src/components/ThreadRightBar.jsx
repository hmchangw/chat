import { useState } from 'react'
import { useThreadEvents } from '../context/ThreadEventsContext'
import ThreadMessageArea from './ThreadMessageArea'
import ThreadMessageInput from './ThreadMessageInput'

export default function ThreadRightBar() {
  const { activeParent, closeThread } = useThreadEvents()
  const [quotedTarget, setQuotedTarget] = useState(null)

  if (!activeParent) return null

  const handleReply = (msg) => {
    setQuotedTarget({
      id: msg.id,
      senderName: msg.sender?.engName || msg.sender?.account || msg.userAccount || 'Unknown',
      content: msg.content || msg.msg || '',
    })
  }

  return (
    <aside className="thread-rightbar">
      <header className="thread-header">
        <span className="thread-header-title">Thread</span>
        <button
          type="button"
          className="thread-header-close"
          aria-label="Close thread"
          onClick={closeThread}
        >
          ✕
        </button>
      </header>
      <ThreadMessageArea onReply={handleReply} />
      <ThreadMessageInput
        quotedTarget={quotedTarget}
        onClearQuote={() => setQuotedTarget(null)}
      />
    </aside>
  )
}
