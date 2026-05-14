import { searchMessages as searchMessagesSubject } from '../_transport/subjects'

/**
 * Full-text search across messages. Optionally scope to a room subset.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.searchText
 * @param {string[]} [args.roomIds]   Limit hits to these rooms.
 * @param {number} args.size
 * @returns {Promise<{results: Array}>}
 */
export async function searchMessages({ user, request }, args) {
  return request(searchMessagesSubject(user.account), args)
}
