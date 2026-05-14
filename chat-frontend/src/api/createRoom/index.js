import { roomCreate } from '../_transport/subjects'

/**
 * Create a room. The server infers the room type from the payload shape:
 * - `name` set → channel
 * - `name` empty + single user in `users` → DM (or BotDM, server decides)
 *
 * Two-phase. The caller typically passes `treatAsSuccess` so that the
 * DM-exists dedup reply isn't treated as an error.
 *
 * @param {{user, requestWithAsyncResult}} nats
 * @param {Object} args
 * @param {string}   [args.name=""]
 * @param {string[]} [args.users=[]]
 * @param {string[]} [args.orgs=[]]
 * @param {Array}    [args.channels=[]]
 * @param {Object} [opts]                Forwarded (`treatAsSuccess`, ...).
 */
export async function createRoom({ user, requestWithAsyncResult }, args, opts) {
  const { name = '', users = [], orgs = [], channels = [] } = args
  const subject = roomCreate(user.account, user.siteId)
  const payload = { name, users, orgs, channels }
  return opts
    ? requestWithAsyncResult(subject, payload, opts)
    : requestWithAsyncResult(subject, payload)
}
