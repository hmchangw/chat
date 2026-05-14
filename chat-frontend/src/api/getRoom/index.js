import { roomsGet } from '../_transport/subjects'

/**
 * Fetch a single room's metadata. Used by RoomEventsContext when a
 * subscription.update event arrives for a room we don't have cached.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.roomId
 */
export async function getRoom({ user, request }, { roomId }) {
  return request(roomsGet(user.account, roomId), {})
}
