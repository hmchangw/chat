import { useState } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from '../context/NatsContext'
import { msgSend } from '../lib/subjects'
import { generateMessageID } from '../lib/idgen'
import { roomPrefix, roomDisplayName } from '../lib/roomFormat'
import MessageInputForm from './messages/MessageInputForm'

export default function RoomMessageInput({ room, quotedTarget, onClearQuote }) {
  const { user, publish } = useNats()
  const [text, setText] = useState('')

  const placeholder = room
    ? `Message ${roomPrefix(room.type)}${roomDisplayName(room)}`
    : 'Select a room...'
  const disabled = !room || !user

  const handleSubmit = () => {
    if (disabled || !text.trim()) return
    const payload = {
      id: generateMessageID(),
      content: text.trim(),
      requestId: uuidv4(),
    }
    if (quotedTarget?.id) payload.quotedParentMessageId = quotedTarget.id
    publish(msgSend(user.account, room.id, user.siteId), payload)
    setText('')
    onClearQuote?.()
  }

  return (
    <MessageInputForm
      value={text}
      onChange={setText}
      onSubmit={handleSubmit}
      placeholder={placeholder}
      disabled={disabled}
      quotedTarget={quotedTarget}
      onClearQuote={onClearQuote}
    />
  )
}
