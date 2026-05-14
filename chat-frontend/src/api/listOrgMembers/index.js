import { orgMembers } from '../_transport/subjects'

/**
 * Expand an org (sect) entry into the list of individual members it
 * grants access to. Used to render the "expand org" affordance in
 * MemberRoster.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.orgId
 * @returns {Promise<{members: Array}>}
 */
export async function listOrgMembers({ user, request }, { orgId }) {
  return request(orgMembers(user.account, orgId), {})
}
