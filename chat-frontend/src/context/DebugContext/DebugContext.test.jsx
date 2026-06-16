import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import { DebugProvider, useDebug } from './DebugContext'

function ToggleProbe() {
  const { debug, toggleDebug, setDebug } = useDebug()
  return (
    <>
      <span data-testid="debug">{String(debug)}</span>
      <button data-testid="toggle" onClick={toggleDebug}>toggle</button>
      <button data-testid="set-on" onClick={() => setDebug(true)}>on</button>
      <button data-testid="set-off" onClick={() => setDebug(false)}>off</button>
    </>
  )
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('DebugProvider initial state', () => {
  it('defaults to false when localStorage is empty', () => {
    render(
      <DebugProvider>
        <ToggleProbe />
      </DebugProvider>,
    )
    expect(screen.getByTestId('debug').textContent).toBe('false')
  })

  it('reads a stored "1" as enabled', () => {
    localStorage.setItem('debug', '1')
    render(
      <DebugProvider>
        <ToggleProbe />
      </DebugProvider>,
    )
    expect(screen.getByTestId('debug').textContent).toBe('true')
  })

  it('treats any non-"1" stored value as disabled', () => {
    localStorage.setItem('debug', 'true')
    render(
      <DebugProvider>
        <ToggleProbe />
      </DebugProvider>,
    )
    expect(screen.getByTestId('debug').textContent).toBe('false')
  })

  it('does not crash when localStorage.getItem throws on initial read', () => {
    const original = Storage.prototype.getItem
    Storage.prototype.getItem = vi.fn(() => {
      throw new Error('localStorage unavailable')
    })
    try {
      render(
        <DebugProvider>
          <ToggleProbe />
        </DebugProvider>,
      )
      expect(screen.getByTestId('debug').textContent).toBe('false')
    } finally {
      Storage.prototype.getItem = original
    }
  })
})

describe('DebugProvider mutations', () => {
  it('toggleDebug flips the flag and persists "1" / clears the key', () => {
    render(
      <DebugProvider>
        <ToggleProbe />
      </DebugProvider>,
    )
    expect(screen.getByTestId('debug').textContent).toBe('false')

    act(() => { screen.getByTestId('toggle').click() })
    expect(screen.getByTestId('debug').textContent).toBe('true')
    expect(localStorage.getItem('debug')).toBe('1')

    act(() => { screen.getByTestId('toggle').click() })
    expect(screen.getByTestId('debug').textContent).toBe('false')
    expect(localStorage.getItem('debug')).toBeNull()
  })

  it('setDebug(true) enables and persists, setDebug(false) disables and clears', () => {
    render(
      <DebugProvider>
        <ToggleProbe />
      </DebugProvider>,
    )
    act(() => { screen.getByTestId('set-on').click() })
    expect(screen.getByTestId('debug').textContent).toBe('true')
    expect(localStorage.getItem('debug')).toBe('1')

    act(() => { screen.getByTestId('set-off').click() })
    expect(screen.getByTestId('debug').textContent).toBe('false')
    expect(localStorage.getItem('debug')).toBeNull()
  })

  it('does not crash when localStorage.setItem throws', () => {
    const original = Storage.prototype.setItem
    Storage.prototype.setItem = vi.fn(() => {
      throw new Error('quota exceeded')
    })
    try {
      render(
        <DebugProvider>
          <ToggleProbe />
        </DebugProvider>,
      )
      act(() => { screen.getByTestId('set-on').click() })
      expect(screen.getByTestId('debug').textContent).toBe('true')
    } finally {
      Storage.prototype.setItem = original
    }
  })
})

describe('useDebug guard', () => {
  it('throws when used outside a DebugProvider', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    function Bare() {
      useDebug()
      return null
    }
    expect(() => render(<Bare />)).toThrow(/useDebug must be used within a DebugProvider/)
    spy.mockRestore()
  })
})
