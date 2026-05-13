import { useNats } from '../context/NatsContext'

export default function MessageActionMenu({ message, room: _room }) {
  const { user } = useNats()
  const isOwnMessage = !!user && message?.sender?.account === user.account
  if (!isOwnMessage) return null

  return (
    <div className="message-action-menu">
      <button
        type="button"
        className="message-action-kebab"
        aria-haspopup="menu"
        aria-expanded={false}
        aria-label="Message actions"
      >
        ⋮
      </button>
    </div>
  )
}
