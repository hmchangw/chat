import { msgThread } from '../_transport/subjects'
import { normalizeHistoricalMessages } from '../_transport/normalizeMessage'
import type { Nats, Message, HistoryMessage } from '../types'

export interface FetchThreadMessagesArgs {
  roomId: string
  siteId: string
  /** Parent message id — the root of the reply chain. */
  threadMessageId: string
  limit?: number
}

export interface FetchThreadMessagesResponse {
  /** Normalised broadcast shape. */
  messages: Message[]
  nextCursor?: string
  hasNext: boolean
}

interface WireResponse {
  messages?: HistoryMessage[]
  nextCursor?: string
  hasNext?: boolean
}

/**
 * Load the reply chain for a thread (parent + replies).
 * Normalises cassandra shape into broadcast shape.
 */
export async function fetchThreadMessages(
  { user, request }: Nats,
  args: FetchThreadMessagesArgs,
): Promise<FetchThreadMessagesResponse> {
  const { roomId, siteId, threadMessageId, limit = 50 } = args
  const subject = msgThread(user.account, roomId, siteId)
  const payload = { threadMessageId, limit }
  // TEMP DEBUG: log the exact wire-level args + response so we can see
  // whether close+reopen sends identical args to refresh+open and what
  // the backend actually returns each time. Remove once the empty-on-
  // reopen bug is root-caused.
  // eslint-disable-next-line no-console
  console.log('[thread-debug] fetchThreadMessages → request', { subject, payload })
  const resp = await request<WireResponse>(subject, payload)
  // eslint-disable-next-line no-console
  console.log('[thread-debug] fetchThreadMessages ← response', {
    subject,
    rawMessageCount: resp?.messages?.length ?? 0,
    hasNext: resp?.hasNext ?? false,
    nextCursor: resp?.nextCursor ?? null,
    firstRawIds: (resp?.messages ?? []).slice(0, 3).map(
      (m: HistoryMessage) => m?.messageId ?? (m as unknown as Message)?.id
    ),
  })
  return {
    messages: normalizeHistoricalMessages(resp.messages),
    nextCursor: resp.nextCursor,
    hasNext: resp.hasNext ?? false,
  }
}
