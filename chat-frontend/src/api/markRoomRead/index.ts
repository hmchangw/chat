import { messageRead } from '../_transport/subjects'
import type { Nats } from '../types'

export interface MarkRoomReadArgs {
  roomId: string
  /** Site the room lives on. Caller usually resolves this from the room
   *  summary; falls back to `user.siteId` at the call site if absent. */
  siteId: string
}

/**
 * Advance the current user's `lastSeenAt` on the server for one room.
 * Fire-and-forget — UI doesn't need to wait, and errors are swallowed
 * because a failed mark-as-read just means the next read-receipt RPC
 * will reflect slightly stale state.
 *
 * Called in two contexts (both in RoomEventsContext / useRoomSubscriptions):
 *  - When the user opens a room (`setActiveRoom`)
 *  - When a new message arrives in the currently-active room from a
 *    different sender
 */
export function markRoomRead(
  { user, request }: Nats,
  { roomId, siteId }: MarkRoomReadArgs,
): Promise<void> {
  // Resolves once the server has replied (the message.read RPC is
  // request/reply — by then the `lastSeenAt` write has committed). Errors
  // are swallowed into a resolve: callers chain a refetch off this and a
  // failed mark-read just means the next read will reconcile. Callers may
  // ignore the promise (fire-and-forget) or await it to sequence work
  // after the read lands.
  return request(messageRead(user.account, roomId, siteId), {}).then(
    () => {},
    () => {},
  )
}
