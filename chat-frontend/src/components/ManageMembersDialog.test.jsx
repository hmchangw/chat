import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ManageMembersDialog from './ManageMembersDialog'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

const room = { id: 'r1', siteId: 'site-A', name: 'general', type: 'group' }

beforeEach(() => {
  useNats.mockReset()
  useNats.mockReturnValue({
    user: { account: 'alice' },
    request: vi.fn().mockResolvedValue({ status: 'accepted' }),
  })
})

describe('ManageMembersDialog', () => {
  it('shows the Add tab by default', () => {
    render(<ManageMembersDialog room={room} onClose={vi.fn()} />)
    expect(screen.getByRole('tab', { name: /^Add$/ })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByLabelText(/Accounts \(comma-separated\)/i)).toBeInTheDocument()
  })

  it('switches tabs when a tab button is clicked', () => {
    render(<ManageMembersDialog room={room} onClose={vi.fn()} />)

    fireEvent.click(screen.getByRole('tab', { name: /^Remove$/ }))
    expect(screen.getByRole('tab', { name: /^Remove$/ })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByLabelText(/^Account$/i)).toBeInTheDocument()
    expect(screen.queryByLabelText(/Accounts \(comma-separated\)/i)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: /^Role$/ }))
    expect(screen.getByLabelText(/^Role$/i)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: /Remove Org/i }))
    expect(screen.getByLabelText(/Org ID/i)).toBeInTheDocument()
  })

  it('calls onClose when Close is clicked', () => {
    const onClose = vi.fn()
    render(<ManageMembersDialog room={room} onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: /Close/i }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when the overlay is clicked', () => {
    const onClose = vi.fn()
    const { container } = render(<ManageMembersDialog room={room} onClose={onClose} />)
    fireEvent.click(container.querySelector('.dialog-overlay'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('does not call onClose when the dialog body is clicked', () => {
    const onClose = vi.fn()
    const { container } = render(<ManageMembersDialog room={room} onClose={onClose} />)
    fireEvent.click(container.querySelector('.dialog'))
    expect(onClose).not.toHaveBeenCalled()
  })
})
