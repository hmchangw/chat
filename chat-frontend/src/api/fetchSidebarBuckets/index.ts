import {
  userSubscriptionGetCurrent,
  userSubscriptionGetApps,
  userSubscriptionGetRooms,
} from '../_transport/subjects'
import type { Nats } from '../types'

/** Per-room subscription metadata sourced from the user-service RPCs.
 *  Each entry corresponds to one of the three `subscription.get*` replies. */
export interface SidebarSubscription {
  roomId: string
  name?: string
  hrInfo?: {
    engName?: string
    name?: string
  }
}

interface SidebarBucketReply {
  subscriptions?: SidebarSubscription[]
}

export interface SidebarBuckets {
  favoriteIds: string[]
  appIds: string[]
  channelDmIds: string[]
  /** Per-roomId map of {name, hrInfo} sourced from any of the three
   *  RPCs. The reducer merges this onto room summaries at read time so
   *  `roomDisplayName` can resolve subscription.Name (channels) or
   *  HRInfo (dm rooms) without changing the underlying summary shape. */
  subscriptionData: Record<string, { name?: string; hrInfo?: SidebarSubscription['hrInfo'] }>
}

/**
 * Fetch the three sidebar bucket lists in parallel: favorites, apps,
 * and non-app rooms (channels / DMs / discussions). Each comes from
 * its own user-service RPC.
 *
 * Returns a single merged result so the caller doesn't have to know
 * about the three underlying subjects. The reducer's `BUCKETS_LOADED`
 * action consumes this shape directly.
 */
export async function fetchSidebarBuckets({ user, request }: Nats): Promise<SidebarBuckets> {
  const [favResp, appResp, roomResp] = await Promise.all([
    request<SidebarBucketReply>(
      userSubscriptionGetCurrent(user.account, user.siteId),
      { favorite: true },
    ),
    request<SidebarBucketReply>(userSubscriptionGetApps(user.account, user.siteId), {}),
    request<SidebarBucketReply>(userSubscriptionGetRooms(user.account, user.siteId), {}),
  ])
  const subscriptionData: SidebarBuckets['subscriptionData'] = {}
  const collect = (resp: SidebarBucketReply) => {
    for (const s of resp?.subscriptions ?? []) {
      if (!s?.roomId) continue
      // Later sources overwrite earlier ones, but the three responses
      // describe the same Subscription record so collisions are benign.
      subscriptionData[s.roomId] = { name: s.name, hrInfo: s.hrInfo }
    }
  }
  collect(favResp)
  collect(appResp)
  collect(roomResp)
  return {
    favoriteIds: (favResp?.subscriptions ?? []).map((s) => s.roomId),
    appIds: (appResp?.subscriptions ?? []).map((s) => s.roomId),
    channelDmIds: (roomResp?.subscriptions ?? []).map((s) => s.roomId),
    subscriptionData,
  }
}
