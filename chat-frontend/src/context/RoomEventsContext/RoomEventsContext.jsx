import { createContext, useCallback, useContext, useMemo, useReducer, useRef } from 'react'
import { useNats } from '@/context/NatsContext'
import { BUFFER_MODE, initialState, roomEventsReducer } from './reducer'
import { useRoomSubscriptions } from './useRoomSubscriptions'
import { fetchMessageHistory, fetchSurroundingMessages } from '@/api'

const RoomEventsContext = createContext(null)

export function RoomEventsProvider({ children }) {
  const nats = useNats()
  const { user } = nats
  const [state, dispatch] = useReducer(roomEventsReducer, initialState)

  const inflightHistory = useRef(new Map())
  const stateRef = useRef(state)
  stateRef.current = state

  // The hook owns the generation counter that gates stale dispatches
  // from this provider's async callbacks below.
  const { currentGeneration } = useRoomSubscriptions(nats, dispatch)

  const loadHistory = useCallback(
    async (roomId) => {
      if (!user || !roomId) return
      const prev = stateRef.current.roomState[roomId]
      if (prev?.hasLoadedHistory) return
      if (inflightHistory.current.has(roomId)) return inflightHistory.current.get(roomId)

      const gen = currentGeneration()
      const promise = (async () => {
        try {
          const resp = await fetchMessageHistory(nats, { roomId, siteId: user.siteId, limit: 50 })
          // history-service ships newest-first; the UI reads chronological.
          // Normalisation to the broadcast `Message` shape now happens inside
          // the api op.
          const asc = [...(resp.messages ?? [])].reverse()
          if (currentGeneration() === gen) dispatch({ type: 'HISTORY_LOADED', roomId, messages: asc })
        } catch (err) {
          if (currentGeneration() === gen) dispatch({ type: 'HISTORY_FAILED', roomId, error: err.message })
          throw err
        } finally {
          inflightHistory.current.delete(roomId)
        }
      })()
      inflightHistory.current.set(roomId, promise)
      return promise
    },
    [user, nats, currentGeneration]
  )

  const setActiveRoom = useCallback((roomId) => {
    dispatch({ type: 'SET_ACTIVE_ROOM', roomId })
  }, [])

  const jumpToMessage = useCallback(
    async (roomId, messageId) => {
      if (!user || !roomId || !messageId) return
      const summary = stateRef.current.summaries.find((r) => r.id === roomId)
      const siteId = summary?.siteId ?? user.siteId
      const gen = currentGeneration()
      try {
        const resp = await fetchSurroundingMessages(nats, { roomId, siteId, messageId })
        if (currentGeneration() !== gen) return
        dispatch({
          type: 'REPLACE_ROOM_BUFFER',
          roomId,
          messages: resp.messages,
          focusMessageId: messageId,
        })
      } catch (err) {
        if (currentGeneration() === gen) {
          dispatch({ type: 'HISTORY_FAILED', roomId, error: err.message })
        }
        throw err
      }
    },
    [user, nats, currentGeneration]
  )

  const resetToLiveTail = useCallback((roomId) => {
    if (!roomId) return
    dispatch({ type: 'RESET_TO_LIVE_TAIL', roomId })
  }, [])

  const value = useMemo(
    () => ({ state, dispatch, loadHistory, setActiveRoom, jumpToMessage, resetToLiveTail }),
    [state, dispatch, loadHistory, setActiveRoom, jumpToMessage, resetToLiveTail]
  )

  return <RoomEventsContext.Provider value={value}>{children}</RoomEventsContext.Provider>
}

function useRoomEventsInternal() {
  const ctx = useContext(RoomEventsContext)
  if (!ctx) throw new Error('RoomEvents hooks must be used inside RoomEventsProvider')
  return ctx
}

