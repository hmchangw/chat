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

/** Wire shape of MsgDelete reply; passed through `request<T>` per convention. */
export interface DeleteMessageResponse {
  messageId?: string
  deletedAt?: number
}

/** Soft-delete a message. Same publish→request reasoning as editMessage. */
export function deleteMessage(
  { user, request }: Nats,
  { roomId, siteId, payload }: DeleteMessageArgs,
): void {
  request<DeleteMessageResponse>(msgDelete(user.account, roomId, siteId), payload).catch(() => {})
}
