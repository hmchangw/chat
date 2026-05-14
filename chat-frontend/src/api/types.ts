// Shared types for the api/ layer.
//
// These mirror the Go server's pkg/model. Keep field names + casing in
// sync with the JSON tags on those structs — the frontend talks to the
// server in JSON, so what's on the wire IS the contract.
//
// Anything that's specific to a single operation lives in that
// operation's index.ts (e.g. its `Args`/`Response` types). Cross-cutting
// shapes live here.

/** Mirrors model.RoomType. */
export type RoomType = 'channel' | 'dm' | 'botDM' | 'discussion'

/** Mirrors model.HistoryMode. */
export type HistoryMode = 'all' | 'none'

/** Mirrors model.HistoryConfig. */
export interface HistoryConfig {
  mode: HistoryMode
}

/** Mirrors model.ChannelRef — the {roomId, siteId} pair used for cross-
 *  site channel-source references in member.add / room.create payloads. */
export interface ChannelRef {
  roomId: string
  siteId: string
}

/** Mirrors model.User as it ships down from auth-service (after the
 *  NATS handshake). The siteId is added client-side in NatsContext. */
export interface User {
  id: string
  account: string
  engName?: string
  chineseName?: string
  siteId: string
}

/** Mirrors model.Room. Timestamps come down as RFC-3339 strings. */
export interface Room {
  id: string
  name: string
  type: RoomType
  createdBy: string
  siteId: string
  userCount: number
  appCount?: number
  lastMsgAt?: string
  lastMsgId?: string
  createdAt: string
  updatedAt: string
  /** Set client-side on DM rooms so the sidebar has a friendly fallback
   *  while the canonical name lands via subscription.update. */
  subscriptionName?: string
}

/** Mirrors model.Participant — the embedded sender/reader on messages
 *  and read-receipts. */
export interface Participant {
  account: string
  userId?: string
  engName?: string
  chineseName?: string
}

/** Cassandra's QuotedParentMessage shape — what gets embedded on a
 *  reply's `quotedParentMessage` field. Note the legacy `messageId`
 *  and `msg` field names (server-side cassandra schema, distinct from
 *  the model.Message wire shape that uses `id` + `content`). */
export interface QuotedParentMessage {
  messageId: string
  sender?: Participant
  msg?: string
}

/** Mirrors model.Message after the frontend's `normalizeHistoricalMessage`
 *  step. Historic messages come in with `messageId` + `msg`; this is the
 *  unified shape every renderer downstream consumes. */
export interface Message {
  id: string
  content: string
  createdAt: string
  editedAt?: string
  deleted?: boolean
  type?: string
  sysMsgData?: string
  sender?: Participant
  userAccount?: string
  mentions?: Participant[]
  quotedParentMessage?: QuotedParentMessage
  threadParentMessageId?: string
  threadParentMessageCreatedAt?: string
  /** Outgoing local-only flag — set by optimistic appenders. Never
   *  arrives from the server. */
  _local?: boolean
  /** Client-side delivery state. Set to 'failed' when an optimistic
   *  message couldn't be published. */
  _status?: 'failed'
  /** Thread reply count, surfaced as the "💬 N replies" badge. */
  tcount?: number
}

/** Enriched roster entry returned by `member.list { enrich: true }`. */
export interface MemberEntry {
  id: string
  member: {
    type: 'individual' | 'org'
    id: string
    account?: string
    engName?: string
    chineseName?: string
    sectName?: string
    memberCount?: number
    isOwner?: boolean
  }
}

/** Reader entry returned by the read-receipt RPC. */
export interface Reader {
  userId: string
  account: string
  engName?: string
  chineseName?: string
}

/** Subscription returned to the subscribe primitive — the NATS client
 *  hands back an object with `.unsubscribe()`. We narrow to that
 *  contract so callers don't need to import nats.ws's full type. */
export interface Subscription {
  unsubscribe: () => void
}

/** The shape of an inbound NATS callback. We type events as `unknown`
 *  because each subscriber narrows them based on `evt.type`. */
export type SubscriptionCallback = (event: any) => void

/** Two-phase async-job result returned by `requestWithAsyncResult`. */
export interface AsyncJobResult<S = unknown, A = unknown> {
  requestId: string
  sync: S
  async: A
}

/** Options forwarded to `requestWithAsyncResult` from the api layer. */
export interface AsyncJobOptions {
  /** When set, a sync reply matching this predicate is treated as
   *  success (e.g. the DM-exists dedup reply on room.create). */
  treatAsSuccess?: (reply: unknown) => boolean
  /** Override the auto-generated request ID. */
  requestId?: string
  syncTimeout?: number
  asyncTimeout?: number
}

/** The shape of `useNats()`'s return value as consumed by the api layer.
 *
 *  We type only the fields the api functions actually use. The full
 *  NatsContext also exposes `connect`, `disconnect`, `connected`,
 *  `error` — components stay .jsx for now, so those fields don't need
 *  types here. */
export interface Nats {
  user: User
  request: (subject: string, data?: unknown) => Promise<any>
  publish: (subject: string, data?: unknown) => void
  subscribe: (subject: string, cb: SubscriptionCallback) => Subscription
  requestWithAsyncResult: <S = unknown, A = unknown>(
    subject: string,
    data?: unknown,
    opts?: AsyncJobOptions
  ) => Promise<AsyncJobResult<S, A>>
}
