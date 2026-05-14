import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import MessageActions from './MessageActions'

const msg = { id: 'm1', userAccount: 'alice' }

describe('MessageActions', () => {
  it('renders Thread and Reply buttons in the main feed context', () => {
    render(<MessageActions message={msg} context="main" onThread={() => {}} onReply={() => {}} />)
    expect(screen.getByRole('button', { name: /reply in thread/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /quote/i })).toBeInTheDocument()
  })

  it('omits Thread and Reply when context is thread-parent', () => {
    render(<MessageActions message={msg} context="thread-parent" onThread={() => {}} onReply={() => {}} />)
    expect(screen.queryByRole('button', { name: /reply in thread/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /quote/i })).not.toBeInTheDocument()
  })

  it('clicking Thread invokes onThread with the message', () => {
    const onThread = vi.fn()
    render(<MessageActions message={msg} context="main" onThread={onThread} onReply={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /reply in thread/i }))
    expect(onThread).toHaveBeenCalledWith(msg)
  })

  it('clicking Reply invokes onReply with the message', () => {
    const onReply = vi.fn()
    render(<MessageActions message={msg} context="main" onThread={() => {}} onReply={onReply} />)
    fireEvent.click(screen.getByRole('button', { name: /quote/i }))
    expect(onReply).toHaveBeenCalledWith(msg)
  })
})
