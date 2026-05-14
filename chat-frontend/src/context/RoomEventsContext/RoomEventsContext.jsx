import { createContext, useCallback, useContext, useEffect, useMemo, useReducer, useRef } from 'react'
import { useNats } from '../NatsContext/NatsContext'
import { BUFFER_MODE, initialState, roomEventsReducer } from './reducer'
import {
  fetchMessageHistory,
  fetchSurroundingMessages,
  getRoom,
  listRooms,
  subscribeToRoomEvents,
  subscribeToRoomMetadataUpdates,
  subscribeToSubscriptionUpdates,
  subscribeToUserRoomEvents,
} from '@/api'
import { normalizeHistoricalMessages } from '@/lib/normalizeMessage'

const RoomEventsContext = createContext(null)

export function RoomEventsProvider({ children }) {
  const nats = useNats()
  const { user } = nats
  const [state, dispatch] = useReducer(roomEventsReducer, initialState)
  const inflightHistory = useRef(new Map())
  const stateRef = useRef(state)
  stateRef.current = state

  const channelSubs = useRef(new Map())
  const cancelledRef = useRef(false)
  const generationRef = useRef(0)

  useEffect(() => {
    if (!user) return
    cancelledRef.current = false
    const generation = ++generationRef.current

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
      dispatch({ type: 'RESET' })   // RESET runs even when cancelled — it's the cleanup itself
    }
  }, [user, nats])

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
