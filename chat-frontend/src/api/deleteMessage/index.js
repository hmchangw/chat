import { msgDelete } from '../_transport/subjects'

/**
 * Soft-delete a message. Fire-and-forget — the server broadcasts the
 * deletion canonical event back to the room (MessageRow then short-
 * circuits on `deleted`).
 *
 * @param {{user, publish}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {Object} args.payload    `{messageId, requestId}`.
 */
export function deleteMessage({ user, publish }, { roomId, siteId, payload }) {
  publish(msgDelete(user.account, roomId, siteId), payload)
}
