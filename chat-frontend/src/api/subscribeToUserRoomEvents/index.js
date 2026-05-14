import { userRoomEvent } from '../_transport/subjects'

/**
 * Subscribe to per-user room events (DM message broadcasts). The subject
 * is the catch-all destination room-worker writes DM-bound events on,
 * since DMs don't have a stable per-room channel.
 *
 * @returns {{unsubscribe: () => void}}
 */
export function subscribeToUserRoomEvents({ user, subscribe }, callback) {
  return subscribe(userRoomEvent(user.account), callback)
}
