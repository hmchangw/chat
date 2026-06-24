# Teams Presence In-Call Sync — Design Spec

**Status:** Draft (awaiting approval)
**Date:** 2026-06-22
**Author:** (session)

## 1. Problem & Goal

Users are often in a Microsoft Teams call while also reachable in chat. We want chat presence to reflect that: when a user is in a Teams call, show them as **`in-call`** and suppress their notifications (DND). A periodic, externally-scheduled job reads Teams presence from Microsoft Graph and feeds it into the existing presence model.

**In scope:** mapping Teams "in a call/meeting" to a new `in-call` presence; the recompute/aggregation change; a one-shot sync binary; Graph client extensions; deploy + docs.

**Out of scope:** real-time Teams webhooks/subscriptions; statuses other than in-call (e.g. surfacing Teams "Away"/"Busy" generally); any frontend change beyond clients already rendering `PresenceState`.

## 2. Background (current system)

`user-presence-service` keeps presence in Valkey (cluster) and recomputes an **effective status** in a Lua script (`store_valkey.go` / `computeLua`) from two layers:

1. **Connections** — per-connection liveness hash → `online` (any active), `away` (all inactive), `offline` (none live).
2. **Manual override** — `online`/`away`/`busy`/`appear_offline`, applied only while the user is live (`appear_offline` → offline always).

The materialized status is written to `presence:{account}:status` and broadcast as a `PresenceState` event on `chat.user.presence.state.{account}`. `BatchGet`/`QueryBatch` read the materialized value. A sweeper recomputes accounts as connection deadlines pass.

`notification-worker` already treats `"in-call"` as DND (`shouldPush` in `notification-worker/presence.go:121` returns false for `busy`/`in-call`); the wire value and DND semantics are pre-existing in code and docs — only the *producer* of `in-call` is missing.

`pkg/msgraph` is a minimal app-only (client-credentials) Graph client that today only creates online meetings. `room-service` derives a member's Teams email as `account + "@" + TEAMS_EMAIL_DOMAIN` (`room-service/handler_teams.go:30`), mirroring auth-service's dev derivation.

## 3. Approach overview

- Add a third **external** layer (Teams) to the presence aggregation, stored at `presence:{account}:azure` and read atomically in the recompute Lua (same `{account}` hash-tag → same cluster slot).
- A new **one-shot cron binary** (`user-presence-service/sync`) reconciles Teams call state into that layer each run, writing directly to Valkey and publishing `PresenceState` changes itself.
- To let a separate binary share the recompute Lua + key layout, **extract** the Valkey store from the daemon's `package main` into an importable `user-presence-service/presencestore` package, consumed by both the daemon and the sync.
- Extend `pkg/msgraph` with app-only `ListUsers` and an ROPC (delegated) `PresenceClient.GetPresencesByUserId`.

## 4. Decisions (resolved during brainstorming)

| # | Decision | Choice |
|---|----------|--------|
| D1 | Status value | Reuse existing wire value **`in-call`** (kebab); add `model.StatusInCall`. Not a valid *manual* status. |
| D2 | Aggregation | Add external layer; new precedence (see §5). |
| D3 | Liveness | `in-call` **respects** the "no active connection ⇒ offline" invariant (live-gated, like manual statuses). |
| D4 | Write path | **Direct Valkey write + publish** from the sync, via shared `presencestore`. |
| D5 | Runtime | **One-shot** binary, triggered by an external cron (K8s CronJob / pipeline schedule); runs once, exits. |
| D6 | Teams→in-call mapping | **Call/meeting activities only**: activity ∈ {`InACall`, `InAConferenceCall`, `Presenting`}. |
| D7 | Graph auth | **ROPC** service account for presence (`Presence.Read.All`, delegated) + **app-only** for `/users` (`User.Read.All`). |
| D8 | User matching | `account + "@" + TEAMS_EMAIL_DOMAIN` matched against Graph `mail` (fallback `userPrincipalName`). |
| D9 | Graph client location | **Extend `pkg/msgraph`** (app-only `ListUsers`; new ROPC `PresenceClient`). |
| D10 | Directory layout | `user-presence-service/presencestore/` (shared) + `user-presence-service/sync/` (`package main` + `deploy/`). |
| D11 | id→account mapping | **Cache** `account → azureObjectID` in Valkey (`presence:idmap:azure`), refreshed by a TTL-gated full `ListUsers` sweep — not re-enumerated every run (the mapping is effectively static). |

