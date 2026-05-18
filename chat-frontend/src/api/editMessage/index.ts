import { msgEdit } from '../_transport/subjects'
import type { Nats } from '../types'

export interface EditMessagePayload {
  messageId: string
  newMsg: string
}

export interface EditMessageArgs {
  roomId: string
  siteId: string
  payload: EditMessagePayload
}

/** Wire shape of the history-service `MsgEdit` reply. Frontend doesn't
 *  consume the fields today (the optimistic `MESSAGE_EDITED_LOCAL`
 *  dispatch already carries the user's editedAt), but the generic is
 *  passed through `request<T>` per project convention. */
export interface EditMessageResponse {
  messageId?: string
  editedAt?: number
}

/**
 * Edit an existing message's content. Fire-and-forget at the call site,
 * but uses NATS request (not publish) under the hood. The backend
 * handler (history-service `MsgEditPattern`) is registered as a
 * request/reply route via `natsrouter.Register` — calling `publish`
 * here triggers a "reply failed: nats: message does not have a reply"
 * error on the server even though the edit itself persists.
 * Errors are swallowed because the caller already dispatches an
 * optimistic `MESSAGE_EDITED_LOCAL`; the next history fetch reconciles
 * if the server rejected the edit.
 */
export function editMessage(
  { user, request }: Nats,
  { roomId, siteId, payload }: EditMessageArgs,
): void {
  request<EditMessageResponse>(msgEdit(user.account, roomId, siteId), payload).catch(() => {})
}
