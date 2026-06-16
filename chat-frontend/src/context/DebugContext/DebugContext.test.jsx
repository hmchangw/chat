import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import { DebugProvider, useDebug, DEBUG_LEVELS } from './DebugContext'

function Probe() {
  const { level, setLevel } = useDebug()
  return (
    <>
      <span data-testid="level">{level}</span>
      <button data-testid="set-trace" onClick={() => setLevel('trace')}>trace</button>
      <button data-testid="set-flow" onClick={() => setLevel('flow')}>flow</button>
      <button data-testid="set-off" onClick={() => setLevel('off')}>off</button>
      <button data-testid="set-bogus" onClick={() => setLevel('bogus')}>bogus</button>
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

describe('DEBUG_LEVELS', () => {
  it('lists off/flow/debug/trace in increasing verbosity', () => {
    expect(DEBUG_LEVELS).toEqual(['off', 'flow', 'debug', 'trace'])
  })
})

describe('DebugProvider initial state', () => {
  it('defaults to "off" when localStorage is empty', () => {
    render(<DebugProvider><Probe /></DebugProvider>)
    expect(screen.getByTestId('level').textContent).toBe('off')
  })

  it('reads a stored level verbatim', () => {
    localStorage.setItem('debug', 'trace')
    render(<DebugProvider><Probe /></DebugProvider>)
    expect(screen.getByTestId('level').textContent).toBe('trace')
  })

  it('maps the legacy "1" value to "debug"', () => {
    localStorage.setItem('debug', '1')
    render(<DebugProvider><Probe /></DebugProvider>)
    expect(screen.getByTestId('level').textContent).toBe('debug')
  })

  it('treats an unknown stored value as "off"', () => {
    localStorage.setItem('debug', 'verbose')
    render(<DebugProvider><Probe /></DebugProvider>)
    expect(screen.getByTestId('level').textContent).toBe('off')
  })

  it('does not crash when localStorage.getItem throws on initial read', () => {
    const original = Storage.prototype.getItem
    Storage.prototype.getItem = vi.fn(() => {
      throw new Error('localStorage unavailable')
    })
    try {
      render(<DebugProvider><Probe /></DebugProvider>)
      expect(screen.getByTestId('level').textContent).toBe('off')
    } finally {
      Storage.prototype.getItem = original
    }
  })
})

describe('DebugProvider mutations', () => {
  it('setLevel persists a non-off level', () => {
    render(<DebugProvider><Probe /></DebugProvider>)
    act(() => { screen.getByTestId('set-trace').click() })
    expect(screen.getByTestId('level').textContent).toBe('trace')
    expect(localStorage.getItem('debug')).toBe('trace')

    act(() => { screen.getByTestId('set-flow').click() })
    expect(screen.getByTestId('level').textContent).toBe('flow')
    expect(localStorage.getItem('debug')).toBe('flow')
  })

  it('setLevel("off") clears the stored key', () => {
    localStorage.setItem('debug', 'trace')
    render(<DebugProvider><Probe /></DebugProvider>)
    act(() => { screen.getByTestId('set-off').click() })
    expect(screen.getByTestId('level').textContent).toBe('off')
    expect(localStorage.getItem('debug')).toBeNull()
  })

  it('normalizes an invalid setLevel argument to "off"', () => {
    localStorage.setItem('debug', 'trace')
    render(<DebugProvider><Probe /></DebugProvider>)
    act(() => { screen.getByTestId('set-bogus').click() })
    expect(screen.getByTestId('level').textContent).toBe('off')
    expect(localStorage.getItem('debug')).toBeNull()
  })

  it('does not crash when localStorage.setItem throws', () => {
    const original = Storage.prototype.setItem
    Storage.prototype.setItem = vi.fn(() => {
      throw new Error('quota exceeded')
    })
    try {
      render(<DebugProvider><Probe /></DebugProvider>)
      act(() => { screen.getByTestId('set-trace').click() })
      expect(screen.getByTestId('level').textContent).toBe('trace')
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
