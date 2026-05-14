import { msgSend } from '../_transport/subjects'

/**
 * Submit a new message into a room. Fire-and-forget — the server
 * acknowledges by broadcasting the canonical event back to the room.
 *
 * @param {{user, publish}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {Object} args.payload    `{id, content, requestId, quotedParentMessageId?}`.
 */
export function sendMessage({ user, publish }, { roomId, siteId, payload }) {
  publish(msgSend(user.account, roomId, siteId), payload)
}
