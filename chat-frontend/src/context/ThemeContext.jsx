import { createContext, useContext, useEffect, useRef, useState } from 'react'

const STORAGE_KEY = 'theme'
const MEDIA_QUERY = '(prefers-color-scheme: dark)'

const ThemeContext = createContext(null)

function resolveInitialTheme() {
  let stored = null
  try {
    stored = localStorage.getItem(STORAGE_KEY)
  } catch {
    // localStorage unavailable; treat as no stored value
  }
  if (stored === 'light' || stored === 'dark') {
    return { theme: stored, source: 'user' }
  }
  if (typeof window.matchMedia === 'function') {
    const prefersDark = window.matchMedia(MEDIA_QUERY).matches
    return { theme: prefersDark ? 'dark' : 'light', source: 'system' }
  }
  return { theme: 'light', source: 'system' }
}

export function ThemeProvider({ children }) {
  const [state, setState] = useState(() => resolveInitialTheme())
  const sourceRef = useRef(state.source)

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', state.theme)
  }, [state.theme])

  useEffect(() => {
    sourceRef.current = state.source
  }, [state.source])

  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const mql = window.matchMedia(MEDIA_QUERY)
    const handler = (event) => {
      if (sourceRef.current !== 'system') return
      setState({ theme: event.matches ? 'dark' : 'light', source: 'system' })
    }
    mql.addEventListener('change', handler)
    return () => mql.removeEventListener('change', handler)
  }, [])

  function setTheme(next) {
    if (next !== 'light' && next !== 'dark') return
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // tolerate private mode / quota errors
    }
    setState({ theme: next, source: 'user' })
  }

  function toggleTheme() {
    setTheme(state.theme === 'dark' ? 'light' : 'dark')
  }

  return (
    <ThemeContext.Provider value={{ theme: state.theme, source: state.source, setTheme, toggleTheme }}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (ctx === null) {
    throw new Error('useTheme must be used within a ThemeProvider')
  }
  return ctx
}
