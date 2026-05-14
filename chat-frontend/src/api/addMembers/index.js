import { memberAdd } from '../_transport/subjects'

/**
 * Add members to a channel. Two-phase: returns once the sync ACK arrives
 * AND the async worker result lands on the response subject. Mirrors
 * pkg/model.AddMembersRequest — passes through users / orgs / channels
 * (plus the history-sharing flag) unmodified.
 *
 * @param {{user, requestWithAsyncResult}} nats
 * @param {Object} args
 * @param {string}   args.roomId
 * @param {string}   args.siteId
 * @param {string[]} [args.users=[]]
 * @param {string[]} [args.orgs=[]]
 * @param {Array}    [args.channels=[]]
 * @param {{mode: 'all'|'none'}} [args.history]
 * @param {Object}   [opts]                  Forwarded to requestWithAsyncResult.
 */
export async function addMembers({ user, requestWithAsyncResult }, args, opts) {
  const { roomId, siteId, users = [], orgs = [], channels = [], history } = args
  const payload = { roomId, users, orgs, channels }
  if (history) payload.history = history
  const subject = memberAdd(user.account, roomId, siteId)
  return opts
    ? requestWithAsyncResult(subject, payload, opts)
    : requestWithAsyncResult(subject, payload)
}
