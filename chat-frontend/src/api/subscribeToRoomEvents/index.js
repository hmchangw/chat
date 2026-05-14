import { roomEvent } from '../_transport/subjects'

/**
 * Subscribe to a single channel's event stream — broadcast-worker fans
 * `new_message` / `message_edited` / `message_deleted` events out here
 * for every member of the channel. Open this per channel the user is a
 * member of on connect, close on subscription.removed.
 *
 * @returns {{unsubscribe: () => void}}
 */
export function subscribeToRoomEvents({ subscribe }, { roomId }, callback) {
  return subscribe(roomEvent(roomId), callback)
}
