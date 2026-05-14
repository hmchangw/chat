import { useCallback, useEffect, useState } from 'react'
import { useRoomSummaries } from '../../context/RoomEventsContext'
import { useThreadEvents } from '../../context/ThreadEventsContext'
import AppHeader from './AppHeader/AppHeader'
import Sidebar from './Sidebar/Sidebar'
import ChatPage from './ChatPage/ChatPage'
import SearchResultsPane from './SearchResultsPane/SearchResultsPane'
import ThreadRightBar from './ThreadRightBar/ThreadRightBar'
import './style.css'

export default function MainApp() {
  const { summaries, setActiveRoom, jumpToMessage } = useRoomSummaries()
  const { activeParent } = useThreadEvents()
  const [selectedRoom, setSelectedRoom] = useState(null)
  const [searchQuery, setSearchQuery] = useState(null)

  // Clear selection if the room disappears from summaries (left / kicked).
  useEffect(() => {
    if (selectedRoom && !summaries.some((r) => r.id === selectedRoom.id)) {
      setSelectedRoom(null)
      setActiveRoom(null)
    }
  }, [summaries, selectedRoom, setActiveRoom])

  const handleSelectRoom = useCallback(
    (room) => {
      setSelectedRoom(room)
      setActiveRoom(room?.id ?? null)
      setSearchQuery(null)
    },
    [setActiveRoom]
  )

  const handleEnterSearch = useCallback((q) => setSearchQuery(q), [])

  const handleJumpToMessage = useCallback(
    (roomId, messageId) => {
      const room = summaries.find((r) => r.id === roomId)
      if (room) {
        setSelectedRoom(room)
        setActiveRoom(room.id)
      }
      setSearchQuery(null)
      if (jumpToMessage) jumpToMessage(roomId, messageId)?.catch?.(() => {})
    },
    [summaries, setActiveRoom, jumpToMessage]
  )

  return (
    <div className="app-shell">
      <AppHeader onSelectRoom={handleSelectRoom} onEnterSearch={handleEnterSearch} />
      <div className="app-row">
        <Sidebar selectedRoomId={selectedRoom?.id ?? null} onSelectRoom={handleSelectRoom} />
        {searchQuery ? (
          <SearchResultsPane
            query={searchQuery}
            onClose={() => setSearchQuery(null)}
            onSelectRoom={handleSelectRoom}
            onJumpToMessage={handleJumpToMessage}
          />
        ) : (
          <ChatPage selectedRoom={selectedRoom} onSelectRoom={handleSelectRoom} />
        )}
        {activeParent && <ThreadRightBar />}
      </div>
    </div>
  )
}
