import { msgSurrounding } from '../_transport/subjects'

/**
 * Fetch the window of messages around a specific message — used when
 * jumping to a search hit or a quoted reference that isn't currently
 * loaded in the room's history buffer.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {string} args.messageId
 */
export async function fetchSurroundingMessages({ user, request }, args) {
  const { roomId, siteId, messageId } = args
  return request(msgSurrounding(user.account, roomId, siteId), { messageId })
}
