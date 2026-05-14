import { memberRoleUpdate } from '../_transport/subjects'
import type { Nats, AsyncJobOptions, AsyncJobResult } from '../types'

export type MemberRole = 'owner' | 'member'

export interface UpdateMemberRoleArgs {
  roomId: string
  siteId: string
  account: string
  newRole: MemberRole
}

/** Promote / demote a member's role in a channel. Two-phase. */
export async function updateMemberRole(
  { user, requestWithAsyncResult }: Nats,
  args: UpdateMemberRoleArgs,
  opts?: AsyncJobOptions,
): Promise<AsyncJobResult> {
  const { roomId, siteId, account, newRole } = args
  const subject = memberRoleUpdate(user.account, roomId, siteId)
  const payload = { roomId, account, newRole }
  return opts
    ? requestWithAsyncResult(subject, payload, opts)
    : requestWithAsyncResult(subject, payload)
}
