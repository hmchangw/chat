import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import { useUnreadCount } from './useUnreadCount'
import { getUnreadCount } from '@/api'

vi.mock('@/api', () => ({ getUnreadCount: vi.fn() }))

const nats = { user: { account: 'alice', siteId: 'site-A' } }

describe('useUnreadCount', () => {
  beforeEach(() => vi.clearAllMocks())

  it('starts at 0 and resolves to the fetched count on mount', async () => {
    getUnreadCount.mockResolvedValue({ count: 42 })
    const { result } = renderHook(() => useUnreadCount(nats, 0, 0))
    expect(result.current).toBe(0)
    await waitFor(() => expect(result.current).toBe(42))
    expect(getUnreadCount).toHaveBeenCalledWith(nats)
  })

  it('refetches immediately when readSeq changes (post mark-read)', async () => {
    getUnreadCount.mockResolvedValue({ count: 1 })
    const { rerender } = renderHook(
      ({ r }) => useUnreadCount(nats, r, 0),
      { initialProps: { r: 0 } },
    )
    await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(1))
    rerender({ r: 1 })
    await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(2))
  })

  it('does not refetch when readSeq is unchanged', async () => {
    getUnreadCount.mockResolvedValue({ count: 1 })
    const { rerender } = renderHook(
      ({ r }) => useUnreadCount(nats, r, 0),
      { initialProps: { r: 3 } },
    )
    await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(1))
    rerender({ r: 3 })
    await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(1))
  })

  it('drops a stale in-flight result when readSeq advanced before it resolved', async () => {
    let resolveFirst
    getUnreadCount
      .mockImplementationOnce(() => new Promise((res) => { resolveFirst = res }))
      .mockResolvedValueOnce({ count: 7 })

    const { result, rerender } = renderHook(
      ({ r }) => useUnreadCount(nats, r, 0),
      { initialProps: { r: 0 } },
    )
    rerender({ r: 1 })
    await waitFor(() => expect(result.current).toBe(7))

    await act(async () => { resolveFirst({ count: 999 }) })
    expect(result.current).toBe(7)
  })

  describe('message-driven refetch (msgRecvSeq)', () => {
    beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }))
    afterEach(() => vi.useRealTimers())

    it('refetches 500ms after msgRecvSeq changes', async () => {
      getUnreadCount.mockResolvedValue({ count: 1 })
      const { rerender } = renderHook(
        ({ s }) => useUnreadCount(nats, 0, s),
        { initialProps: { s: 0 } },
      )
      await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(1))

      rerender({ s: 1 })
      await act(async () => { await vi.advanceTimersByTimeAsync(300) })
      expect(getUnreadCount).toHaveBeenCalledTimes(1)

      await act(async () => { await vi.advanceTimersByTimeAsync(200) })
      expect(getUnreadCount).toHaveBeenCalledTimes(2)
    })

    it('collapses a burst of msgRecvSeq bumps into one refetch', async () => {
      getUnreadCount.mockResolvedValue({ count: 1 })
      const { rerender } = renderHook(
        ({ s }) => useUnreadCount(nats, 0, s),
        { initialProps: { s: 0 } },
      )
      await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(1))

      rerender({ s: 1 })
      await act(async () => { await vi.advanceTimersByTimeAsync(100) })
      rerender({ s: 2 })
      await act(async () => { await vi.advanceTimersByTimeAsync(100) })
      rerender({ s: 3 })
      await act(async () => { await vi.advanceTimersByTimeAsync(500) })

      expect(getUnreadCount).toHaveBeenCalledTimes(2)
    })

    it('does not schedule a refetch when msgRecvSeq stays 0', async () => {
      getUnreadCount.mockResolvedValue({ count: 1 })
      renderHook(() => useUnreadCount(nats, 0, 0))
      await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(1))
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
      expect(getUnreadCount).toHaveBeenCalledTimes(1)
    })

    it('does not re-arm the debounce on reconnect when msgRecvSeq is unchanged', async () => {
      getUnreadCount.mockResolvedValue({ count: 1 })
      const nats1 = { user: { account: 'alice', siteId: 'site-A' } }
      const nats2 = { user: { account: 'alice', siteId: 'site-A' } }
      const { rerender } = renderHook(
        ({ n, s }) => useUnreadCount(n, 0, s),
        { initialProps: { n: nats1, s: 0 } },
      )
      await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(1))

      // A message → one debounced refetch.
      rerender({ n: nats1, s: 1 })
      await act(async () => { await vi.advanceTimersByTimeAsync(500) })
      expect(getUnreadCount).toHaveBeenCalledTimes(2)

      // Reconnect: nats identity flips, msgRecvSeq unchanged. The
      // immediate (nats/readSeq) effect refetches once — but the
      // debounced effect must NOT re-arm off the recreated fetchNow.
      rerender({ n: nats2, s: 1 })
      await waitFor(() => expect(getUnreadCount).toHaveBeenCalledTimes(3))
      await act(async () => { await vi.advanceTimersByTimeAsync(500) })
      expect(getUnreadCount).toHaveBeenCalledTimes(3)
    })
  })
})
