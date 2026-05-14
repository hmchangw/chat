export const MAX_CACHED = 200

export function appendBounded(messages, msg) {
  if (messages.some((m) => m.id === msg.id)) return messages
  const next = [...messages, msg]
  if (next.length > MAX_CACHED) {
    return next.slice(next.length - MAX_CACHED)
  }
  return next
}

// mergeById merges `incoming` ahead of `existing`, dedupes by `id`, and
// preserves any `_local` / `_status` markers that live only on the existing
// rows (the server doesn't know about them). Incoming is reversed so the
// newest item (last in the incoming array) appears first in the result.
// When the merged length exceeds MAX_CACHED, existing rows are trimmed from
// the front (oldest) while all incoming rows are preserved.
export function mergeById(existing, incoming) {
  const incomingMap = new Map(incoming.map((m) => [m.id, m]))
  const existingIds = new Set(existing.map((e) => e.id))

  // Reverse incoming so the newest (last) item appears first; filter to only
  // items that are not already in existing (those are handled via updatedExisting).
  const newIncoming = [...incoming].reverse().filter((m) => !existingIds.has(m.id))

  // Update existing rows with incoming values, preserving _local / _status markers.
  const updatedExisting = existing.map((e) => {
    const inc = incomingMap.get(e.id)
    if (!inc) return e
    const out = { ...inc }
    if (e._local) out._local = e._local
    if (e._status) out._status = e._status
    return out
  })

  if (newIncoming.length + updatedExisting.length > MAX_CACHED) {
    const keep = MAX_CACHED - newIncoming.length
    return [...newIncoming, ...updatedExisting.slice(updatedExisting.length - keep)]
  }
  return [...newIncoming, ...updatedExisting]
}
