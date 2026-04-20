import { createContext, useCallback, useContext, useMemo, useReducer, useRef } from 'react'
import { useNats } from './NatsContext'
import { initialState, roomEventsReducer } from '../lib/roomEventsReducer'
import { msgHistory } from '../lib/subjects'

const RoomEventsContext = createContext(null)

export function RoomEventsProvider({ children }) {
  const { user, request } = useNats()
  const [state, dispatch] = useReducer(roomEventsReducer, initialState)
  const inflightHistory = useRef(new Map())

  const loadHistory = useCallback(
    async (roomId) => {
      if (!user || !roomId) return
      const prev = state.roomState[roomId]
      if (prev?.hasLoadedHistory) return
      if (inflightHistory.current.has(roomId)) return inflightHistory.current.get(roomId)

      const promise = (async () => {
        try {
          const resp = await request(msgHistory(user.account, roomId, user.siteId), { limit: 50 })
          const asc = [...(resp.messages ?? [])].reverse()
          dispatch({ type: 'HISTORY_LOADED', roomId, messages: asc })
        } catch (err) {
          dispatch({ type: 'HISTORY_FAILED', roomId, error: err.message })
          throw err
        } finally {
          inflightHistory.current.delete(roomId)
        }
      })()
      inflightHistory.current.set(roomId, promise)
      return promise
    },
    [user, request, state.roomState]
  )

  const setActiveRoom = useCallback((roomId) => {
    dispatch({ type: 'SET_ACTIVE_ROOM', roomId })
  }, [])

  const value = useMemo(
    () => ({ state, dispatch, loadHistory, setActiveRoom }),
    [state, loadHistory, setActiveRoom]
  )

  return <RoomEventsContext.Provider value={value}>{children}</RoomEventsContext.Provider>
}

function useRoomEventsInternal() {
  const ctx = useContext(RoomEventsContext)
  if (!ctx) throw new Error('RoomEvents hooks must be used inside RoomEventsProvider')
  return ctx
}

export function useRoomEvents(roomId) {
  const { state, loadHistory } = useRoomEventsInternal()
  const room = state.roomState[roomId]
  return {
    messages: room?.messages ?? [],
    hasLoadedHistory: !!room?.hasLoadedHistory,
    historyError: room?.historyError ?? null,
    loadHistory: () => loadHistory(roomId),
  }
}

export function useRoomSummaries() {
  const { state, setActiveRoom } = useRoomEventsInternal()
  return {
    summaries: state.summaries,
    setActiveRoom,
    error: state.roomsError,
  }
}
