import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import DeleteConfirmDialog from './DeleteConfirmDialog'

describe('DeleteConfirmDialog', () => {
  it('renders the confirm prompt', () => {
    render(<DeleteConfirmDialog onConfirm={() => {}} onCancel={() => {}} />)
    expect(screen.getByText(/cannot be undone/i)).toBeInTheDocument()
  })

  it('Cancel button calls onCancel', () => {
    const onCancel = vi.fn()
    render(<DeleteConfirmDialog onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onCancel).toHaveBeenCalled()
  })

  it('Delete button calls onConfirm', () => {
    const onConfirm = vi.fn()
    render(<DeleteConfirmDialog onConfirm={onConfirm} onCancel={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /^delete$/i }))
    expect(onConfirm).toHaveBeenCalled()
  })

  it('Esc dismisses (calls onCancel)', () => {
    const onCancel = vi.fn()
    render(<DeleteConfirmDialog onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalled()
  })
})
