import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import InRoomSearch from './InRoomSearch'

vi.mock('../context/NatsContext', () => ({
  useNats: vi.fn(),
}))

import { useNats } from '../context/NatsContext'

describe('InRoomSearch', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('fetches message results after 300ms debounce, scoped to roomIds: [roomId]', async () => {
    const request = vi.fn().mockResolvedValue({ results: [], total: 0 })
    useNats.mockReturnValue({
      user: { account: 'alice' },
      request,
    })

    render(
      <InRoomSearch
        roomId="r1"
        onClose={vi.fn()}
        onJumpToMessage={vi.fn()}
      />
    )

    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'hi' } })

    expect(request).not.toHaveBeenCalled()

    vi.advanceTimersByTime(300)

    await waitFor(() => {
      expect(request).toHaveBeenCalledWith(
        'chat.user.alice.request.search.messages',
        { searchText: 'hi', roomIds: ['r1'], size: 20 }
      )
    })
  })

  it('clicking a result calls onJumpToMessage with the message id', async () => {
    const onJumpToMessage = vi.fn()
    const onClose = vi.fn()
    const request = vi.fn().mockResolvedValue({
      results: [
        { messageId: 'msg-1', roomId: 'r1', content: 'hello world' },
      ],
      total: 1,
    })
    useNats.mockReturnValue({
      user: { account: 'alice' },
      request,
    })

    render(
      <InRoomSearch
        roomId="r1"
        onClose={onClose}
        onJumpToMessage={onJumpToMessage}
      />
    )

    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'hello' } })
    vi.advanceTimersByTime(300)

    await waitFor(() => {
      expect(screen.getByText('hello world')).toBeInTheDocument()
    })

    fireEvent.click(screen.getByText('hello world'))

    expect(onJumpToMessage).toHaveBeenCalledWith('msg-1')
    expect(onClose).toHaveBeenCalled()
  })

  it('X button calls onClose', () => {
    const onClose = vi.fn()
    useNats.mockReturnValue({
      user: { account: 'alice' },
      request: vi.fn().mockResolvedValue({ results: [], total: 0 }),
    })

    render(
      <InRoomSearch
        roomId="r1"
        onClose={onClose}
        onJumpToMessage={vi.fn()}
      />
    )

    fireEvent.click(screen.getByLabelText(/close/i))
    expect(onClose).toHaveBeenCalled()
  })
})
