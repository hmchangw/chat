import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import { DebugProvider } from '@/context/DebugContext'
import DebugToggle from './DebugToggle'

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  localStorage.clear()
})

describe('DebugToggle', () => {
  it('renders the "enable" label and aria-pressed=false when debug is off', () => {
    render(
      <DebugProvider>
        <DebugToggle />
      </DebugProvider>,
    )
    const btn = screen.getByRole('button', { name: /enable x-debug header/i })
    expect(btn).toBeInTheDocument()
    expect(btn).toHaveAttribute('aria-pressed', 'false')
    expect(btn.querySelector('[data-icon="bug"]')).not.toBeNull()
  })

  it('renders the "disable" label and aria-pressed=true when debug is on', () => {
    localStorage.setItem('debug', '1')
    render(
      <DebugProvider>
        <DebugToggle />
      </DebugProvider>,
    )
    const btn = screen.getByRole('button', { name: /disable x-debug header/i })
    expect(btn).toBeInTheDocument()
    expect(btn).toHaveAttribute('aria-pressed', 'true')
  })

  it('clicking flips the flag and persists it', () => {
    render(
      <DebugProvider>
        <DebugToggle />
      </DebugProvider>,
    )
    expect(localStorage.getItem('debug')).toBeNull()

    act(() => {
      screen.getByRole('button', { name: /enable x-debug header/i }).click()
    })

    expect(localStorage.getItem('debug')).toBe('1')
    expect(
      screen.getByRole('button', { name: /disable x-debug header/i }),
    ).toHaveAttribute('aria-pressed', 'true')
  })
})
