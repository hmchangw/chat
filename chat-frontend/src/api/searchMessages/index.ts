import { searchMessages as searchMessagesSubject } from '../_transport/subjects'
import type { Nats } from '../types'

export interface SearchMessagesArgs {
  searchText: string
  /** Limit hits to these rooms; empty/omitted searches everywhere. */
  roomIds?: string[]
  size: number
}

export interface SearchMessageHit {
  messageId: string
  roomId: string
  roomName?: string
  content: string
  userAccount: string
  createdAt: string
}

export interface SearchMessagesResponse {
  results: SearchMessageHit[]
  total?: number
}

/** Full-text search across messages. Optionally scope to a room subset. */
export async function searchMessages(
  { user, request }: Nats,
  args: SearchMessagesArgs,
): Promise<SearchMessagesResponse> {
  return request(searchMessagesSubject(user.account), args)
}