export function useRoomEvents(roomId) {
  const { state, dispatch, loadHistory, jumpToMessage, resetToLiveTail } = useRoomEventsInternal()
  const room = state.roomState[roomId]
  const load = useCallback(() => loadHistory(roomId), [loadHistory, roomId])
  const jump = useCallback(
    (messageId) => jumpToMessage(roomId, messageId),
    [jumpToMessage, roomId]
  )
  const reset = useCallback(() => resetToLiveTail(roomId), [resetToLiveTail, roomId])
  return useMemo(
    () => ({
      messages: room?.messages ?? [],
      hasLoadedHistory: !!room?.hasLoadedHistory,
      historyError: room?.historyError ?? null,
      loadHistory: load,
      bufferMode: room?.bufferMode ?? BUFFER_MODE.LIVE,
      pendingCount: room?.pendingLiveMessages?.length ?? 0,
      focusMessageId: room?.focusMessageId ?? null,
      jumpToMessage: jump,
      resetToLiveTail: reset,
      dispatch,
    }),
    [room, load, jump, reset, dispatch]
  )
}

export function useRoomSummaries() {
  const { state, setActiveRoom, jumpToMessage } = useRoomEventsInternal()
  return {
    summaries: state.summaries,
    setActiveRoom,
    jumpToMessage,
    error: state.roomsError,
  }
}

export function useRoomDispatch() {
  const ctx = useContext(RoomEventsContext)
  if (!ctx) throw new Error('useRoomDispatch must be used inside RoomEventsProvider')
  return ctx.dispatch
}

/**
 * Partition the room summaries into the three sidebar buckets:
 * Favorite, Apps, Channels and DMs. Bucket membership comes from the
 * `BUCKETS_LOADED` dispatch (fetched by useRoomSubscriptions on login)
 * + the per-type ROOM_ADDED / ROOM_REMOVED maintenance the reducer
 * applies.
 *
 * Returns an ordered array of `{key, title, rooms}` sections so the
 * sidebar can render headers + rows without re-deriving the split.
 *
 * Per-room subscription metadata (subscription.Name + HRInfo) is
 * merged onto each room here so `roomDisplayName` resolves the user's
 * preferred name for channels and the counterpart's HRInfo for DMs
 * without the underlying summary structure carrying those fields.
 */
export function useSidebarSections() {
  const { state } = useRoomEventsInternal()
  const { summaries, favoriteIds, appIds, channelDmIds, subscriptions } = state
  return useMemo(() => {
    const enrich = (room) => {
      const sub = subscriptions[room.id]
      if (!sub) return room
      return {
        ...room,
        subscriptionName: sub.name ?? room.subscriptionName,
        hrInfo: sub.hrInfo ?? room.hrInfo,
      }
    }
    const favorite = []
    const apps = []
    const channelDm = []
    for (const room of summaries) {
      if (favoriteIds.has(room.id)) favorite.push(enrich(room))
      else if (appIds.has(room.id)) apps.push(enrich(room))
      else if (channelDmIds.has(room.id)) channelDm.push(enrich(room))
    }
    return [
      { key: 'favorite',  title: 'Favorite',          rooms: favorite },
      { key: 'apps',      title: 'Apps',              rooms: apps },
      { key: 'channelDm', title: 'Channels and DMs',  rooms: channelDm },
    ]
  }, [summaries, favoriteIds, appIds, channelDmIds, subscriptions])
}

/**
 * Read the current user's Subscription record for a single room.
 *
 * Returns the full `model.Subscription` (roles, alert, hasMention,
 * lastSeenAt, hrInfo, …) so components don't have to re-fetch via
 * `member.list` or re-derive permission from message events. Returns
 * `undefined` until the bucket bootstrap completes or a
 * `subscription.update added` event lands.
 *
 * Hook re-renders only when the subscription for THIS room changes,
 * not when other rooms' subscriptions update — driven by the
 * dictionary's reference identity (the reducer rebuilds the map only
 * on writes touching `roomId`).
 */
export function useSubscription(roomId) {
  const { state } = useRoomEventsInternal()
  return roomId ? state.subscriptions[roomId] : undefined
}
