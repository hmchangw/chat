import { useState } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from '../context/NatsContext'
import { msgSend } from '../lib/subjects'

export default function MessageInput({ room }) {
  const { user, publish } = useNats()
  const [text, setText] = useState('')

  const handleSubmit = (e) => {
    e.preventDefault()
    if (!text.trim() || !room || !user) return

    const account = user.account
    const siteId = user.siteId
    publish(msgSend(account, room.id, siteId), {
      id: uuidv4(),
      content: text.trim(),
      requestId: uuidv4(),
    })

    setText('')
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSubmit(e)
    }
  }

  return (
    <form className="message-input" onSubmit={handleSubmit}>
      <input
        type="text"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder={room ? `Message #${room.name}` : 'Select a room...'}
        disabled={!room}
      />
      <button type="submit" disabled={!room || !text.trim()}>
        Send
      </button>
    </form>
  )
}
