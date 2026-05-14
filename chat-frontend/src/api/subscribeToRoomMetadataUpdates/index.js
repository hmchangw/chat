import { roomMetadataUpdate } from '../_transport/subjects'

/**
 * Subscribe to per-user room-metadata updates (room renamed, member
 * count changed, last-message timestamp moved). Used by the sidebar
 * to keep room summaries fresh without a full refetch.
 *
 * @returns {{unsubscribe: () => void}}
 */
export function subscribeToRoomMetadataUpdates({ user, subscribe }, callback) {
  return subscribe(roomMetadataUpdate(user.account), callback)
}
