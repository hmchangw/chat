import { createContext, useCallback, useContext, useMemo, useReducer, useRef } from 'react'
import { useNats } from '@/context/NatsContext'
import { BUFFER_MODE, initialState, roomEventsReducer } from './reducer'
import { useRoomSubscriptions } from './useRoomSubscriptions'
import { fetchMessageHistory, fetchSurroundingMessages } from '@/api'
import { normalizeHistoricalMessages } from '@/lib/normalizeMessage'

const RoomEventsContext = createContext(null)

export function RoomEventsProvider({ children }) {
  const nats = useNats()
  const { user } = nats
  const [state, dispatch] = useReducer(roomEventsReducer, initialState)

  // Refs threaded into the subscription hook + the async callbacks below
  // so a fetch started in connection cycle N drops its dispatch if cycle
  // N+1 has already begun. See useRoomSubscriptions for the full story.
  const generationRef = useRef(0)
  const inflightHistory = useRef(new Map())
  const stateRef = useRef(state)
  stateRef.current = state

  useRoomSubscriptions(nats, dispatch, generationRef)

  const loadHistory = useCallback(
    async (roomId) => {
      if (!user || !roomId) return
      const prev = stateRef.current.roomState[roomId]
      if (prev?.hasLoadedHistory) return
      if (inflightHistory.current.has(roomId)) return inflightHistory.current.get(roomId)

      const gen = generationRef.current
      const promise = (async () => {
        try {
          const resp = await fetchMessageHistory(nats, { roomId, siteId: user.siteId, limit: 50 })
          // history-service returns cassandra.Message (messageId/msg); the
          // reducer + UI consume model.Message (id/content). Normalize here
          // so msg.id is defined for click handlers downstream.
          const asc = [...(resp.messages ?? [])].reverse()
          const normalized = normalizeHistoricalMessages(asc)
          if (generationRef.current === gen) dispatch({ type: 'HISTORY_LOADED', roomId, messages: normalized })
        } catch (err) {
          if (generationRef.current === gen) dispatch({ type: 'HISTORY_FAILED', roomId, error: err.message })
          throw err
        } finally {
          inflightHistory.current.delete(roomId)
        }
      })()
      inflightHistory.current.set(roomId, promise)
      return promise
    },
    [user, nats]
  )

  const setActiveRoom = useCallback((roomId) => {
    dispatch({ type: 'SET_ACTIVE_ROOM', roomId })
  }, [])

  const jumpToMessage = useCallback(
    async (roomId, messageId) => {
      if (!user || !roomId || !messageId) return
      const summary = stateRef.current.summaries.find((r) => r.id === roomId)
      const siteId = summary?.siteId ?? user.siteId
      const gen = generationRef.current
      try {
        const resp = await fetchSurroundingMessages(nats, { roomId, siteId, messageId })
        if (generationRef.current !== gen) return
        const messages = normalizeHistoricalMessages(resp.messages ?? [])
        dispatch({
          type: 'REPLACE_ROOM_BUFFER',
          roomId,
          messages,
          focusMessageId: messageId,
        })
      } catch (err) {
        if (generationRef.current === gen) {
          dispatch({ type: 'HISTORY_FAILED', roomId, error: err.message })
        }
        throw err
      }
    },
    [user, nats]
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
