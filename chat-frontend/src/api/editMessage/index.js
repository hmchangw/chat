import { msgEdit } from '../_transport/subjects'

/**
 * Edit an existing message's content. Fire-and-forget — the server
 * broadcasts the edited canonical event back to the room.
 *
 * @param {{user, publish}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {Object} args.payload    `{messageId, content, requestId}`.
 */
export function editMessage({ user, publish }, { roomId, siteId, payload }) {
  publish(msgEdit(user.account, roomId, siteId), payload)
}
