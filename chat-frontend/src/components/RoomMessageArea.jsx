import { useEffect, useRef, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { useRoomEvents } from '../context/RoomEventsContext'
import { BUFFER_MODE } from '../lib/roomEventsReducer'
import { msgEdit, msgDelete } from '../lib/subjects'
import MessageList from './messages/MessageList'
import DeleteConfirmDialog from './messages/DeleteConfirmDialog'
import TextInputDialog from './messages/TextInputDialog'

export default function RoomMessageArea({ room, onThread, onReply }) {
  const { user, publish } = useNats()
  const {
    messages,
    hasLoadedHistory,
    historyError,
    loadHistory,
    bufferMode,
    pendingCount,
    focusMessageId,
    resetToLiveTail,
    jumpToMessage,
    dispatch,
  } = useRoomEvents(room?.id ?? null)
  const bottomRef = useRef(null)
  const [editingMessage, setEditingMessage] = useState(null)
  const [pendingDelete, setPendingDelete] = useState(null)

  useEffect(() => { setEditingMessage(null); setPendingDelete(null) }, [room?.id])

  useEffect(() => {
    if (!room) return
    loadHistory().catch(() => {})
  }, [room, loadHistory])

  useEffect(() => {
    if (bufferMode === BUFFER_MODE.HISTORICAL) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, bufferMode])

  useEffect(() => {
    if (bufferMode === BUFFER_MODE.LIVE) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [bufferMode])

  const handleEdit = (msg) => setEditingMessage(msg)
  const handleEditCancel = () => setEditingMessage(null)
  const handleEditSave = (newContent) => {
    if (!editingMessage) return
    // Server: EditMessageRequest{ MessageID, NewMsg }. No createdAt, no requestId.
    publish(msgEdit(user.account, room.id, user.siteId), {
      messageId: editingMessage.id,
      newMsg: newContent,
    })
    dispatch({
      type: 'MESSAGE_EDITED_LOCAL',
      roomId: room.id,
      messageId: editingMessage.id,
      content: newContent,
      editedAt: new Date().toISOString(),
    })
    setEditingMessage(null)
  }

  const handleDelete = (msg) => setPendingDelete(msg)
  const handleDeleteCancel = () => setPendingDelete(null)
  const handleDeleteConfirm = () => {
    if (!pendingDelete) return
    // Server: DeleteMessageRequest{ MessageID }.
    publish(msgDelete(user.account, room.id, user.siteId), {
      messageId: pendingDelete.id,
    })
    dispatch({
      type: 'MESSAGE_DELETED_LOCAL',
      roomId: room.id,
      messageId: pendingDelete.id,
    })
    setPendingDelete(null)
  }

  if (!room) {
    return (
      <div className="message-area">
        <div className="message-area-empty">Select a room to start chatting</div>
      </div>
    )
  }

  return (
    <div className="message-area">
      <MessageList
        messages={messages}
        room={room}
        hasLoadedHistory={hasLoadedHistory}
        historyError={historyError}
        context="main"
        focusMessageId={focusMessageId}
        currentUserAccount={user?.account}
        onThread={onThread}
        onReply={onReply}
        onEdit={handleEdit}
        onDelete={handleDelete}
        onJumpToMessage={(msgId) => jumpToMessage?.(msgId)?.catch?.(() => {})}
        bottomRef={bottomRef}
      />
      {bufferMode === BUFFER_MODE.HISTORICAL && pendingCount > 0 && (
        <div className="jump-latest-pill">
          <button type="button" onClick={() => resetToLiveTail()}>
            Jump to latest ({pendingCount} new)
          </button>
        </div>
      )}
      {pendingDelete && (
        <DeleteConfirmDialog onConfirm={handleDeleteConfirm} onCancel={handleDeleteCancel} />
      )}
      {editingMessage && (
        <TextInputDialog
          title="Edit message"
          initialValue={editingMessage.content || editingMessage.msg || ''}
          confirmLabel="Save"
          onSave={handleEditSave}
          onCancel={handleEditCancel}
        />
      )}
    </div>
  )
}
