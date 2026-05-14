import { memberRemove } from '../_transport/subjects'

/**
 * Leave a channel as the current user. Same subject as removeMember
 * but sync-only — the caller doesn't wait on the async worker result
 * because the UI just unsubscribes and moves on either way.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 */
export async function leaveRoom({ user, request }, { roomId, siteId }) {
  return request(memberRemove(user.account, roomId, siteId), {
    roomId,
    account: user.account,
  })
}
