import { roomsKeysBootstrap } from '../_transport/subjects'
import type { Nats, RoomKeysResponse } from '../types'

/** Fetch the full snapshot of (roomId, version, privateKey) for every
 *  room the caller is subscribed to. Call once on (re)connect. */
export function fetchRoomKeysBootstrap(
  { request, user }: Pick<Nats, 'request' | 'user'>,
): Promise<RoomKeysResponse> {
  return request<RoomKeysResponse>(roomsKeysBootstrap(user.account), {})
}
