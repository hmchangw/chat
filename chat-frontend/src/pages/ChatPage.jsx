import { useState } from 'react'
import { useNats } from '../context/NatsContext'
import RoomList from '../components/RoomList'
import MessageArea from '../components/MessageArea'
import MessageInput from '../components/MessageInput'
import CreateRoomDialog from '../components/CreateRoomDialog'

export default function ChatPage() {
  const { user, disconnect } = useNats()
  const [selectedRoom, setSelectedRoom] = useState(null)
  const [showCreateRoom, setShowCreateRoom] = useState(false)

  return (
    <div className="chat-layout">
      <div className="chat-header">
        <span className="chat-header-title">Chat</span>
        <span className="chat-header-user">
          {user?.account} &middot; {user?.siteId}
        </span>
        <button className="chat-header-logout" onClick={disconnect}>
          Logout
        </button>
      </div>
      <div className="chat-body">
        <div className="chat-sidebar">
          <RoomList
            selectedRoomId={selectedRoom?.id}
            onSelectRoom={setSelectedRoom}
          />
          <button
            className="create-room-btn"
            onClick={() => setShowCreateRoom(true)}
          >
            + Create Room
          </button>
        </div>
        <div className="chat-main">
          <MessageArea room={selectedRoom} />
          <MessageInput room={selectedRoom} />
        </div>
      </div>
      {showCreateRoom && (
        <CreateRoomDialog
          onClose={() => setShowCreateRoom(false)}
          onCreated={(room) => setSelectedRoom(room)}
        />
      )}
    </div>
  )
}
