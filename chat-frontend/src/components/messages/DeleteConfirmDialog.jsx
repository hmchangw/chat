import { useEffect } from 'react'

export default function DeleteConfirmDialog({ onConfirm, onCancel, pending }) {
  useEffect(() => {
    const handler = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onCancel?.()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
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
