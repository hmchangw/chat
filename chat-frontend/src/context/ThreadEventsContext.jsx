import { createContext, useCallback, useContext, useEffect, useReducer, useRef } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from './NatsContext'
import { generateMessageID } from '../lib/idgen'
import { msgSend, msgThread } from '../lib/subjects'
import { threadEventsReducer, initialState } from '../lib/threadEventsReducer'

const ThreadEventsContext = createContext(null)

export function ThreadEventsProvider({ children }) {
  const { user, request, publish } = useNats()
  const [state, dispatch] = useReducer(threadEventsReducer, initialState)
  const generationRef = useRef(0)
  const stateRef = useRef(state)
  stateRef.current = state

  // Reset on logout.
  useEffect(() => {
    if (!user) dispatch({ type: 'RESET' })
  }, [user])

  const openThread = useCallback(
    (parent) => {
      // Short-circuit if it's already the same parent (mirrors reducer guard).
      if (stateRef.current.activeParent?.messageId === parent.messageId) return
      const myGen = ++generationRef.current
      dispatch({ type: 'OPEN_THREAD', parent })
      if (!user) return
      const subj = msgThread(user.account, parent.roomId, parent.siteId)
      request(subj, { threadMessageId: parent.messageId, limit: 50 })
        .then((resp) => {
          if (myGen !== generationRef.current) return
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
    [user, request]
  )

  const closeThread = useCallback(() => {
    generationRef.current++
    dispatch({ type: 'CLOSE_THREAD' })
  }, [])

  // publishReply is synchronous — `publish` is sync void and throws if not
  // connected. Callers wrap in try/catch.
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
      publish(msgSend(user.account, parent.roomId, parent.siteId), payload)
    },
    [user, publish]
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
        optimistic.quotedParentMessage = {
          id: opts.quotedParentMessageId,
          senderName: opts.quotedSnapshot?.senderName,
          content: opts.quotedSnapshot?.content,
        }
      }
      dispatch({ type: 'REPLY_SENT_LOCAL', message: optimistic })
      try {
        publishReply(id, content.trim(), opts)
        // Ch.8 task 8.3 adds: roomDispatch({ type: 'OWN_THREAD_REPLY_SENT', ... })
      } catch (err) {
        dispatch({ type: 'REPLY_SEND_FAILED', messageId: id, error: err?.message ?? String(err) })
      }
    },
    [user, publishReply]
  )

  const retryReply = useCallback(
    (messageId) => {
      const row = stateRef.current.messages.find((m) => m.id === messageId)
      if (!row) return
      dispatch({ type: 'REPLY_RETRIED', messageId })
      try {
        publishReply(
          messageId,
          row.content,
          row.quotedParentMessage ? { quotedParentMessageId: row.quotedParentMessage.id } : undefined
        )
        // Ch.8 task 8.3 adds: roomDispatch({ type: 'OWN_THREAD_REPLY_SENT', ... })
      } catch (err) {
        dispatch({ type: 'REPLY_SEND_FAILED', messageId, error: err?.message ?? String(err) })
      }
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
  }

  return <ThreadEventsContext.Provider value={value}>{children}</ThreadEventsContext.Provider>
}

export function useThreadEvents() {
  const ctx = useContext(ThreadEventsContext)
  if (!ctx) throw new Error('useThreadEvents must be used inside ThreadEventsProvider')
  return ctx
}
