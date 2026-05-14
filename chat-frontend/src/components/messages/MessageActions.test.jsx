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

describe('MessageActions — Edit / Delete visibility', () => {
  it('renders Edit and Delete on own messages', () => {
    render(
      <MessageActions
        message={msg}
        context="main"
        isOwn
        onThread={() => {}} onReply={() => {}}
        onEdit={() => {}} onDelete={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: /edit message/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /delete message/i })).toBeInTheDocument()
  })

  it('omits Edit and Delete on other users\' messages', () => {
    render(
      <MessageActions
        message={msg}
        context="main"
        isOwn={false}
        onThread={() => {}} onReply={() => {}}
        onEdit={() => {}} onDelete={() => {}}
      />
    )
    expect(screen.queryByRole('button', { name: /edit message/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /delete message/i })).not.toBeInTheDocument()
  })

  it('clicking Edit / Delete invokes the handlers with the message', () => {
    const onEdit = vi.fn()
    const onDelete = vi.fn()
    render(
      <MessageActions
        message={msg}
        context="main"
        isOwn
        onThread={() => {}} onReply={() => {}}
        onEdit={onEdit} onDelete={onDelete}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /edit message/i }))
    expect(onEdit).toHaveBeenCalledWith(msg)
    fireEvent.click(screen.getByRole('button', { name: /delete message/i }))
    expect(onDelete).toHaveBeenCalledWith(msg)
  })
})
