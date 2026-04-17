import { useState, useEffect, useRef } from 'react'
import { useNats } from '../context/NatsContext'
import { roomsList, roomsGet, subscriptionUpdate, roomMetadataUpdate } from '../lib/subjects'

export default function RoomList({ selectedRoomId, onSelectRoom }) {
  const { user, request, subscribe } = useNats()
  const [rooms, setRooms] = useState([])
  const [error, setError] = useState(null)
  const subsRef = useRef([])

  useEffect(() => {
    if (!user) return

    const account = user.account

    // Fetch initial room list
    request(roomsList(account), {})
      .then((resp) => {
        const sorted = (resp.rooms || []).sort(
          (a, b) => new Date(b.lastMsgAt) - new Date(a.lastMsgAt)
        )
        setRooms(sorted)
      })
      .catch((err) => setError(err.message))

    // Subscribe to subscription updates (room added/removed)
    const subUpdate = subscribe(
      subscriptionUpdate(account),
      (evt) => {
        if (evt.action === 'added') {
          // Fetch the full room details and add to list
          request(roomsGet(account, evt.subscription.roomId), {})
            .then((room) => {
              setRooms((prev) => {
                if (prev.some((r) => r.id === room.id)) return prev
                return [room, ...prev]
              })
            })
            .catch(() => {})
        } else if (evt.action === 'removed') {
          setRooms((prev) => prev.filter((r) => r.id !== evt.subscription.roomId))
        }
      }
    )

    // Subscribe to room metadata updates (name, userCount, lastMsgAt)
    const metaUpdate = subscribe(
      roomMetadataUpdate(account),
      (evt) => {
        setRooms((prev) => {
          const updated = prev.map((r) =>
            r.id === evt.roomId
              ? { ...r, name: evt.name, userCount: evt.userCount, lastMsgAt: evt.lastMsgAt }
              : r
          )
          return updated.sort(
            (a, b) => new Date(b.lastMsgAt) - new Date(a.lastMsgAt)
          )
        })
      }
    )

    subsRef.current = [subUpdate, metaUpdate]

    return () => {
      subsRef.current.forEach((s) => s.unsubscribe())
      subsRef.current = []
    }
  }, [user, request, subscribe])

  return (
    <div className="room-list">
      <div className="room-list-header">Rooms</div>
      {error && <div className="room-list-error">{error}</div>}
      <div className="room-list-items">
        {rooms.map((room) => (
          <div
            key={room.id}
            className={`room-item ${room.id === selectedRoomId ? 'room-item-selected' : ''}`}
            onClick={() => onSelectRoom(room)}
          >
            <span className="room-name">
              {room.type === 'dm' ? '@ ' : '# '}{room.name}
            </span>
            <span className="room-meta">{room.userCount}</span>
          </div>
        ))}
        {rooms.length === 0 && !error && (
          <div className="room-list-empty">No rooms yet</div>
        )}
      </div>
    </div>
  )
}
