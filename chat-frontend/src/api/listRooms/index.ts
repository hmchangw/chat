import { roomsList } from '../_transport/subjects'
import type { Nats, Room } from '../types'

export interface ListRoomsResponse {
  rooms: Room[]
}

/** List all rooms the current user is subscribed to. */
export async function listRooms({ user, request }: Nats): Promise<ListRoomsResponse> {
  const subject = roomsList(user.account)
  const resp = await request<ListRoomsResponse>(subject, {})
  // TEMP DEBUG: pair with the [sidebar-bootstrap] logs in
  // fetchSidebarBuckets so we can diff what listRooms returns vs
  // what the subscription RPCs return. Compact summary so the
  // console stays readable for accounts with many rooms. Remove
  // once verified.
  console.log('[sidebar-bootstrap]', subject, {
    count: resp.rooms?.length ?? 0,
    roomIds: resp.rooms?.map((r) => r.id) ?? [],
  })
  return resp
}
