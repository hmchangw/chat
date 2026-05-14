import { useEffect, useState } from 'react'
import { useRoomSummaries } from '../context/RoomEventsContext'
import { useThreadEvents } from '../context/ThreadEventsContext'
import RoomMessageArea from '../components/RoomMessageArea'
import RoomMessageInput from '../components/RoomMessageInput'
import ManageMembersDialog from '../components/ManageMembersDialog'
import LeaveRoomButton from '../components/LeaveRoomButton'
import InRoomSearch from '../components/InRoomSearch'
import RoomMembersBadge from '../components/RoomMembersBadge'
import { roomPrefix, roomDisplayName } from '../lib/roomFormat'

export default function ChatPage({ selectedRoom, onSelectRoom }) {
  const { jumpToMessage } = useRoomSummaries()
  const { openThread, closeThread, activeParent } = useThreadEvents()
  const [showMembers, setShowMembers] = useState(false)
  const [inRoomSearchOpen, setInRoomSearchOpen] = useState(false)
  // Bumped each time ManageMembersDialog closes so RoomMembersBadge refetches
  // its count immediately, without waiting for the next room switch. Mirrors
  // the pattern in upstream MessageArea (pre-refactor).
  const [membersRefreshKey, setMembersRefreshKey] = useState(0)
  const [quotedTarget, setQuotedTarget] = useState(null)

  // When the selected room changes, close room-scoped overlays.
  useEffect(() => {
    setShowMembers(false)
    setInRoomSearchOpen(false)
  }, [selectedRoom?.id])

  // Clear quoted target on room change.
  useEffect(() => {
    setQuotedTarget(null)
  }, [selectedRoom?.id])

  // Close thread when the user switches rooms.
  useEffect(() => {
    if (activeParent && activeParent.roomId !== selectedRoom?.id) {
      closeThread()
    }
  }, [selectedRoom?.id, activeParent, closeThread])

  // Ctrl/Cmd-F opens the in-room side panel; Esc closes it.
  useEffect(() => {
    if (!selectedRoom) return
    const handler = (e) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.key === 'F')) {
        e.preventDefault()
        if (activeParent) closeThread()
        setInRoomSearchOpen(true)
      } else if (e.key === 'Escape') {
        setInRoomSearchOpen(false)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [selectedRoom, activeParent, closeThread])

  const handleInRoomJump = (msgId) => {
    if (selectedRoom && jumpToMessage) {
      jumpToMessage(selectedRoom.id, msgId)?.catch?.(() => {})
    }
  }

  const handleThread = (msg) => {
    if (!selectedRoom || !msg) return
    setInRoomSearchOpen(false)
    openThread({
      roomId: selectedRoom.id,
      siteId: selectedRoom.siteId,
      messageId: msg.id,
      createdAtMs: new Date(msg.createdAt).getTime(),
    })
  }

  const handleReply = (msg) => {
    setQuotedTarget({
      id: msg.id,
      senderName: msg.sender?.engName || msg.sender?.account || msg.userAccount || 'Unknown',
      content: msg.content || msg.msg || '',
    })
  }

  const isChannel = selectedRoom?.type === 'channel'

  return (
    <main className="chat-page">
      {selectedRoom && (
        <header className="chat-room-header">
          <span className="chat-room-name">
            {roomPrefix(selectedRoom.type)}{roomDisplayName(selectedRoom)}
          </span>
          <RoomMembersBadge
            room={selectedRoom}
            onOpen={() => setShowMembers(true)}
            refreshKey={membersRefreshKey}
          />
          <div className="chat-room-header-spacer" />
          {isChannel && <LeaveRoomButton room={selectedRoom} />}
        </header>
      )}
      <div className="chat-page-body">
        <div className="chat-main-content">
          <RoomMessageArea
            room={selectedRoom}
            onThread={handleThread}
            onReply={handleReply}
          />
          <RoomMessageInput room={selectedRoom} quotedTarget={quotedTarget} onClearQuote={() => setQuotedTarget(null)} />
        </div>
        {inRoomSearchOpen && selectedRoom && (
          <InRoomSearch
            roomId={selectedRoom.id}
            onClose={() => setInRoomSearchOpen(false)}
            onJumpToMessage={handleInRoomJump}
          />
        )}
      </div>
      {showMembers && selectedRoom && (
        <ManageMembersDialog
          room={selectedRoom}
          onClose={() => {
            setShowMembers(false)
            setMembersRefreshKey((k) => k + 1)
          }}
        />
      )}
    </main>
  )
}
