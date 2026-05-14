import { roomsList } from '../_transport/subjects'

/**
 * List all rooms the current user is subscribed to (summaries).
 * Called once on RoomEventsProvider mount.
 *
 * @param {{user, request}} nats
 */
export async function listRooms({ user, request }) {
  return request(roomsList(user.account), {})
}
