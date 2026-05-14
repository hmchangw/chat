import { subscriptionUpdate } from '../_transport/subjects'

/**
 * Subscribe to the per-user subscription update stream — fires
 * `{action: 'added'|'removed', subscription}` when room-worker
 * grants/revokes the caller's membership.
 *
 * @returns {{unsubscribe: () => void}}
 */
export function subscribeToSubscriptionUpdates({ user, subscribe }, callback) {
  return subscribe(subscriptionUpdate(user.account), callback)
}
