import { msgSend } from '../_transport/subjects'
import type { AsyncJobOptions, AsyncPublishResult, Message, Nats } from '../types'

export interface SendMessagePayload {
  id: string
  content: string
  requestId: string
  quotedParentMessageId?: string
  threadParentMessageId?: string
  threadParentMessageCreatedAt?: number
}

export interface SendMessageArgs {
  roomId: string
  siteId: string
  payload: SendMessagePayload
}

/**
 * Submit a new message into a room. The publish itself lands on the MESSAGES
 * stream, but `message-gatekeeper` validates it and replies once on
 * `chat.user.{account}.response.{requestId}` — the canonical Message on
 * success or a user-safe error on a validation failure. This op awaits that
 * reply so callers can surface send errors (e.g. content too large, not a
 * room member) instead of failing silently.
 *
 * The reply is correlated by `payload.requestId`, which the gatekeeper reads
 * from the body, so we forward it as the subscription's requestId.
 */
export function sendMessage(
  { user, publishWithAsyncResult }: Nats,
  { roomId, siteId, payload }: SendMessageArgs,
  opts: AsyncJobOptions = {},
): Promise<AsyncPublishResult<Message>> {
  return publishWithAsyncResult<Message>(
    msgSend(user.account, roomId, siteId),
    payload,
    { ...opts, requestId: payload.requestId },
  )
}
