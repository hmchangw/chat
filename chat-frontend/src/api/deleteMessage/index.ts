import { msgDelete } from '../_transport/subjects'
import type { Nats } from '../types'

export interface DeleteMessagePayload {
  messageId: string
}

export interface DeleteMessageArgs {
  roomId: string
  siteId: string
  payload: DeleteMessagePayload
}

/** Wire shape of the history-service `MsgDelete` reply. Frontend doesn't
 *  consume the fields today (the optimistic `MESSAGE_DELETED_LOCAL`
 *  dispatch carries no timestamp), but the generic is passed through
 *  `request<T>` per project convention. */
export interface DeleteMessageResponse {
  messageId?: string
  deletedAt?: number
}

/**
 * Soft-delete a message. Fire-and-forget at the call site, but uses
 * NATS request (not publish) under the hood — same reason as
 * `editMessage`: history-service registers `MsgDeletePattern` as a
 * request/reply route, so a raw publish causes a `Respond` failure on
 * the server. Errors are swallowed because the caller already
 * dispatches an optimistic `MESSAGE_DELETED_LOCAL`.
 */
export function deleteMessage(
  { user, request }: Nats,
  { roomId, siteId, payload }: DeleteMessageArgs,
): void {
  request<DeleteMessageResponse>(msgDelete(user.account, roomId, siteId), payload).catch(() => {})
}
