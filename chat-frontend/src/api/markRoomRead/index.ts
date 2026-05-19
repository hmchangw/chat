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
 *
 * Never rejects (safe to ignore for fire-and-forget callers). Resolves
 * to a commit flag so callers can sequence work *only* when the write
 * actually landed:
 *  - `true`  — the `message.read` reply was received; the server-side
 *              `lastSeenAt` write has committed.
 *  - `false` — transport error; nothing committed. Callers must NOT
 *              treat this as a read (e.g. don't bump `readSeq`); the
 *              next successful read reconciles.
 *
 * Called in two contexts (both in RoomEventsContext / useRoomSubscriptions):
 *  - When the user opens a room (`setActiveRoom`)
 *  - When a new message arrives in the currently-active room
 */
export function markRoomRead(
  { user, request }: Nats,
  { roomId, siteId }: MarkRoomReadArgs,
): Promise<boolean> {
  return request<void>(messageRead(user.account, roomId, siteId), {}).then(
    () => true,
    () => false,
  )
}
