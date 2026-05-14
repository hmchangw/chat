import { msgHistory } from '../_transport/subjects'

/**
 * Fetch a page of message history for a room. The server-side default
 * sort is newest-first; the caller pages backwards with `before`.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {number} [args.limit=50]
 * @param {string} [args.before]    Cursor (messageId) — paginate older.
 */
export async function fetchMessageHistory({ user, request }, args) {
  const { roomId, siteId, limit = 50, before } = args
  const payload = before ? { limit, before } : { limit }
  return request(msgHistory(user.account, roomId, siteId), payload)
}
