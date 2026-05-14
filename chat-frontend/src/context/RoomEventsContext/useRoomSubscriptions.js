import { useEffect, useRef } from 'react'
import {
  getRoom,
  listRooms,
  subscribeToRoomEvents,
  subscribeToRoomMetadataUpdates,
  subscribeToSubscriptionUpdates,
  subscribeToUserRoomEvents,
} from '@/api'

/**
 * Owns every backend subscription + the initial-room-list fetch that
 * keeps RoomEventsContext.state in sync with the server. Splitting this
 * out of the provider's body means the provider itself just wires
 * reducer dispatchers and exposes callbacks — this hook is the one
 * place where "what arrives from NATS turns into what dispatch?" lives.
 *
 * Behaviour:
 *   - On mount (or when `user` flips), open four subscriptions:
 *       * userRoomEvents          → DM `new_message`
 *       * roomEvents(roomId)      → per-channel `new_message` (opened
 *                                   lazily for each channel the user
 *                                   joins)
 *       * subscriptionUpdates     → `added` / `removed` membership
 *                                   changes, triggers getRoom() +
 *                                   ROOM_ADDED, or ROOM_REMOVED +
 *                                   close-channel-sub
 *       * roomMetadataUpdates     → name / userCount / lastMsgAt drips
 *   - Also fires listRooms() once and seeds channel subscriptions for
 *     every channel in the response.
 *   - On unmount (or when `user` flips again), tears every subscription
 *     down, dispatches RESET, and bumps a cancellation flag so any
 *     in-flight getRoom() promise that resolves late is swallowed.
 *
 * Dispatch guard: `safeDispatch` and the cancellation flag together
 * stop a late-resolving promise from writing to a torn-down reducer.
 *
 * Caller responsibilities:
 *   - Pass the live `nats` value from useNats() — the hook destructures
 *     `user` to gate the effect.
 *   - Pass the reducer's `dispatch` directly.
 *   - Pass `generationRef`: a ref owned by the provider. The hook bumps
 *     it on every (re)connect so the provider's outside-the-hook async
 *     callbacks (loadHistory, jumpToMessage) can detect "I started in
 *     generation N, but generation is N+1 by the time I resolved — drop
 *     this dispatch." Without the shared ref a user reconnect mid-fetch
 *     would let a stale fetch write into the fresh state.
 */
export function useRoomSubscriptions(nats, dispatch, generationRef) {
  const { user } = nats
  // Channel subscriptions are tracked in a ref so subscriptionUpdate's
  // "added" branch (which opens them) and the cleanup (which closes
  // them) can both reach the same map without re-creating the effect.
  const channelSubs = useRef(new Map())
  const cancelledRef = useRef(false)

  useEffect(() => {
    if (!user) return
    cancelledRef.current = false
    // Bump generation so any in-flight async work owned by the provider
    // (loadHistory, jumpToMessage) can detect that it was started in a
    // prior connection cycle and skip its dispatch.
    generationRef.current += 1

    const safeDispatch = (action) => {
      if (cancelledRef.current) return
      dispatch(action)
    }

    const dmSub = subscribeToUserRoomEvents(nats, (evt) => {
      if (evt?.type === 'new_message') {
        safeDispatch({ type: 'MESSAGE_RECEIVED', event: evt })
      }
    })

    const openChannelSub = (roomId) => {
      if (channelSubs.current.has(roomId)) return
      const sub = subscribeToRoomEvents(nats, { roomId }, (evt) => {
        if (evt?.type === 'new_message') {
          const hasMention = (evt.mentions ?? []).some(
            (p) => p.account === user.account
          )
          safeDispatch({ type: 'MESSAGE_RECEIVED', event: { ...evt, hasMention } })
        }
      })
      channelSubs.current.set(roomId, sub)
    }

    const closeChannelSub = (roomId) => {
      const sub = channelSubs.current.get(roomId)
      if (sub) {
        sub.unsubscribe()
        channelSubs.current.delete(roomId)
      }
    }

    const subUpdate = subscribeToSubscriptionUpdates(nats, (evt) => {
      if (cancelledRef.current) return
      if (evt.action === 'added' && evt.subscription?.roomId) {
        getRoom(nats, { roomId: evt.subscription.roomId })
          .then((room) => {
            if (cancelledRef.current || !room) return
            // DM rooms have no canonical Room.Name server-side — the friendly
            // text lives on the user's Subscription. Stash that here so the
            // sidebar + header can fall back to it via roomDisplayName(room).
            const merged = evt.subscription?.name
              ? { ...room, subscriptionName: evt.subscription.name }
              : room
            safeDispatch({ type: 'ROOM_ADDED', room: merged })
            if (room.type === 'channel') openChannelSub(room.id)
          })
          .catch(() => {})
      } else if (evt.action === 'removed') {
        const roomId = evt.subscription?.roomId
        if (!roomId) return
        closeChannelSub(roomId)
        safeDispatch({ type: 'ROOM_REMOVED', roomId })
      }
    })

    const metaUpdate = subscribeToRoomMetadataUpdates(nats, (evt) => {
      safeDispatch({
        type: 'ROOM_METADATA_UPDATED',
        roomId: evt.roomId,
        name: evt.name,
        userCount: evt.userCount,
        lastMsgAt: evt.lastMsgAt,
      })
    })

    listRooms(nats)
      .then((resp) => {
        if (cancelledRef.current) return
        const rooms = resp.rooms ?? []
        safeDispatch({ type: 'ROOMS_LOADED', rooms })
        for (const r of rooms) {
          if (r.type === 'channel') openChannelSub(r.id)
        }
      })
      .catch((err) => {
        if (!cancelledRef.current) safeDispatch({ type: 'ROOMS_FAILED', error: err.message })
      })

    return () => {
      cancelledRef.current = true
      dmSub.unsubscribe()
      subUpdate.unsubscribe()
      metaUpdate.unsubscribe()
      for (const sub of channelSubs.current.values()) sub.unsubscribe()
      channelSubs.current.clear()
      // RESET runs even when cancelled — it IS the cleanup.
      dispatch({ type: 'RESET' })
    }
  }, [user, nats, dispatch, generationRef])
}
