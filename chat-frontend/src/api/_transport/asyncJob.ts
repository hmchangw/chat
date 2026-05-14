// requestWithAsyncResult bridges the room-service two-phase reply contract:
//   1. sync NATS reply  → typically {status:"accepted", …} (or {error})
//   2. async result     → AsyncJobResult on chat.user.{account}.response.{requestID}
//
// The response subscription is opened BEFORE the request is published so a
// fast worker can't beat us to the punch. The X-Request-ID header on the
// request is what tells room-worker which response subject to publish on —
// without it the async result is never emitted.

import { StringCodec, headers as natsHeaders } from 'nats.ws'
import { v7 as uuidv7 } from 'uuid'
import { userResponse } from './subjects'
import type { AsyncJobOptions, AsyncJobResult } from '../types'

const sc = StringCodec()

const DEFAULT_SYNC_TIMEOUT = 5000
const DEFAULT_ASYNC_TIMEOUT = 30000

/** Discriminant on Error.kind so callers can distinguish wire-level
 *  failures from server-reported ones without string-matching. */
export type AsyncJobErrorKind =
  | 'sync-error'
  | 'async-error'
  | 'async-timeout'
  | 'subscription-closed'

export const ASYNC_JOB_ERROR_KINDS: Readonly<Record<string, AsyncJobErrorKind>> =
  Object.freeze({
    SyncError: 'sync-error',
    AsyncError: 'async-error',
    AsyncTimeout: 'async-timeout',
    SubscriptionClosed: 'subscription-closed',
  })

/** Error subtype thrown from this module. Casting from a caught
 *  `unknown` is the typical access pattern. */
export interface AsyncJobError extends Error {
  kind: AsyncJobErrorKind
  cause?: unknown
}

function taggedError(message: string, kind: AsyncJobErrorKind, cause?: unknown): AsyncJobError {
  const err = new Error(message) as AsyncJobError
  err.kind = kind
  if (cause) err.cause = cause
  return err
}

/**
 * User-facing message for an error thrown by `requestWithAsyncResult`.
 *
 * Server-side errors (`SyncError`, `AsyncError`) already carry a sanitised,
 * user-safe message and are returned as-is. Wire-level failures
 * (`AsyncTimeout`, `SubscriptionClosed`) get a friendlier hint that says what
 * happened and what the user can do about it — the raw "async result timeout"
 * isn't actionable.
 */
export function formatAsyncJobError(err: unknown): string {
  if (!err) return ''
  const kind = (err as AsyncJobError).kind
  switch (kind) {
    case ASYNC_JOB_ERROR_KINDS.AsyncTimeout:
      return "The server didn't respond in time. The action may still complete — refresh to check."
    case ASYNC_JOB_ERROR_KINDS.SubscriptionClosed:
      return 'Connection interrupted before the server confirmed. Refresh to check the result.'
    default:
      return (err as Error)?.message ?? ''
  }
}

// Internal envelope passed from the inbox loop to the awaiter. Discriminated
// so the awaiter can pattern-match on `kind` without optional chaining.
type Envelope =
  | { kind: 'data'; data: any }
  | { kind: 'closed' }
  | { kind: 'error'; error: unknown }
  | { kind: 'timeout' }

/**
 * Issues a NATS request whose handler responds in two phases: a sync reply
 * (typically `{status:"accepted"}` or `{error}`) followed by an
 * `AsyncJobResult` published to `chat.user.{account}.response.{requestID}`
 * once the underlying worker finishes.
 *
 * Subscribes to the response subject BEFORE publishing so a fast worker
 * can't beat the client to the punch. Sets the `X-Request-ID` NATS header
 * so the worker knows where to publish the async result.
 *
 * @throws {AsyncJobError} On any failure, with `.kind` set to one of
 *   the `AsyncJobErrorKind` values. Pass to `formatAsyncJobError(err)`
 *   for user-facing text.
 */
export async function requestWithAsyncResult<S = any, A = any>(
  nc: any,
  account: string,
  subject: string,
  payload: unknown,
  opts: AsyncJobOptions = {}
): Promise<AsyncJobResult<S, A | null>> {
  const {
    requestId = uuidv7(),
    syncTimeout = DEFAULT_SYNC_TIMEOUT,
    asyncTimeout = DEFAULT_ASYNC_TIMEOUT,
    treatAsSuccess,
  } = opts

  const sub = nc.subscribe(userResponse(account, requestId), { max: 1 })

  // Register before request resolves so a result that arrives during the
  // sync window is buffered, not dropped. Tagged-envelope resolves never
  // reject so late cleanup signals can't surface as unhandled rejections.
  let resolveAsync!: (env: Envelope) => void
  const asyncPromise = new Promise<Envelope>((res) => { resolveAsync = res })
  ;(async () => {
    try {
      for await (const msg of sub) {
        resolveAsync({ kind: 'data', data: JSON.parse(sc.decode(msg.data)) })
        return
      }
      resolveAsync({ kind: 'closed' })
    } catch (err) {
      resolveAsync({ kind: 'error', error: err })
    }
  })()

  const cleanupSub = () => {
    try { sub.unsubscribe() } catch { /* already closed */ }
  }

  let sync: S
  try {
    const h = natsHeaders()
    h.set('X-Request-ID', requestId)
    const resp = await nc.request(subject, sc.encode(JSON.stringify(payload)), {
      timeout: syncTimeout,
      headers: h,
    })
    sync = JSON.parse(sc.decode(resp.data)) as S
  } catch (err) {
    cleanupSub()
    throw taggedError((err as Error).message, ASYNC_JOB_ERROR_KINDS.SyncError, err)
  }

  if ((sync as any)?.error) {
    // DM-exists and similar "200 with error+roomId" replies are success cases
    // for the caller. The caller opts into this with treatAsSuccess(reply).
    if (treatAsSuccess && treatAsSuccess(sync)) {
      cleanupSub()
      return { requestId, sync, async: null }
    }
    cleanupSub()
    throw taggedError((sync as any).error, ASYNC_JOB_ERROR_KINDS.SyncError)
  }

  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    const timeoutPromise = new Promise<Envelope>((resolve) => {
      timer = setTimeout(() => resolve({ kind: 'timeout' }), asyncTimeout)
    })
    const envelope = await Promise.race([asyncPromise, timeoutPromise])
    if (timer) clearTimeout(timer)
    if (envelope.kind === 'timeout') {
      throw taggedError('async result timeout', ASYNC_JOB_ERROR_KINDS.AsyncTimeout)
    }
    if (envelope.kind === 'error') {
      throw taggedError(
        (envelope.error as Error)?.message ?? 'subscription error',
        ASYNC_JOB_ERROR_KINDS.SubscriptionClosed,
        envelope.error
      )
    }
    if (envelope.kind === 'closed') {
      throw taggedError(
        'subscription closed before result arrived',
        ASYNC_JOB_ERROR_KINDS.SubscriptionClosed
      )
    }
    const asyncResult = envelope.data as A & { status?: string; error?: string }
    if (asyncResult.status === 'error') {
      throw taggedError(asyncResult.error || 'operation failed', ASYNC_JOB_ERROR_KINDS.AsyncError)
    }
    cleanupSub()
    return { requestId, sync, async: asyncResult }
  } catch (err) {
    if (timer) clearTimeout(timer)
    cleanupSub()
    throw err
  }
}
