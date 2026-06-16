import { useDebug } from '@/context/DebugContext'
import './style.css'

function BugIcon() {
  return (
    <svg
      data-icon="bug"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M8 2l1.5 2.5M16 2l-1.5 2.5" />
      <rect x="8" y="6" width="8" height="12" rx="4" />
      <path d="M3 9h3M18 9h3M3 14h3M18 14h3M3 19l3-2M18 17l3 2M12 18v3" />
    </svg>
  )
}

export default function DebugToggle() {
  const { debug, toggleDebug } = useDebug()
  const label = debug ? 'Disable X-Debug header' : 'Enable X-Debug header'
  return (
    <button
      type="button"
      className={`chat-header-logout debug-toggle${debug ? ' debug-toggle--on' : ''}`}
      onClick={toggleDebug}
      aria-pressed={debug}
      aria-label={label}
      title={label}
    >
      <BugIcon />
    </button>
  )
}
