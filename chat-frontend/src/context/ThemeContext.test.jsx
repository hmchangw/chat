import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import { ThemeProvider, useTheme } from './ThemeContext'

function Probe() {
  const { theme, source } = useTheme()
  return (
    <div>
      <span data-testid="theme">{theme}</span>
      <span data-testid="source">{source}</span>
    </div>
  )
}

function setMatchMedia(matches) {
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

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
})

afterEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  vi.unstubAllGlobals()
})

describe('ThemeProvider initial state', () => {
  it('uses stored "dark" from localStorage and marks source=user', () => {
    localStorage.setItem('theme', 'dark')
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('dark')
    expect(screen.getByTestId('source').textContent).toBe('user')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('uses stored "light" from localStorage and marks source=user', () => {
    localStorage.setItem('theme', 'light')
    setMatchMedia(true)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')
    expect(screen.getByTestId('source').textContent).toBe('user')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('falls back to matchMedia when localStorage is empty', () => {
    setMatchMedia(true)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('dark')
    expect(screen.getByTestId('source').textContent).toBe('system')
  })

  it('treats invalid localStorage value as absent', () => {
    localStorage.setItem('theme', 'turquoise')
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')
    expect(screen.getByTestId('source').textContent).toBe('system')
  })

  it('defaults to light when matchMedia is undefined', () => {
    delete window.matchMedia
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')
    expect(screen.getByTestId('source').textContent).toBe('system')
  })
})
