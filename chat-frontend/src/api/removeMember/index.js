import { memberRemove } from '../_transport/subjects'

/**
 * Remove a member from a channel. Two-phase. The server accepts either
 * an `account` (remove a single individual) or an `orgId` (remove an
 * entire org membership) on the same subject — exactly one must be set.
 * Use `leaveRoom` for the "leave myself" path; that's the sync variant.
 *
 * @param {{user, requestWithAsyncResult}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {string} [args.account]    Individual target.
 * @param {string} [args.orgId]      Org target (mutually exclusive with account).
 * @param {Object} [opts]
 */
export async function removeMember({ user, requestWithAsyncResult }, args, opts) {
  const { roomId, siteId, account, orgId } = args
  const payload = { roomId }
  if (account) payload.account = account
  if (orgId) payload.orgId = orgId
  const subject = memberRemove(user.account, roomId, siteId)
  return opts
    ? requestWithAsyncResult(subject, payload, opts)
    : requestWithAsyncResult(subject, payload)
}
