import {
  userSubscriptionGetCurrent,
  userSubscriptionGetApps,
} from '../_transport/subjects'
import type { Nats, DMSubscription } from '../types'

/** Wire shape of every `subscription.get*` reply.
 *  Both fields are non-omitempty on the Go side
 *  (`mock-user-service/handler.go::subscriptionListResp`) — `Subscriptions`
 *  is always a slice (possibly empty), and `Total` is always an int.
 *
 *  Each entry is typed `DMSubscription` (= Subscription ∪ { hrInfo? }) to
 *  match Go's flattened JSON for both subscription kinds: channels/groups
 *  ship plain Subscription (hrInfo absent ⇒ typed `undefined`), DM rooms
 *  ship DMSubscription (hrInfo present). One type covers both since
 *  DMSubscription extends Subscription. */
interface SidebarBucketReply {
  subscriptions: DMSubscription[]
  total: number
}

export interface SidebarBuckets {
  favoriteIds: string[]
  appIds: string[]
  channelDmIds: string[]
  /** Per-roomId map of the full subscription record (DM variant typing
   *  covers both kinds — see SidebarBucketReply above). The reducer
   *  stores this directly under `state.subscriptions` so components
   *  consume the live per-room state via `useSubscription(roomId)`. */
  subscriptions: Record<string, DMSubscription>
}

/**
 * Bootstrap the sidebar by fetching three lists from user-service in
 * parallel:
 *   1. `getCurrent()` — canonical full subscription list (every roomType).
 *      Becomes `state.subscriptions` (source of truth for
 *      `useSubscription`) and seeds `channelDmIds`. The Channels and DMs
 *      section is partitioned by roomType at render time from this list.
 *   2. `getCurrent({ favorite: true })` — favorited room IDs for the
 *      Favorite section.
 *   3. `getApps()` — app subscription IDs for the Apps section.
 *
 * The reducer's `BUCKETS_LOADED` action consumes this shape directly.
 * Partition exclusivity (favorite > apps > channelDm) is enforced at
 * render time by `useSidebarSections`, so a room ID can appear in
 * `channelDmIds` and one of the other Sets without double-render.
 */
export async function fetchSidebarBuckets({ user, request }: Nats): Promise<SidebarBuckets> {
  const currentSubject = userSubscriptionGetCurrent(user.account, user.siteId)
  const appsSubject = userSubscriptionGetApps(user.account, user.siteId)
  // Favorite RPC is commented out for now — `pkg/model.Subscription` has no
  // `Favorite` field, so the user-service `{favorite:true}` filter is
  // unenforceable end-to-end. Keep the call commented (not deleted) so the
  // path is easy to re-enable once the backend supports favorites. The
  // sidebar's Favorite section renders a "Favorites are not yet supported"
  // note in the meantime — see `useSidebarSections`.
  // const favSubject = userSubscriptionGetCurrent(user.account, user.siteId)
  // const favPromise = request<SidebarBucketReply>(favSubject, { favorite: true })

  // Promise.allSettled so a single RPC failure degrades to an empty bucket
  // instead of black-holing the entire sidebar bootstrap (which would have
  // happened with Promise.all). Each failure is logged with its subject for
  // diagnosis.
  const results = await Promise.allSettled([
    request<SidebarBucketReply>(currentSubject, {}),
    request<SidebarBucketReply>(appsSubject, {}),
  ])
  const empty: SidebarBucketReply = { subscriptions: [], total: 0 }
  const unwrap = (
    result: PromiseSettledResult<SidebarBucketReply>,
    label: string,
  ): SidebarBucketReply => {
    if (result.status === 'fulfilled') {
      // TEMP DEBUG: log each subscription RPC reply so we can see exactly
      // what the user-service returns on cold start. Remove once the live
      // backend behaviour is verified.
      console.log('[sidebar-bootstrap]', label, result.value)
      return result.value
    }
    const err = result.reason
    console.warn(
      '[sidebar-bootstrap]',
      label,
      'FAILED:',
      err?.message ?? err,
    )
    return empty
  }
  const allResp = unwrap(results[0], currentSubject)
  const appResp = unwrap(results[1], appsSubject)
  const subscriptions: Record<string, DMSubscription> = {}
  const collect = (resp: SidebarBucketReply) => {
    for (const s of resp.subscriptions) {
      if (!s?.roomId) continue
      // Later sources overwrite earlier ones, but the responses describe
      // the same Subscription record so collisions are benign.
      subscriptions[s.roomId] = s
    }
  }
  collect(allResp)
  collect(appResp)
  return {
    favoriteIds: [],
    appIds: appResp.subscriptions.map((s) => s.roomId),
    channelDmIds: allResp.subscriptions.map((s) => s.roomId),
    subscriptions,
  }
}
