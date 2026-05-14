import { msgThread } from '../_transport/subjects'
import type { Nats, Message } from '../types'

export interface FetchThreadMessagesArgs {
  roomId: string
  siteId: string
  /** Parent message id — the root of the reply chain. */
  threadMessageId: string
  limit?: number
}

export interface FetchThreadMessagesResponse {
  messages: Message[]
}

/** Load the reply chain for a thread (parent + replies). */
export async function fetchThreadMessages(
  { user, request }: Nats,
  args: FetchThreadMessagesArgs,
): Promise<FetchThreadMessagesResponse> {
  const { roomId, siteId, threadMessageId, limit = 50 } = args
  return request(msgThread(user.account, roomId, siteId), { threadMessageId, limit })
}
