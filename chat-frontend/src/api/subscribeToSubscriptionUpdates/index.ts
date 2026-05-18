import { subscriptionUpdate } from '../_transport/subjects'
import type { Nats, NatsSubscription, SubscriptionUpdateEvent } from '../types'

/** Callback fired for every `subscription.update` event addressed to
 *  the current user. Payload is the parsed
 *  `pkg/model.SubscriptionUpdateEvent` — narrow on `.action` to handle
 *  added / removed / role_updated (and any future actions the backend
 *  publishes; the reducer's SUBSCRIPTION_UPSERTED handles them all). */
export type SubscriptionUpdateCallback = (event: SubscriptionUpdateEvent) => void

/** Subscribe to per-user subscription updates (added / removed
 *  membership events from room-worker, role_updated, …). */
export function subscribeToSubscriptionUpdates(
  { user, subscribe }: Nats,
  callback: SubscriptionUpdateCallback,
): NatsSubscription {
  // The transport's `subscribe` is typed with the generic
  // `SubscriptionCallback = (event: unknown) => void`. We're locally
  // narrower for this op — cast once at the boundary so consumers
  // (useRoomSubscriptions) get the typed event without each call site
  // re-narrowing.
  return subscribe(subscriptionUpdate(user.account), callback as (event: unknown) => void)
}
