import MessageActionMenu from '../MessageActionMenu'
import MessageActions from './MessageActions'
import QuotedBlock from './QuotedBlock'

function formatTime(dateStr) {
  const d = new Date(dateStr)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function senderName(msg) {
  if (msg.sender) {
    return msg.sender.engName || msg.sender.account || msg.sender.userId || 'Unknown'
  }
  return msg.userAccount || msg.userId || 'Unknown'
}

function messageContent(msg) {
  return msg.content || msg.msg || ''
}

export default function MessageRow({
  message,
  room,
  context,
  onThread,
  onReply,
  onJumpToMessage,
}) {
  return (
    <div
      className="message-row"
      data-message-id={message.id}
      tabIndex={0}
    >
      {message.quotedParentMessage && (
        <QuotedBlock
          variant="bubble"
          snapshot={message.quotedParentMessage}
          onClick={onJumpToMessage}
        />
      )}
      <div className="message-header">
        <span className="message-sender">{senderName(message)}</span>
        <span className="message-time">{formatTime(message.createdAt)}</span>
      </div>
      <div className="message-content">{messageContent(message)}</div>
      <MessageActions
        message={message}
        context={context}
        onThread={onThread}
        onReply={onReply}
      />
      <MessageActionMenu message={message} room={room} />
    </div>
  )
}
