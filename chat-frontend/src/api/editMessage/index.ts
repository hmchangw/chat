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

/** Wire shape of MsgEdit reply; passed through `request<T>` per convention. */
export interface EditMessageResponse {
  messageId?: string
  editedAt?: number
}

/** Edit a message's content. Uses NATS request (backend is request/reply); errors swallowed. */
export function editMessage(
  { user, request }: Nats,
  { roomId, siteId, payload }: EditMessageArgs,
): void {
  request<EditMessageResponse>(msgEdit(user.account, roomId, siteId), payload).catch(() => {})
}
