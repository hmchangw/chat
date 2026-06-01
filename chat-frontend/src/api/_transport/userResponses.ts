// startUserResponseSubscription opens the baseline subscription the docs
// (docs/client-api.md §1, §4) require: chat.user.{account}.response.>. Every
// async reply on the user's response namespace lands here — including the
// msg.send reply the message-gatekeeper publishes on
// chat.user.{account}.response.{requestID}. Each message is parsed and routed
// to its waiter via the correlator (keyed by the requestId in the subject).
//
// Before this subscription existed, msg.send replies were dropped: the
// frontend only held narrow chat.user.{account}.event.* subscriptions, none of
// which match the response subject, so the gatekeeper's reply reached no
// subscriber.

import { StringCodec } from 'nats.ws'
import type { NatsConnection, Subscription as NatsSubscription } from 'nats.ws'
import { userResponseWildcard } from './subjects'
import { parseResponseRequestId, type ResponseCorrelator } from './responseCorrelator'

const sc = StringCodec()

/**
 * Subscribe to chat.user.{account}.response.> and route every reply to
 * `correlator.deliver(requestId, payload)`. Returns the NATS subscription so
 * the caller can `unsubscribe()` on disconnect.
 */
export function startUserResponseSubscription(
  nc: NatsConnection,
  account: string,
  correlator: ResponseCorrelator,
): NatsSubscription {
  const sub = nc.subscribe(userResponseWildcard(account))
  ;(async () => {
    for await (const msg of sub) {
      const requestId = parseResponseRequestId(msg.subject)
      if (!requestId) continue
      let payload: unknown
      try {
        payload = JSON.parse(sc.decode(msg.data))
      } catch {
        // Replies are always JSON; skip anything malformed rather than
        // letting a parse error tear down the whole subscription loop.
        continue
      }
      correlator.deliver(requestId, payload)
    }
  })()
  return sub
}
