import { msgSurrounding } from '../_transport/subjects'
import type { Nats, Message } from '../types'

export interface FetchSurroundingMessagesArgs {
  roomId: string
  siteId: string
  messageId: string
}

export interface FetchSurroundingMessagesResponse {
  messages: Message[]
}

/** Fetch the window of messages around a specific message. */
export async function fetchSurroundingMessages(
  { user, request }: Nats,
  args: FetchSurroundingMessagesArgs,
): Promise<FetchSurroundingMessagesResponse> {
  const { roomId, siteId, messageId } = args
  return request(msgSurrounding(user.account, roomId, siteId), { messageId })
}
