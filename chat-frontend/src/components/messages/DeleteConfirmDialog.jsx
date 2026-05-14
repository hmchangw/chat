import { useEffect } from 'react'

export default function DeleteConfirmDialog({ onConfirm, onCancel, pending }) {
  useEffect(() => {
    // Capture-phase listener so the dialog "claims" Esc before any ancestor
    // (e.g., ThreadRightBar's onKeyDown that closes the thread). stopPropagation
    // then prevents the bubble from reaching React's synthetic-event tree.
    const handler = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        onCancel?.()
      }
    }
    window.addEventListener('keydown', handler, true)
    return () => window.removeEventListener('keydown', handler, true)
  }, [onCancel])

  return (
    <div className="dialog-backdrop">
      <div className="dialog dialog-delete-confirm" role="dialog" aria-modal="true">
        <p>Delete this message? This cannot be undone.</p>
        <div className="dialog-actions">
          <button type="button" onClick={onCancel} disabled={pending}>Cancel</button>
          <button type="button" onClick={onConfirm} disabled={pending}>
            {pending ? 'Deleting…' : 'Delete'}
          </button>
        </div>
      </div>
    </div>
  )
}
