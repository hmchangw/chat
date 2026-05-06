import { vi } from 'vitest'

export function setMatchMedia(matches) {
  const listeners = new Set()
  const mql = {
    matches,
    media: '(prefers-color-scheme: dark)',
    addEventListener: (_event, cb) => listeners.add(cb),
    removeEventListener: (_event, cb) => listeners.delete(cb),
    dispatchEvent: (event) => {
      mql.matches = event.matches
      listeners.forEach((cb) => cb(event))
      return true
    },
  }
  window.matchMedia = vi.fn().mockReturnValue(mql)
  return mql
}

export function resetThemeState() {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
}
