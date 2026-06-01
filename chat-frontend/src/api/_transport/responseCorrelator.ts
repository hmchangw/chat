// responseCorrelator routes async replies that land on the per-account response
// wildcard (chat.user.{account}.response.>) back to the caller that is waiting
// for a specific requestId.
//
// The message-gatekeeper replies to a msg.send on
// chat.user.{account}.response.{requestID} with the persisted Message (or an
// {error} envelope). Neither body carries the requestId — the ONLY correlator
// is the {requestID} token in the subject. So the wildcard subscription parses
// it from the subject (parseResponseRequestId) and hands (requestId, payload)
// to deliver(), which resolves whichever waiter registered for that requestId.

const RESPONSE_SEGMENT = '.response.'

/** Tag set on the Error a waiter rejects with when no reply arrives in time. */
export const RESPONSE_TIMEOUT_KIND = 'response-timeout'

const DEFAULT_RESPONSE_TIMEOUT = 30000

interface PendingWaiter {
  resolve: (payload: unknown) => void
  reject: (err: unknown) => void
  timer: ReturnType<typeof setTimeout> | undefined
}

export interface ResponseCorrelator {
  /** Register interest in the reply for `requestId`. Resolves with the reply
   *  payload once delivered, or rejects with a RESPONSE_TIMEOUT_KIND error
   *  after `timeout` ms. Call this BEFORE publishing the request. */
  waitFor: (requestId: string, opts?: { timeout?: number }) => Promise<unknown>
  /** Hand a parsed reply to its waiter. Returns true if a waiter matched,
   *  false when no one is (or is still) waiting on `requestId`. */
  deliver: (requestId: string, payload: unknown) => boolean
  /** Reject every pending waiter (e.g. on disconnect) and empty the registry. */
  rejectAll: (err: unknown) => void
}

/**
 * Extract the `{requestID}` token from a response subject of the form
 * `chat.user.{account}.response.{requestID}`. Returns null when the subject is
 * missing or has no `.response.` segment.
 */
export function parseResponseRequestId(subject: string | undefined | null): string | null {
  if (!subject) return null
  const idx = subject.indexOf(RESPONSE_SEGMENT)
  if (idx === -1) return null
  const tail = subject.slice(idx + RESPONSE_SEGMENT.length)
  return tail.length > 0 ? tail : null
}

/** Build a fresh, empty correlator. One per NATS connection / session. */
export function createResponseCorrelator(): ResponseCorrelator {
  const pending = new Map<string, PendingWaiter>()

  function settle(requestId: string): PendingWaiter | undefined {
    const waiter = pending.get(requestId)
    if (!waiter) return undefined
    pending.delete(requestId)
    if (waiter.timer) clearTimeout(waiter.timer)
    return waiter
  }

  return {
    waitFor(requestId, { timeout = DEFAULT_RESPONSE_TIMEOUT } = {}) {
      return new Promise<unknown>((resolve, reject) => {
        const timer = setTimeout(() => {
          if (settle(requestId)) {
            const err = new Error(`response timeout for requestId ${requestId}`) as Error & {
              kind: string
            }
            err.kind = RESPONSE_TIMEOUT_KIND
            reject(err)
          }
        }, timeout)
        pending.set(requestId, { resolve, reject, timer })
      })
    },

    deliver(requestId, payload) {
      const waiter = settle(requestId)
      if (!waiter) return false
      waiter.resolve(payload)
      return true
    },

    rejectAll(err) {
      for (const [, waiter] of pending) {
        if (waiter.timer) clearTimeout(waiter.timer)
        waiter.reject(err)
      }
      pending.clear()
    },
  }
}
