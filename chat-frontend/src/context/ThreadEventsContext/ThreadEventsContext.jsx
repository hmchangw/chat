import { createContext, useCallback, useContext, useEffect, useReducer, useRef } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from '../NatsContext/NatsContext'
import {
  useRoomDispatch,
  useRegisterThreadReplyHandler,
  useRegisterThreadMessageMutationHandler,
} from '../RoomEventsContext/RoomEventsContext'
import { generateMessageID } from '@/lib/idgen'
import { fetchThreadMessages, sendMessage, formatAsyncJobError } from '@/api'
import { threadEventsReducer, initialState } from './reducer'

const ThreadEventsContext = createContext(null)

export function ThreadEventsProvider({ children }) {
  const nats = useNats()
  const { user } = nats
  const roomDispatch = useRoomDispatch()
  const registerThreadReplyHandler = useRegisterThreadReplyHandler()
  const registerThreadMessageMutationHandler = useRegisterThreadMessageMutationHandler()
  const [state, dispatch] = useReducer(threadEventsReducer, initialState)
  const generationRef = useRef(0)
  const stateRef = useRef(state)
  stateRef.current = state

  // Reset on logout.
  useEffect(() => {
    if (!user) dispatch({ type: 'RESET' })
  }, [user])

  // Bridge live room-channel thread replies → THREAD_REPLY_RECEIVED.
  useEffect(() => {
    const unsubscribe = registerThreadReplyHandler((evt) => {
      dispatch({
        type: 'THREAD_REPLY_RECEIVED',
        parentId: evt.parentMessageId,
        message: evt.message,
      })
    })
    return unsubscribe
  }, [registerThreadReplyHandler])

  // Bridge live edit/delete mutations → REPLY_EDITED / REPLY_DELETED.
  useEffect(() => {
    const unsubscribe = registerThreadMessageMutationHandler((mut) => {
      if (mut.kind === 'edited') {
        dispatch({
          type: 'REPLY_EDITED',
          messageId: mut.messageId,
          content: mut.content ?? '',
          editedAt: mut.editedAt,
        })
      } else if (mut.kind === 'deleted') {
        dispatch({ type: 'REPLY_DELETED', messageId: mut.messageId })
      }
    })
    return unsubscribe
  }, [registerThreadMessageMutationHandler])

  const openThread = useCallback(
    (parent) => {
      // Short-circuit if it's already the same parent (mirrors reducer guard).
      if (stateRef.current.activeParent?.messageId === parent.messageId) return
      const myGen = ++generationRef.current
      dispatch({ type: 'OPEN_THREAD', parent })
      if (!user) return
      fetchThreadMessages(nats, {
        roomId: parent.roomId,
        siteId: parent.siteId,
        threadMessageId: parent.messageId,
        limit: 50,
      })
        .then((resp) => {
          if (myGen !== generationRef.current) return
          // The api op already normalises into broadcast shape (id/content);
          // hand it straight to the reducer.
          dispatch({ type: 'HISTORY_LOADED', parentId: parent.messageId, resp })
        })
        .catch((err) => {
          if (myGen !== generationRef.current) return
          dispatch({
            type: 'HISTORY_FAILED',
            parentId: parent.messageId,
            error: err?.message ?? String(err),
          })
        })
    },
    [user, nats]
  )

  const closeThread = useCallback(() => {
    generationRef.current++
    dispatch({ type: 'CLOSE_THREAD' })
  }, [])

  // publishReply returns the sendMessage promise — it resolves once
  // message-gatekeeper acks and rejects on a validation error (or a wire
  // failure). Throws synchronously only when there's no active thread; callers
  // attach .then/.catch for the async outcome.
  const publishReply = useCallback(
    (id, content, opts) => {
      const parent = stateRef.current.activeParent
      if (!parent || !user) throw new Error('no active thread')
      const payload = {
        id,
        content,
        requestId: uuidv4(),
        threadParentMessageId: parent.messageId,
        threadParentMessageCreatedAt: parent.createdAtMs,
      }
      if (opts?.quotedParentMessageId) payload.quotedParentMessageId = opts.quotedParentMessageId
      return sendMessage(nats, { roomId: parent.roomId, siteId: parent.siteId, payload })
    },
    [user, nats]
  )

  const sendReply = useCallback(
    (content, opts) => {
      const parent = stateRef.current.activeParent
      if (!parent || !user || !content || !content.trim()) return
      const id = generateMessageID()
      const optimistic = {
        id,
        content: content.trim(),
        createdAt: new Date().toISOString(),
        sender: { account: user.account },
        threadParentMessageId: parent.messageId,
        threadParentMessageCreatedAt: new Date(parent.createdAtMs).toISOString(),
        _local: true,
      }
      if (opts?.quotedParentMessageId) {
        // Mirror the server's cassandra.QuotedParentMessage shape so the
        // optimistic snapshot renders identically to what later broadcasts
        // / history loads will carry (messageId / sender.engName / msg).
        const sn = opts.quotedSnapshot?.senderName
        optimistic.quotedParentMessage = {
          messageId: opts.quotedParentMessageId,
          sender: { engName: sn, account: sn },
          msg: opts.quotedSnapshot?.content,
        }
      }
      dispatch({ type: 'REPLY_SENT_LOCAL', message: optimistic })
      // OWN_THREAD_REPLY_SENT bumps the parent's tcount and must fire only on a
      // confirmed send — moved into the success branch now that sendMessage is
      // async (the gatekeeper ack is the confirmation). A rejected send tags
      // the optimistic row failed instead, surfacing the (previously silent)
      // validation error.
      publishReply(id, content.trim(), opts)
        .then(() => {
          if (parent) {
            // replyId lets the room reducer dedupe the inbound echo on tcount.
            roomDispatch({
              type: 'OWN_THREAD_REPLY_SENT',
              roomId: parent.roomId,
              parentId: parent.messageId,
              replyId: id,
            })
          }
        })
        .catch((err) => {
          dispatch({ type: 'REPLY_SEND_FAILED', messageId: id, error: formatAsyncJobError(err) })
        })
    },
    [user, publishReply, roomDispatch]
  )

  const retryReply = useCallback(
    (messageId) => {
      const row = stateRef.current.messages.find((m) => m.id === messageId)
      if (!row) return
      dispatch({ type: 'REPLY_RETRIED', messageId })
      // Intentionally NOT dispatching OWN_THREAD_REPLY_SENT on success. A retry
      // is the continuation of a logical send, not a new one; double-bumping
      // the parent's tcount would inflate the badge across repeated failures
      // and recoveries. The optimistic count stays at 0 until the next
      // msg.thread reload reconciles from the authoritative server state.
      publishReply(
        messageId,
        row.content,
        row.quotedParentMessage
          ? { quotedParentMessageId: row.quotedParentMessage.messageId ?? row.quotedParentMessage.id }
          : undefined
      ).catch((err) => {
        dispatch({ type: 'REPLY_SEND_FAILED', messageId, error: formatAsyncJobError(err) })
      })
    },
    [publishReply]
  )

  const dismissReply = useCallback((messageId) => {
    dispatch({ type: 'REPLY_DISMISSED', messageId })
  }, [])

  const value = {
    activeParent: state.activeParent,
    messages: state.messages,
    hasLoadedHistory: state.hasLoadedHistory,
    historyLoading: state.historyLoading,
    historyError: state.historyError,
    openThread,
    closeThread,
    sendReply,
    retryReply,
    dismissReply,
    dispatch,
  }

  return <ThreadEventsContext.Provider value={value}>{children}</ThreadEventsContext.Provider>
}

export function useThreadEvents() {
  const ctx = useContext(ThreadEventsContext)
  if (!ctx) throw new Error('useThreadEvents must be used inside ThreadEventsProvider')
  return ctx
}
