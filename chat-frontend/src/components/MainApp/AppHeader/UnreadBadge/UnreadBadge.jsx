import { useUnreadTotal } from '@/context/RoomEventsContext'
import './style.css'

export default function UnreadBadge() {
  const { total, hasMention } = useUnreadTotal()
  if (total <= 0) return null

  const label = `${total} unread message${total === 1 ? '' : 's'}`
  const className = `unread-badge${hasMention ? ' unread-badge--mention' : ''}`

  return (
    <span className={className} aria-label={label} title={label}>
      {total > 99 ? '99+' : total}
    </span>
  )
}
