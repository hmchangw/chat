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
