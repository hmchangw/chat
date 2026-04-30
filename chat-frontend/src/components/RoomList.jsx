import { useRoomSummaries } from '../context/RoomEventsContext'
import { useNats } from '../context/NatsContext'
import { roomPrefix } from '../lib/roomFormat'

function mentionBadge(summary) {
  if (summary.mentionAll) return <span className="room-badge-mention-all">!</span>
  if (summary.hasMention) return <span className="room-badge-mention">@</span>
  return null
}

export default function RoomList({ selectedRoomId, onSelectRoom }) {
  const { summaries, error } = useRoomSummaries()
  const { user } = useNats()
  const userSiteId = user?.siteId

  return (
    <div className="room-list">
      <div className="room-list-header">Rooms</div>
      {error && <div className="room-list-error">{error}</div>}
      <div className="room-list-items">
        {summaries.map((room) => {
          const isSelected = room.id === selectedRoomId
          const unread = room.unreadCount > 0
          const isRemote = room.siteId && userSiteId && room.siteId !== userSiteId
          const classes = ['room-item']
          if (isSelected) classes.push('room-item-selected')
          if (unread) classes.push('room-item-unread')
          if (isRemote) classes.push('room-item-remote')
          return (
            <div
              key={room.id}
              className={classes.join(' ')}
              onClick={() => onSelectRoom(room)}
            >
              <span className="room-name">
                {roomPrefix(room.type)}{room.name}
              </span>
              {mentionBadge(room)}
              {isRemote && (
                <span className="room-badge-remote" title={`Federated from ${room.siteId}`}>
                  {room.siteId}
                </span>
              )}
              <span className="room-meta">{room.userCount}</span>
              {unread && <span className="room-badge-unread">{room.unreadCount}</span>}
            </div>
          )
        })}
        {summaries.length === 0 && !error && (
          <div className="room-list-empty">No rooms yet</div>
        )}
      </div>
    </div>
  )
}
