import { readReceipt } from '../_transport/subjects'

/**
 * Fetch the read-receipt aggregate for a single message: who has read
 * it and the total reader count. Used by MessageActionMenu to render
 * the "Seen by N" tooltip.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {string} args.messageId
 */
export async function fetchReadReceipt({ user, request }, args) {
  const { roomId, siteId, messageId } = args
  return request(readReceipt(user.account, roomId, siteId), { messageId })
}
