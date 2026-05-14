// The chat API has two message shapes that flow through the client:
//
//   1. Broadcast (`pkg/model.Message`) — published by broadcast-worker.
//      Field names: id, content, userAccount, createdAt.
//
//   2. History (`pkg/model/cassandra.Message`) — read by history-service.
//      Field names: messageId, msg, sender.account, createdAt.
//
// Optimistic dispatches mirror shape #1 (id + content) because the
// broadcast-worker round-trip is the canonical write path. Without a
// normaliser, historical messages arrive with `messageId`/`msg` and
// every `msg.id` access in the UI is `undefined` — Thread / Reply /
// edit / delete all fail silently on anything loaded after a refresh.
//
// Normalise at the load boundary: map historical messages into the
// broadcast shape before they hit the reducer. Reducer + UI stay shape
// #1 only.
export function normalizeHistoricalMessage(m) {
  if (!m) return m
  // Already in broadcast shape — no-op.
  if (m.id && m.content !== undefined) return m
  const id = m.id ?? m.messageId
  const content = m.content ?? m.msg ?? ''
  // Preserve every other field — quotedParentMessage, sender, createdAt,
  // tcount, deleted, editedAt, etc. all use identical names in both shapes.
  return { ...m, id, content }
}

export function normalizeHistoricalMessages(messages) {
  if (!Array.isArray(messages)) return []
  return messages.map(normalizeHistoricalMessage)
}