## 5. Aggregation / precedence (the core change)

Effective status is recomputed in `computeLua` from connections (`anyLive`, `anyActive`), the manual override (`KEYS[2]`), and the new azure/external key (`KEYS[4]`):

```text
if not anyLive               -> offline      (invariant wins over everything)
else (user is live):
  manual == 'appear_offline' -> offline      ┐ high manual tier
  manual == 'away'           -> away         ┘ (above in-call)
  azure  == 'in-call'        -> in-call      ← external layer
  manual == 'online'|'busy'  -> that         ┐ low manual tier
  anyActive                  -> online       │ (below in-call)
  else                       -> away         ┘ connection-derived
```

This splits today's single manual overlay into a **high tier** (away / appear_offline, above in-call) and a **low tier** (online / busy, below in-call). All existing operations (ping/activity/bye/manual/sweep) inherit the new rule because they share `computeLua`.

`in-call` is **external-only**: the `SetManual` allow-list is unchanged, so a client cannot set it.

## 6. Valkey key design

| Key | Type | Owner | Purpose |
|-----|------|-------|---------|
| `presence:{account}:azure` | string | presencestore | External (Teams) status: `"in-call"` or absent. Read as `KEYS[4]` in recompute. Written with a **TTL safety-net** (~`EXTERNAL_TTL`, default 5m) so a dead sync self-heals. |
| `presence:status:index:azure` | set | sync | Accounts currently marked in-call. Lets a run compute `toClear = prev − current`. Single key (own slot); updated in a pipeline (not atomic with per-account keys — acceptable for a reconciler). |
| `presence:idmap:azure` | hash | sync | `account → azureObjectID` cache. Read every run to resolve our accounts to Graph IDs without enumerating the tenant. Refreshed by a TTL-gated full `ListUsers` sweep (see §7), gated by the marker key below. |
| `presence:idmap:azure:fresh` | string | sync | Marker with `IDMAP_REFRESH_TTL`. Present ⇒ the idmap is fresh, skip the `ListUsers` sweep; absent/expired ⇒ one run does the full sweep and re-sets it. |

## 7. The sync service

**Type:** `user-presence-service/sync` — `package main`, its own `main.go` + `deploy/Dockerfile`, triggered by an external cron. Runs one reconcile and exits (non-zero on failure).

**Reconcile flow (per run):**
1. `ListSiteAccounts(siteID)` from Mongo `users` → the accounts we care about (presence is site-scoped).
2. **Refresh idmap if stale:** if `presence:idmap:azure:fresh` is absent/expired, run Graph app-only `ListUsers` once, rebuild `presence:idmap:azure` (`account → azureObjectID`, restricted to accounts matching `accountFromEmail(mail|UPN, TEAMS_EMAIL_DOMAIN)`), then set the marker with `IDMAP_REFRESH_TTL`. Otherwise skip the sweep entirely.
3. **Resolve via cache:** `HMGET presence:idmap:azure <our accounts>` → the IDs to query (in-memory `azureID → account` inverse for this run). Accounts absent from the map aren't in Teams → skipped until the next sweep.
4. Graph ROPC `GetPresencesByUserId(ids)` (batched ≤650/req).
5. `current` = accounts whose presence `isInCall` (activity ∈ {InACall, InAConferenceCall, Presenting}).
6. `prev` = `SMEMBERS presence:status:index:azure`.
7. For each `current`: `SetExternal(account, in-call, ttl)` → `SADD` → publish `PresenceState` if changed.
8. For each `prev − current`: `SetExternal(account, "", ttl)` (clear) → `SREM` → publish if changed.
9. Log a summary; exit 0.

Full status reconciliation each run means a missed/crashed run self-corrects next time; the per-key TTL covers total sync death. The idmap is the *only* thing not rebuilt every run — it's TTL-refreshed because the mapping is effectively static (Azure object IDs are immutable). The cost: a newly-added user isn't eligible for `in-call` until the next idmap refresh (bounded by `IDMAP_REFRESH_TTL`) — acceptable latency for this feature.

