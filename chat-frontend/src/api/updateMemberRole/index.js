import { memberRoleUpdate } from '../_transport/subjects'

/**
 * Promote / demote a member's role in a channel (owner ↔ member).
 * Two-phase.
 *
 * @param {{user, requestWithAsyncResult}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {string} args.account     Target account.
 * @param {string} args.newRole     ROLE_OWNER | ROLE_MEMBER.
 * @param {Object} [opts]
 */
export async function updateMemberRole({ user, requestWithAsyncResult }, args, opts) {
  const { roomId, siteId, account, newRole } = args
  const subject = memberRoleUpdate(user.account, roomId, siteId)
  const payload = { roomId, account, newRole }
  return opts
    ? requestWithAsyncResult(subject, payload, opts)
    : requestWithAsyncResult(subject, payload)
}
