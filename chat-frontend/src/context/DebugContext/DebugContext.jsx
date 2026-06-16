import { createContext, useCallback, useContext, useMemo, useState } from 'react'

const STORAGE_KEY = 'debug'
const ENABLED_VALUE = '1'

const DebugContext = createContext(null)

function resolveInitialDebug() {
  try {
    return localStorage.getItem(STORAGE_KEY) === ENABLED_VALUE
  } catch {
    // localStorage unavailable; treat as disabled
    return false
  }
}

function persistDebug(on) {
  try {
    if (on) localStorage.setItem(STORAGE_KEY, ENABLED_VALUE)
    else localStorage.removeItem(STORAGE_KEY)
  } catch {
    // tolerate private mode / quota errors
  }
}

export function DebugProvider({ children }) {
  const [debug, setDebugState] = useState(resolveInitialDebug)

  const setDebug = useCallback((next) => {
    const on = Boolean(next)
    persistDebug(on)
    setDebugState(on)
  }, [])

  const toggleDebug = useCallback(() => {
    setDebugState((prev) => {
      const next = !prev
      persistDebug(next)
      return next
    })
  }, [])

  const value = useMemo(
    () => ({ debug, setDebug, toggleDebug }),
    [debug, setDebug, toggleDebug],
  )

  return <DebugContext.Provider value={value}>{children}</DebugContext.Provider>
}

export function useDebug() {
  const ctx = useContext(DebugContext)
  if (ctx === null) {
    throw new Error('useDebug must be used within a DebugProvider')
  }
  return ctx
}
