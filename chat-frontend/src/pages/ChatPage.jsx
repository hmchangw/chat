import { useEffect, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { useRoomSummaries } from '../context/RoomEventsContext'
import RoomList from '../components/RoomList'
import MessageArea from '../components/MessageArea'
import MessageInput from '../components/MessageInput'
import CreateRoomDialog from '../components/CreateRoomDialog'
import GlobalSearchBar from '../components/GlobalSearchBar'
import SearchResultsView from '../components/SearchResultsView'

export default function ChatPage() {
  const { user, disconnect } = useNats()
  const { summaries, setActiveRoom } = useRoomSummaries()
  const [selectedRoom, setSelectedRoom] = useState(null)
  const [showCreateRoom, setShowCreateRoom] = useState(false)
  const [searchQuery, setSearchQuery] = useState(null)

  // Clear selection if the selected room disappears from summaries
  useEffect(() => {
    if (selectedRoom && !summaries.some((r) => r.id === selectedRoom.id)) {
      setSelectedRoom(null)
      setActiveRoom(null)
    }
  }, [summaries, selectedRoom, setActiveRoom])

  const handleSelectRoom = (room) => {
    setSelectedRoom(room)
    setActiveRoom(room?.id ?? null)
  }

  // Prefer the live summary over search hit fields so the message area gets
  // the full room (userCount, up-to-date name, etc.).
  const handleSelectFromSearch = (hit) => {
    const summary = summaries.find((r) => r.id === hit.id)
    handleSelectRoom(summary ?? hit)
  }

  return (
    <div className="chat-layout">
      <div className="chat-header">
        <span className="chat-header-title">Chat</span>
        <div className="chat-header-search-wrap">
          <GlobalSearchBar
            onSelectRoom={handleSelectFromSearch}
            onSubmit={(q) => setSearchQuery(q)}
          />
        </div>
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
            onSelectRoom={handleSelectRoom}
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
          onCreated={(room) => handleSelectRoom(room)}
        />
      )}
      {searchQuery && (
        <SearchResultsView
          text={searchQuery}
          onClose={() => setSearchQuery(null)}
          onSelectRoom={handleSelectFromSearch}
        />
      )}
    </div>
  )
}
