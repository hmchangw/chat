import { useCallback, useRef, useState } from 'react'
import { useNats } from '../context/NatsContext'
import { searchMessages, searchRooms } from './subjects'

export const PAGE_SIZE = 20

const initialState = {
  hits: [],
  total: 0,
  loading: false,
  error: null,
  done: false,
}

// usePager fetches paginated search hits for either 'rooms' or 'messages'.
// State progresses in append-only fashion: each load() call fetches the next
// PAGE_SIZE window and appends to hits. start() is a one-shot guard so
// callers can trigger the first load from an effect without double-firing
// under React StrictMode.
export function usePager(kind, text) {
  const { user, request } = useNats()
  const [state, setState] = useState(initialState)
  const startedRef = useRef(false)
  const offsetRef = useRef(0)
  const loadingRef = useRef(false)
  const doneRef = useRef(false)

  const load = useCallback(async () => {
    if (!user || loadingRef.current || doneRef.current) return
    const q = (text ?? '').trim()
    if (!q) return

    loadingRef.current = true
    setState((s) => ({ ...s, loading: true, error: null }))

    try {
      const subject =
        kind === 'rooms'
          ? searchRooms(user.account)
          : searchMessages(user.account)
      const body =
        kind === 'rooms'
          ? {
              searchText: q,
              scope: 'all',
              size: PAGE_SIZE,
              offset: offsetRef.current,
            }
          : { searchText: q, size: PAGE_SIZE, offset: offsetRef.current }

      const resp = await request(subject, body)
      const newHits = resp?.results ?? []
      const newTotal = resp?.total ?? 0
      offsetRef.current += newHits.length
      const done =
        newHits.length === 0 || offsetRef.current >= newTotal
      doneRef.current = done

      setState((s) => ({
        hits: [...s.hits, ...newHits],
        total: newTotal,
        loading: false,
        error: null,
        done,
      }))
    } catch (err) {
      setState((s) => ({ ...s, loading: false, error: err.message }))
    } finally {
      loadingRef.current = false
    }
  }, [user, request, kind, text])

  const start = useCallback(() => {
    if (startedRef.current) return
    startedRef.current = true
    load()
  }, [load])

  return { ...state, load, start }
}
