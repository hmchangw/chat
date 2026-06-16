import { createContext, useCallback, useContext, useMemo, useState } from 'react'

const STORAGE_KEY = 'debug'

// Ordered debug verbosity levels. 'off' sends no X-Debug header; the others
// are sent verbatim as the header value so the backend can scale diagnostics.
export const DEBUG_LEVELS = ['off', 'flow', 'debug', 'trace']

const DebugContext = createContext(null)

function normalizeLevel(value) {
  if (value === '1') return 'debug' // legacy boolean-on value
  return DEBUG_LEVELS.includes(value) ? value : 'off'
}

function resolveInitialLevel() {
  try {
    return normalizeLevel(localStorage.getItem(STORAGE_KEY))
  } catch {
    // localStorage unavailable; treat as off
    return 'off'
  }
}

function persistLevel(level) {
  try {
    if (level === 'off') localStorage.removeItem(STORAGE_KEY)
    else localStorage.setItem(STORAGE_KEY, level)
  } catch {
    // tolerate private mode / quota errors
  }
}

export function DebugProvider({ children }) {
  const [level, setLevelState] = useState(resolveInitialLevel)

  const setLevel = useCallback((next) => {
    const normalized = normalizeLevel(next)
    persistLevel(normalized)
    setLevelState(normalized)
  }, [])

  const value = useMemo(() => ({ level, setLevel }), [level, setLevel])

  return <DebugContext.Provider value={value}>{children}</DebugContext.Provider>
}

export function useDebug() {
  const ctx = useContext(DebugContext)
  if (ctx === null) {
    throw new Error('useDebug must be used within a DebugProvider')
  }
  return ctx
}
