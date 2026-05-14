import { searchRooms as searchRoomsSubject } from '../_transport/subjects'
import type { Nats, RoomType } from '../types'

export interface SearchRoomsArgs {
  searchText: string
  scope: 'all' | 'subscribed'
  size: number
}

export interface SearchRoomHit {
  roomId: string
  roomName: string
  roomType: RoomType
  siteId: string
}

export interface SearchRoomsResponse {
  results: SearchRoomHit[]
  total?: number
}

/**
 * Search rooms the caller is a member of (or all rooms if scope='all').
 *
 * Mirrors search-service's `search.rooms` handler — returns hits sorted
 * by relevance.
 */
export async function searchRooms(
  { user, request }: Nats,
  args: SearchRoomsArgs,
): Promise<SearchRoomsResponse> {
  return request(searchRoomsSubject(user.account), args)
}