**Config (env):** `SITE_ID`, `TEAMS_EMAIL_DOMAIN`, `EXTERNAL_TTL`, `IDMAP_REFRESH_TTL` (default e.g. 1h), `RUN_TIMEOUT`, `NATS_URL`/`NATS_CREDS_FILE`, `VALKEY_ADDRS`/`VALKEY_PASSWORD`, `MONGO_URI`/`MONGO_DB`/creds, `PRESENCE_STALE_THRESHOLD`/`PRESENCE_CONNS_TTL` (must match the daemon), `GRAPH_TENANT_ID`/`GRAPH_CLIENT_ID`/`GRAPH_CLIENT_SECRET` (app-only), `GRAPH_ROPC_USERNAME`/`GRAPH_ROPC_PASSWORD` (service account). Secrets are `required`, never defaulted, never logged.

## 8. `pkg/msgraph` extensions

- **App-only** (existing client, reuses `accessToken`): `ListUsers(ctx) ([]GraphUser, error)` with `@odata.nextLink` paging; `GraphUser{ID, Mail, UserPrincipalName}`.
- **ROPC** (new `PresenceClient`, separate token cache, `grant_type=password`): `GetPresencesByUserId(ctx, ids) ([]Presence, error)`; `Presence{ID, Availability, Activity}`; batches at Graph's 650-id cap. Behind a `PresenceReader` interface for mocking.

## 9. Code structure & impact

```text
pkg/model/presence.go            + StatusInCall
pkg/msgraph/msgraph.go           + ListUsers, GraphUser
pkg/msgraph/presence.go          NEW  ROPC PresenceClient, GetPresencesByUserId

user-presence-service/presencestore/   NEW  (moved from store_valkey.go)
    store.go   keys (+azureKey), Lua (+KEYS[4], new precedence), Store, SetExternal,
               StatusChange, PublishFunc, PublishState, NewValkeyStore(FromClient)
user-presence-service/store.go         PresenceStore interface → presencestore.StatusChange, +SetExternal
user-presence-service/{handler,sweeper,main}.go   use presencestore.*
user-presence-service/store_valkey.go  DELETED
(+ daemon test refs & regenerated mock)

user-presence-service/sync/            NEW  package main (cron one-shot)
    main.go, reconcile.go, store.go(+impl), valkey.go, *_test.go, mock_test.go
    deploy/{Dockerfile,docker-compose.yml,azure-pipelines.yml}

docs/client-api.md               + in-call in the PresenceStatus enum
```

The daemon's runtime behavior is unchanged except the new precedence in the shared Lua. `notification-worker` needs **no change** (already DND on `in-call`).

## 10. Deferred / non-goals

- App-only presence (single auth flow) — rejected per D7; revisit only if the tenant grants application `Presence.Read.All` for `getPresencesByUserId`.
- Surfacing non-call Teams statuses.

## 11. Testing strategy

- **Unit:** model round-trip (`StatusInCall`); `pkg/msgraph` `ListUsers` paging + ROPC `GetPresencesByUserId` (httptest, grant-type/username asserted, token never logged); sync `isInCall`/`accountFromEmail` table tests; sync `reconcile.run` with mocked Graph/store/index/publisher (set + clear paths).
- **Integration (testcontainers Valkey, `pkg/testutil`):** precedence matrix (away/appear_offline beat in-call; in-call beats online/busy; offline invariant; clear restores connection-derived) and `SetExternal` in `presencestore`.
- **Gates:** `make test`, `make lint`, `make sast` (no medium+), `make test-integration SERVICE=user-presence-service`. ≥80% coverage on new packages.

## 12. Risks & mitigations

- **Stale in-call** if a run fails mid-way: full reconcile next run + per-key TTL self-heal.
- **Bucket/threshold drift:** sync must use the same `PRESENCE_STALE_THRESHOLD`/`PRESENCE_CONNS_TTL` as the daemon (documented, configured together).
- **ROPC fragility** (MFA/conditional-access on the service account): out of band; document the service-account requirements; failures exit non-zero and are visible to the cron platform.
- **Cross-slot index vs per-account writes** are not atomic: acceptable; reconciler is idempotent and self-correcting.

## 13. Open questions for reviewer

1. `EXTERNAL_TTL` default of 5m — acceptable, or tie to expected cron cadence?
2. Should the sync also clear `in-call` for accounts that left the tenant/`/users` (not just those that left a call)? Current design clears via the index diff, which covers this.
3. `IDMAP_REFRESH_TTL` default — 1h reasonable? (Bounds how long a newly-added Teams user waits before becoming `in-call`-eligible.)
