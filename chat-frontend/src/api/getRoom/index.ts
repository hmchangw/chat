import { roomsGet } from '../_transport/subjects'
import type { Nats, Room } from '../types'

export interface GetRoomArgs {
  roomId: string
}

/** Fetch a single room's metadata. */
export async function getRoom(
  { user, request }: Nats,
  { roomId }: GetRoomArgs,
): Promise<Room | null> {
  return request(roomsGet(user.account, roomId), {})
}
