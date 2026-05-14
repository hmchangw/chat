import { msgHistory } from '../_transport/subjects'
import type { Nats, Message } from '../types'

export interface FetchMessageHistoryArgs {
  roomId: string
  siteId: string
  limit?: number
  /** Cursor (messageId). Paginate older. */
  before?: string
}

export interface FetchMessageHistoryResponse {
  messages: Message[]
}

/** Fetch a page of message history for a room. Newest-first. */
export async function fetchMessageHistory(
  { user, request }: Nats,
  args: FetchMessageHistoryArgs,
): Promise<FetchMessageHistoryResponse> {
  const { roomId, siteId, limit = 50, before } = args
  const payload: Record<string, unknown> = { limit }
  if (before) payload.before = before
  return request(msgHistory(user.account, roomId, siteId), payload)
}
