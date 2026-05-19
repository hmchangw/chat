import { useCallback, useEffect, useRef, useState } from 'react'
import { getUnreadCount } from '@/api'

/** Trailing-debounce window for the message-driven refetch. A burst of
 *  incoming messages collapses to one `subscription.count` RPC at the
 *  trailing edge — mirrors useRoomSubscriptions' markRoomRead debounce. */
const MSG_REFETCH_DEBOUNCE_MS = 500

/**
 * App-wide unread total for the header badge, sourced from the
 * `subscription.count` RPC instead of derived from `state.summaries`.
 *
 * Refetch triggers:
 *   - mount / reconnect (`nats` identity) and active-room change —
 *     fetched immediately;
 *   - every received message (`msgRecvSeq` bumps) — debounced
 *     {@link MSG_REFETCH_DEBOUNCE_MS}ms so a chatty channel doesn't
 *     generate one RPC per message.
 *
 * A monotonic request id makes the latest fetch win: a slow earlier
 * request (or one resolving after unmount) is dropped.
 *
 * @param {{ user: { account: string, siteId: string } }} nats
 * @param {string|null} activeRoomId
 * @param {number} [msgRecvSeq] reducer's accepted-message counter
 * @returns {number} unread total (0 until the first fetch resolves)
 */
export function useUnreadCount(nats, activeRoomId, msgRecvSeq) {
  const [total, setTotal] = useState(0)
  const reqIdRef = useRef(0)

  const fetchNow = useCallback(() => {
    const myId = ++reqIdRef.current
    getUnreadCount(nats)
      .then((resp) => {
        if (myId === reqIdRef.current) setTotal(resp.count ?? 0)
      })
      .catch(() => {
        if (myId === reqIdRef.current) setTotal(0)
      })
  }, [nats])

  // Immediate: mount, reconnect, active-room change.
  useEffect(() => {
    fetchNow()
  }, [fetchNow, activeRoomId])

  // Debounced: a received message moved the (possibly remote) unread
  // total. Skip the seed value so this doesn't double-fire on mount.
  useEffect(() => {
    if (!msgRecvSeq) return undefined
    const t = setTimeout(fetchNow, MSG_REFETCH_DEBOUNCE_MS)
    return () => clearTimeout(t)
  }, [fetchNow, msgRecvSeq])

  return total
}
