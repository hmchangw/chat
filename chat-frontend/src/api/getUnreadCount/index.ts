import type { Nats } from '../types'
// import { userSubscriptionCount } from '../_transport/subjects'

export interface UnreadCountResponse {
  count: number
}

/**
 * Fetch the caller's unread-message total for the header badge.
 *
 * MOCK: the real RPC lives in a local user-service that is not in this
 * repo. To go live: change the destructure to `{ user, request }`,
 * uncomment the subjects import + the `request` line, and delete the
 * hardcoded return. The subject + payload below are the production ones.
 *
 *   const subject = userSubscriptionCount(user.account, user.siteId)
 *   return request<UnreadCountResponse>(subject, { unread: true })
 */
export async function getUnreadCount(
  _nats: Nats,
): Promise<UnreadCountResponse> {
  return { count: 42 }
}
