import { useEffect, useRef, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { useThreadEvents } from '../context/ThreadEventsContext'
import { useRoomEvents, useRoomDispatch } from '../context/RoomEventsContext'
import { msgEdit, msgDelete } from '../lib/subjects'
import MessageList from './messages/MessageList'
import DeleteConfirmDialog from './messages/DeleteConfirmDialog'

export default function ThreadMessageArea({ onReply }) {
  const { activeParent, messages, hasLoadedHistory, historyLoading, historyError,
          retryReply, dismissReply, dispatch: threadDispatch } = useThreadEvents()
  const { messages: roomMessages } = useRoomEvents(activeParent?.roomId ?? null)
  const roomDispatch = useRoomDispatch()
  const { user, publish } = useNats()
  const bottomRef = useRef(null)
  const [editingMessageId, setEditingMessageId] = useState(null)
  const [pendingDelete, setPendingDelete] = useState(null)

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

  const isParent = (msgId) => activeParent && msgId === activeParent.messageId

  const handleEdit = (msg) => setEditingMessageId(msg.id)
  const handleEditCancel = () => setEditingMessageId(null)
  const handleEditSubmit = (msg, newContent) => {
    publish(msgEdit(user.account, activeParent.roomId, activeParent.siteId), {
      messageId: msg.id, newMsg: newContent,
    })
    if (isParent(msg.id)) {
      roomDispatch({
        type: 'MESSAGE_EDITED_LOCAL', roomId: activeParent.roomId,
        messageId: msg.id, content: newContent, editedAt: new Date().toISOString(),
      })
    } else {
      threadDispatch({
        type: 'REPLY_EDITED_LOCAL', messageId: msg.id,
        content: newContent, editedAt: new Date().toISOString(),
      })
    }
    setEditingMessageId(null)
  }

  const handleDelete = (msg) => setPendingDelete(msg)
  const handleDeleteCancel = () => setPendingDelete(null)
  const handleDeleteConfirm = () => {
    if (!pendingDelete) return
    publish(msgDelete(user.account, activeParent.roomId, activeParent.siteId), {
      messageId: pendingDelete.id,
    })
    if (isParent(pendingDelete.id)) {
      roomDispatch({ type: 'MESSAGE_DELETED_LOCAL', roomId: activeParent.roomId, messageId: pendingDelete.id })
    } else {
      threadDispatch({ type: 'REPLY_DELETED_LOCAL', messageId: pendingDelete.id })
    }
    setPendingDelete(null)
  }

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
        parentMessageId={activeParent.messageId}
        currentUserAccount={user?.account}
        editingMessageId={editingMessageId}
        emptyText={empty ? 'No replies yet — be the first to reply' : undefined}
        onReply={onReply}
        onEdit={handleEdit}
        onEditSubmit={handleEditSubmit}
        onEditCancel={handleEditCancel}
        onDelete={handleDelete}
        onRetry={retryReply}
        onDismiss={dismissReply}
        bottomRef={bottomRef}
        ariaLive="polite"
      />
      {pendingDelete && (
        <DeleteConfirmDialog onConfirm={handleDeleteConfirm} onCancel={handleDeleteCancel} />
      )}
    </div>
  )
}
