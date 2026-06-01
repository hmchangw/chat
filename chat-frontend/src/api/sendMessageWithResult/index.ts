import { msgSend } from '../_transport/subjects'
import type { Nats } from '../types'
import type { SendMessageArgs } from '../sendMessage'

/** Options for awaiting the gatekeeper reply. */
export interface SendMessageResultOpts {
  /** Override the default response timeout (ms). */
  timeout?: number
}

/**
 * Submit a new message AND await the gatekeeper's reply.
 *
 * Unlike the fire-and-forget {@link sendMessage}, this resolves with the
 * persisted `Message` the message-gatekeeper publishes on
 * chat.user.{account}.response.{requestID} (or rejects on the error envelope /
 * a response timeout). The reply is correlated by the `requestId` in
 * `payload` via the session-long response-wildcard subscription.
 *
 * The waiter is registered BEFORE publishing so a fast gatekeeper can't beat
 * the client to the reply.
 */
export function sendMessageWithResult(
  { user, publish, waitForResponse }: Nats,
  { roomId, siteId, payload }: SendMessageArgs,
  opts: SendMessageResultOpts = {},
): Promise<unknown> {
  const waiting = waitForResponse(payload.requestId, opts)
  publish(msgSend(user.account, roomId, siteId), payload)
  return waiting
}
