import { msgThread } from '../_transport/subjects'

/**
 * Load the reply chain for a thread (parent + replies).
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {string} args.threadMessageId    Parent message id.
 * @param {number} [args.limit=50]
 */
export async function fetchThreadMessages({ user, request }, args) {
  const { roomId, siteId, threadMessageId, limit = 50 } = args
  return request(msgThread(user.account, roomId, siteId), { threadMessageId, limit })
}
