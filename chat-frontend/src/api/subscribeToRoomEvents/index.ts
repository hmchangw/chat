import { roomEvent } from '../_transport/subjects'
import type { Nats, Subscription, SubscriptionCallback } from '../types'

/** Subscribe to a single channel's event stream. */
export function subscribeToRoomEvents(
  { subscribe }: Pick<Nats, 'subscribe'>,
  { roomId }: { roomId: string },
  callback: SubscriptionCallback,
): Subscription {
  return subscribe(roomEvent(roomId), callback)
}
