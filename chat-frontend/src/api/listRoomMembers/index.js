import { memberList } from '../_transport/subjects'

/**
 * List the members of a room. With `enrich: true` the server fattens
 * each entry with engName/chineseName/etc; without it, callers get the
 * compact roster used for member counts.
 *
 * @param {{user, request}} nats
 * @param {Object} args
 * @param {string} args.roomId
 * @param {string} args.siteId
 * @param {boolean} [args.enrich=false]
 * @returns {Promise<{members: Array}>}
 */
export async function listRoomMembers({ user, request }, { roomId, siteId, enrich = false }) {
  const payload = enrich ? { enrich: true } : {}
  return request(memberList(user.account, roomId, siteId), payload)
}
