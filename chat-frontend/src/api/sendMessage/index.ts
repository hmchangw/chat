import { msgSend } from '../_transport/subjects'
import type { Nats, QuotedParentMessage } from '../types'

export interface SendMessagePayload {
  id: string
  content: string
  requestId: string
  quotedParentMessageId?: string
  /** Client-supplied fallback snapshot of the quoted parent. The server resolves
   *  the authoritative snapshot from history-service and ignores this on the happy
   *  path; it is used only to degrade (not drop) the message if that fetch fails
   *  transiently. Sent alongside quotedParentMessageId. */
  quotedParentMessage?: QuotedParentMessage
  threadParentMessageId?: string
  threadParentMessageCreatedAt?: number
}

export interface SendMessageArgs {
  roomId: string
  siteId: string
  payload: SendMessagePayload
}

/** Submit a new message into a room. Fire-and-forget. */
export function sendMessage(
  { user, publish }: Nats,
  { roomId, siteId, payload }: SendMessageArgs,
): void {
  publish(msgSend(user.account, roomId, siteId), payload)
}
