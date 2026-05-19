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
 *   - mount / reconnect (`nats` identity) — fetched immediately;
 *   - `readSeq` bumps (a `markRoomRead` RPC has resolved, so the
 *     server `lastSeenAt` write is committed) — fetched immediately,
 *     AFTER the read rather than racing it;
 *   - `msgRecvSeq` bumps (a message was received) — debounced
 *     {@link MSG_REFETCH_DEBOUNCE_MS}ms so a chatty channel doesn't
 *     generate one RPC per message.
 *
 * A monotonic request id makes the latest fetch win: a slow earlier
 * request (or one resolving after unmount) is dropped.
 *
 * @param {{ user: { account: string, siteId: string } }} nats
 * @param {number} [readSeq] reducer's post-mark-read counter
 * @param {number} [msgRecvSeq] reducer's accepted-message counter
 * @returns {number} unread total (0 until the first fetch resolves)
 */
export function useUnreadCount(nats, readSeq, msgRecvSeq) {
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

  // Immediate: mount, reconnect, and after a mark-read RPC resolves
  // (readSeq) — pulled once lastSeenAt is committed, not racing it.
  useEffect(() => {
    fetchNow()
  }, [fetchNow, readSeq])

  // Debounced: a received message moved the (possibly remote) unread
  // total. Skip the seed value so this doesn't double-fire on mount.
  useEffect(() => {
    if (!msgRecvSeq) return undefined
    const t = setTimeout(fetchNow, MSG_REFETCH_DEBOUNCE_MS)
    return () => clearTimeout(t)
  }, [fetchNow, msgRecvSeq])

  return total
}
