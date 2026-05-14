import { searchRooms as searchRoomsSubject } from '../_transport/subjects'

/**
 * Search rooms the caller is a member of (or all rooms if scope='all').
 *
 * Mirrors search-service's `search.rooms` handler — returns hits sorted
 * by relevance.
 *
 * @param {{user, request}} nats        From `useNats()`.
 * @param {Object} args
 * @param {string} args.searchText      Query string.
 * @param {'all'|'subscribed'} args.scope
 * @param {number} args.size            Max hits to return.
 * @returns {Promise<{results: Array}>}
 */
export async function searchRooms({ user, request }, args) {
  return request(searchRoomsSubject(user.account), args)
}
