import { useNats } from '../context/NatsContext'
import { memberRemove } from '../lib/subjects'

export default function LeaveRoomButton({ room }) {
  const { user, request } = useNats()

  if (!room || room.type !== 'group') return null

  const handleClick = async () => {
    if (!window.confirm(`Leave "${room.name}"?`)) return
    try {
      await request(memberRemove(user.account, room.id, room.siteId), {
        roomId: room.id,
        account: user.account,
      })
    } catch (err) {
      window.alert(`Failed to leave: ${err.message}`)
    }
  }

  return (
    <button type="button" className="chat-header-logout" onClick={handleClick}>
      Leave
    </button>
  )
}
