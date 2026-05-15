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

/** Mirrors model.Role. */
export type Role = 'owner' | 'admin' | 'member'

/** HRInfo carries the two name fields used to render a DM-room label.
 *  Backend (`pkg/model.Subscription.HRInfo *HRInfo \`json:"hrInfo,omitempty"\``)
 *  populates this struct ONLY on DM-type subscriptions; when the pointer
 *  is present both inner fields are populated. */
export interface HRInfo {
  engName: string
  name: string
}

/** Mirrors model.Subscription — the per-user record linking a user to a
 *  room. Carries the room's roles for THIS user, the user's preferred
 *  name, mute/alert state, mention/thread-unread bookkeeping, and the
 *  optional HRInfo for DM-display. */
export interface Subscription {
  id: string
  u: { id: string; account: string }
  roomId: string
  siteId: string
  roles: Role[]
  name: string
  roomType: RoomType
  isSubscribed?: boolean
  historySharedSince?: string
  joinedAt: string
  lastSeenAt?: string
  hasMention: boolean
  threadUnread?: string[]
  alert: boolean
  /** Only present on DM subscriptions. */
  hrInfo?: HRInfo
}

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
 *  NATS handshake). siteId is added client-side in NatsContext. The
 *  other sect/employee fields arrive from the auth payload but might
 *  be empty depending on the user's directory entry. */
export interface User {
  id: string
  account: string
  siteId: string
  sectId?: string
  sectName?: string
  engName?: string
  chineseName?: string
  employeeId?: string
}

/** Mirrors model.Room. Timestamps come down as RFC-3339 strings.
 *  `lastMsgAt`, `lastMentionAllAt`, `minUserLastSeenAt` are `omitempty`
 *  on the wire — typed optional. The rest are always present. */
export interface Room {
  id: string
  name: string
  type: RoomType
  createdBy: string
  siteId: string
  userCount: number
  appCount: number
  lastMsgId: string
  lastMsgAt?: string
  lastMentionAllAt?: string
  minUserLastSeenAt?: string
  createdAt: string
  updatedAt: string
  restricted?: boolean
  /** Set client-side on DM rooms so the sidebar has a friendly fallback
   *  while the canonical name lands via subscription.update. */
  subscriptionName?: string
}

/** Mirrors model.Participant — the embedded sender/reader on messages
 *  and read-receipts. `siteId` rides along on enriched senders. */
export interface Participant {
  account: string
  userId?: string
  engName?: string
  chineseName?: string
  siteId?: string
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

/**
 * Cassandra-shape message as it arrives from history-service.
 * Distinct from the broadcast `Message` shape: history rows carry
 * `messageId` + `msg`, broadcasts carry `id` + `content`. The api/
 * layer normalises history results into `Message` before handing them
 * to callers — this type is mostly internal to the normalisation step.
 */
export interface HistoryMessage {
  messageId: string
  roomId: string
  sender?: Participant
  createdAt: string
  msg: string
  editedAt?: string
  deleted?: boolean
  type?: string
  sysMsgData?: string
  mentions?: Participant[]
  quotedParentMessage?: QuotedParentMessage
  threadParentId?: string
  threadParentCreatedAt?: string
  tcount?: number
}

/**
 * Normalised message shape consumed by every renderer (MessageRow,
 * QuotedBlock, SystemMessage). Broadcast events arrive in this shape;
 * historic rows are mapped to it by `normalizeHistoricalMessages` in
 * the api layer.
 */
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

/** Mirrors model.RoomMember. `id` is the Mongo doc id, `rid` is the
 *  containing room, `ts` is the join timestamp. `member` carries the
 *  type-tagged details (individual vs org). */
export interface MemberEntry {
  id: string
  rid: string
  ts: string
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

/** Inbound NATS event payload. Subscribers narrow on `evt.type` to
 *  reach the variant fields. Typed `unknown` so call sites can't
 *  bypass the discriminator. */
export type SubscriptionCallback = (event: unknown) => void

/** Two-phase async-job result returned by `requestWithAsyncResult`. */
export interface AsyncJobResult<S = unknown, A = unknown> {
  requestId: string
  sync: S
  async: A | null
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

/**
 * The shape of `useNats()`'s return value as consumed by the api layer.
 *
 * `request<T>` is generic so each api op declares its response shape
 * once and gets type-checked at the call site — `Promise<any>` would
 * silently swallow shape mismatches.
 *
 * Only the fields the api layer actually uses are typed; NatsContext
 * also exposes `connect`/`disconnect`/`connected`/`error`, but those
 * are component-facing and JSX consumers don't need a TS type yet.
 */
export interface Nats {
  user: User
  request: <T = unknown>(subject: string, data?: unknown) => Promise<T>
  publish: (subject: string, data?: unknown) => void
  subscribe: (subject: string, cb: SubscriptionCallback) => Subscription
  requestWithAsyncResult: <S = unknown, A = unknown>(
    subject: string,
    data?: unknown,
    opts?: AsyncJobOptions,
  ) => Promise<AsyncJobResult<S, A>>
}
