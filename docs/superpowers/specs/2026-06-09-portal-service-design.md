# portal-service — design

**Status:** Ready for implementation planning
**Date:** 2026-06-11

## 1. Context & problem

The chat system is multi-site, fronted by a **single global Keycloak realm** and a **NATS
supercluster**: browsers connect to a NATS **gateway** that routes each connection to the
user's home site by the JWT's signing account. Message history is a distributed Cassandra
(one logical store); MongoDB (rooms/subscriptions) is per-site. Each site serves its own
**chat-frontend** deployment.

chat-frontend logs in against Keycloak (auth-code + PKCE via `oidc-client-ts`), generates a
NATS nkey **in the browser** (`nkeys.js` `createUser()` in
`src/context/NatsContext/NatsContext.jsx`), POSTs `{ssoToken, natsPublicKey}` to
`auth-service /auth`, receives `{natsJwt, user}`, and connects with
`jwtAuthenticator(jwt, seed)`. The nkey **seed never leaves the browser**.
`auth-service/handler.go:signNATSJWT` mints the user JWT signed by the site account NKey.

Two gaps in that flow:
1. The browser must already know **which site's** `auth-service` to call and **which** NATS
   URL to dial — per-site knowledge that doesn't belong in the client.
2. Nothing enforces that an account is **provisioned/ready** before it gets credentials, and a
   user who lands on the **wrong site's frontend** has no path to their own.

**portal-service** fills both gaps. It is a thin, **stateless-per-request** broker between the
Keycloak-authenticating frontend and the per-site auth-services. Keycloak login stays in the
frontend; the portal engages only **after** login, when the browser asks for NATS credentials.
Given a logged-in user's SSO token and a NATS public key, the portal resolves the user's home
site from an in-memory directory, **enforces readiness as a security gate**, sends a user on
the wrong frontend to their home-site frontend, forwards to the home site's `auth-service` to
mint the JWT, and returns the JWT plus the home-site NATS URL.

## 2. Goals / non-goals

**Goals**
- One credential entry per site: the frontend calls its site's portal endpoint; the portal
  handles all multi-site routing.
- Resolve the user's home `siteId` (directory) and enforce **readiness** — this is a
  **security control**, not advisory UX: an unprovisioned account must not be able to obtain
  NATS credentials at all.
- Direct a user who logs in on the wrong site's frontend to **their own** site's frontend.
- Reuse each site's `auth-service` to mint the `natsJwt` (portal **forwards**, never mints).
- The **nkey seed stays in the browser** — the portal only ever sees the public key.
- Portal **returns the home-site NATS URL** (`natsUrl`) so the frontend needs no static
  NATS/auth backend config beyond the portal's own URL.
- No per-user server state — the browser carries the SSO token; the portal relays it.

**Non-goals**
- **Not an OIDC client** — the portal takes no part in Keycloak authentication.
- **No NATS proxying** — the browser connects to the gateway directly; the portal stays out of
  the data path.
- Portal does **not** own/sync directory data — read-only (a daily ops cron owns it; §4).
- No persistent UI.
- No change to `auth-service` **code** — the lockdown in §2.1 is deployment/network-level.

### 2.1 Security model — the gate must not be bypassable

Today the browser calls `auth-service /auth` directly; if that stayed reachable, anyone could
skip the portal and the readiness gate would be decorative. Therefore:

- **`auth-service` becomes internal-only.** No public ingress; reachable only from the portal
  (and other internal services) via network policy. Browsers obtain NATS credentials
  **exclusively** through the portal.
- The portal forwards the `ssoToken` unchanged; `auth-service` still validates it itself
  (defense in depth — the portal gates, `auth-service` remains the minting authority).
- The NATS gateway stays public by necessity; a valid `natsJwt` is required to connect.
- `Origin` is a **UX signal only** (cross-site redirect, §3.1) — never a security input. The
  security inputs are the verified token and the directory.
- `DEV_MODE` bypasses token verification and must never be set in production manifests
  (compose/local only).

## 3. Architecture & topology

**One portal deployment per site** (single pod each, to start). Each site's frontend is
configured with its own site's `PORTAL_URL`. Every portal pod holds the **full global
directory** as an in-memory map plus the full site config maps (§5), so any portal can
recognize any user and redirect them home. A portal outage therefore affects only its own
site's logins/refreshes.

**Directory distribution:** the `directory` collection lives in **each site's MongoDB**; the
daily ops cron populates every site's copy, then calls that site's portal
`POST /admin/cache/reload`. The portal **bulk-loads** the collection into memory at startup
(fail fast if the load fails) and atomically swaps the map on each reload. **The request path
never queries Mongo** — a map miss is terminal (`account_not_ready`).

`portal-service` is a small **Gin HTTP service**. One client-facing route, plus admin + health:

1. `POST /session/nats-jwt` — body `{ssoToken, natsPublicKey}`. Verifies the SSO token (JWKS)
   to derive the account, looks it up in the directory map (readiness gate), and then either
   **directs a cross-site login home** (returns `{redirectTo}`) or **forwards**
   `{ssoToken, natsPublicKey}` to the home site's `auth-service /auth` and returns
   `{natsJwt, natsUrl, user}`.
2. `POST /admin/cache/reload` — reload the directory map from Mongo; bearer-token-guarded
   (called by the daily cron after it syncs). Internal-only route.
3. `GET /healthz` — liveness + directory record count (`200 {"status":"ok","records":N}`). Startup fail-fast guarantees a live pod
   always has a loaded map.

```
 Browser ─ Keycloak login (direct) ──► SSO token          [portal NOT involved]
 Browser ─ generate nkey keypair  (seed stays in the browser)
 Browser ─ POST /session/nats-jwt {ssoToken, natsPublicKey}  (Origin: this frontend) ──► site portal
                                                                  │ verify token (pkg/oidc, JWKS) → account
                                                                  │ directory map: ready? which siteId?  (no Mongo)
                                                                  │
                                          wrong frontend?  ───────┤
                                          ◄─ {redirectTo: home-site frontend}   (no mint)
                                          (browser navigates there, re-runs the call)
                                                                  │
                                          home frontend ──────────┤
                                                                  │ forward {ssoToken, natsPublicKey} ──► home-site auth-service
                                                                  │ ◄─ {natsJwt, user}            (internal-only; mints natsJwt)
                                                                  │ natsUrl = SITE_NATS_URLS[siteId]
 Browser ◄─ {natsJwt, natsUrl, user} ─────────────────────────────┘
 Browser ─ nats.ws(natsJwt + seed) ──► NATS gateway ──► home-site NATS  (supercluster routes by account)
```

### 3.1 Cross-site redirect

A user may open any site's chat-frontend. After Keycloak login, the frontend calls its site's
portal; the portal compares the request's `Origin` against the user's home-site frontend
(`SITE_FRONTEND_URLS[siteId]`):
- **Match** → proceed to forward + return credentials.
- **Mismatch** (Origin maps to a known frontend that isn't home) → return
  `200 {redirectTo: <home-site frontend>}` **without** forwarding; the frontend navigates there
  (`window.location.assign`), and the home-site frontend re-runs the call against its own
  portal (the Keycloak session already exists, so login is silent).
- **Origin absent or unknown** → proceed (non-browser callers; the redirect is best-effort UX —
  the token + directory gate is what protects minting).

Origins are compared **normalized** (scheme + host + port, no path); `SITE_FRONTEND_URLS`
values are canonical origins.

### 3.2 Multi-site transparency

The browser holds no per-site logic: it calls one portal endpoint and gets back a JWT signed by
the right site plus that site's `natsUrl`. Refresh re-runs the same call — the frontend renews
its SSO token via Keycloak (as today) and re-POSTs to the portal.

## 4. Data model — directory

One MongoDB doc per account in each site's `directory` collection; account = `_id`.

| field      | json/bson    | type   | notes                                        |
|------------|--------------|--------|----------------------------------------------|
| Account    | `_id`        | string | natural key (Keycloak `preferred_username`)  |
| EmployeeID | `employeeId` | string | informational (ops/troubleshooting); not consumed by the credential flow |
| SiteID     | `siteId`     | string | home site                                    |

- **Ready** = present in the map **and** `siteId` non-empty **and** `siteId` configured in both
  `SITE_AUTH_URLS` and `SITE_NATS_URLS`.
- Anything else — no record, empty `siteId`, unconfigured `siteId` — is **one** client-facing
  outcome: `account_not_ready` (403). The server log records which case it was.

**In-memory map (not a lazy cache):**
- **Startup:** `LoadAll` the collection → `map[account]record`. Load failure ⇒ log and exit
  (fail fast, consistent with repo convention).
- **Request path:** map lookup only — never Mongo. Map miss ⇒ `account_not_ready`.
- **Reload:** `POST /admin/cache/reload` re-runs `LoadAll` and **atomically swaps** the map
  (`atomic.Pointer` / `RWMutex`). On failure: keep serving the old map, log, return `500` so
  the cron retries/alerts.
- **Consequence (accepted):** a newly provisioned user appears only after the cron syncs and
  reloads — for an urgent add, ops runs the cron/reload on demand.
- An already-issued `natsJwt` stays valid until expiry — a hard cut-off requires the cron to
  also disable the user in Keycloak (which kills SSO refresh).

**Sync (ops-owned, out of scope):** the daily cron adds/removes users per site, writes every
site's `directory` collection, then calls each site portal's reload endpoint.

## 5. Site resolution — config maps

Every portal deployment carries the **full** (all-site) maps:

```
SITE_AUTH_URLS=site-a=http://auth-a:8080,site-b=http://auth-b:8080      # portal → auth-service (internal network)
SITE_NATS_URLS=site-a=wss://nats-a:9222,site-b=wss://nats-b:9222        # returned to the client as natsUrl
SITE_FRONTEND_URLS=site-a=https://chat-a.example.com,site-b=https://chat-b.example.com   # redirect targets + CORS allowlist
```

Site endpoints are infra/ops config, kept out of per-employee records. A `siteId` missing from
`SITE_AUTH_URLS`/`SITE_NATS_URLS` ⇒ not ready (logged as misconfiguration). A `siteId` missing
from `SITE_FRONTEND_URLS` ⇒ no redirect (proceed). The CORS allowlist **is** the set of
`SITE_FRONTEND_URLS` values — no separate origin config. Cross-site forwards (portal →
another site's auth-service) ride the existing inter-site internal network and are rare by
construction (the redirect sends browsers home first).

## 6. Endpoints & contracts

### 6.1 `POST /session/nats-jwt` — the client-facing auth entry

Request: `{ "ssoToken": "<keycloak access token>", "natsPublicKey": "U..." }`

Handler order:
1. Bind body; require both fields; validate `natsPublicKey` (`nkeys.IsValidPublicUserKey`) →
   `400` on bad input.
2. **Verify** `ssoToken` via `pkg/oidc` — the **same validator implementation auth-service
   uses** (issuer + audience + expiry) → `401` (`invalid_sso_token` / `sso_token_expired`).
   Derive the account with the **identical fallback chain** as `auth-service/handler.go`:
   `preferred_username` → `name` → reject `401` (blank account must never reach lookup).
3. Directory **map** lookup + readiness gate → `403 account_not_ready` on miss / empty
   `siteId` / unconfigured `siteId` (server log carries the specific case).
4. **Cross-site check** (§3.1): known foreign `Origin` → `200 {redirectTo}`, stop.
5. Resolve `authURL = SITE_AUTH_URLS[siteId]`, `natsUrl = SITE_NATS_URLS[siteId]`.
6. **Forward** `{ssoToken, natsPublicKey}` to `${authURL}/auth` (Resty), propagating
   `X-Request-ID`; relay the upstream response (§7).

Success `200` (home site):
```json
{
  "natsJwt": "<signed nats user jwt>",
  "natsUrl": "wss://nats-a:9222",
  "user": { "email": "...", "account": "...", "employeeId": "...", "engName": "...", "chineseName": "...", "deptName": "...", "deptId": "..." }
}
```
Redirect `200` (wrong frontend):
```json
{ "redirectTo": "https://chat-a.example.com" }
```
The same endpoint serves initial issue and silent refresh (same inputs, fresh JWT).

### 6.2 `POST /admin/cache/reload`
Reloads the directory map from Mongo (atomic swap). Guarded by
`Authorization: Bearer ${ADMIN_TOKEN}`; missing/wrong token → `401`. Reload failure → `500`
(old map keeps serving). Deployed as an internal-only route (not exposed on public ingress).
Called by the daily cron after each sync.

### 6.3 `GET /healthz` → `200 {"status":"ok","records":N}` (liveness + directory record count).

### 6.4 `DEV_MODE`
With `DEV_MODE=true` (no Keycloak), token verification is bypassed: `/session/nats-jwt` accepts
`{account, natsPublicKey}`, runs the same directory/readiness/redirect/routing logic, and
forwards the dev-shape body to the dev-mode site auth-service. With `DEV_MODE=false`, the
`account`-only form is rejected and a verified `ssoToken` is required.

## 7. Error model

`pkg/errcode` + `errhttp.Write` (JSON) on every error path — the portal is a pure API. One new
reason in `pkg/errcode/codes_portal.go`:

| reason              | constant                | category / status |
|---------------------|-------------------------|-------------------|
| `account_not_ready` | `PortalAccountNotReady` | `Forbidden` (403) |

- Token-verification failures reuse the existing auth reasons (`sso_token_expired`,
  `invalid_sso_token`, `401`).
- **Upstream relay:** a non-2xx from `auth-service` is decoded with `errcode.Parse`
  (cross-service envelope consumption — the sanctioned Tier-3 use) and relayed with its
  original category/status and reason.
- **Transport failure** to `auth-service` (unreachable, timeout): return the raw wrapped error
  (`fmt.Errorf("forward auth: %w", err)`) — it collapses to `internal` (500) at the boundary;
  never misreport infra failures as `account_not_ready`.
- The `redirectTo` response is a normal `200`, not an error.
- The frontend surfaces error reasons via its existing handling (`formatAsyncJobError` falls
  back to message text).

## 8. Token validation & forwarding (no session)

- The portal **validates** the frontend-issued SSO token via `pkg/oidc` against the realm JWKS
  (`OIDC_ISSUER_URL`, `OIDC_AUDIENCES`) — reusing the exact validator `auth-service` wires, so
  the two can never disagree on validity or on the account claim. **No client secret, no OIDC
  redirect, no session, no token storage.**
- Why verify (vs. forward blindly): the portal must trust the account to pick the home site
  *before* it knows which `auth-service` to call. The same `ssoToken` is then forwarded and
  re-validated by `auth-service` (defense in depth).
- The **nkey seed never reaches the portal** — the browser holds it and presents it only to
  NATS.
- **CORS:** the token travels in the request **body**, not a cookie — no SameSite concerns.
  Allowed origins = `SITE_FRONTEND_URLS` values; the middleware answers the `OPTIONS`
  preflight (JSON `POST` is non-simple) and allows `POST` + `Content-Type`.
- **Audience:** `OIDC_AUDIENCES` must match the frontend Keycloak client's audience (the same
  value `auth-service` validates today).
- **Logging discipline:** request middleware logs method/path/status/latency/request-ID only —
  never bodies, never the `ssoToken` (CLAUDE.md). Request ID per CLAUDE.md: accept a valid
  inbound `X-Request-ID` or generate via `idgen.GenerateRequestID()`; propagate to
  `auth-service` on the forward.

## 9. Service layout (flat dir, DI for TDD)

```
portal-service/
  main.go         config, mongo connect + initial LoadAll (fail fast), pkg/oidc verifier, Resty, wiring, graceful shutdown
  routes.go       POST /session/nats-jwt, POST /admin/cache/reload, GET /healthz
  handler.go      PortalHandler{ verifier TokenVerifier; dir *dirMap; forwarder AuthForwarder; siteAuth, siteNats, siteFrontend map[string]string }
  store.go        DirectoryStore interface — LoadAll(ctx) ([]directoryRecord, error) + //go:generate mockgen
  store_mongo.go  Mongo impl
  dirmap.go       in-memory directory map: Lookup(account), Reload(ctx, store) with atomic swap
  forwarder.go    AuthForwarder interface (Resty impl) — Forward(ctx, authURL, body) (status, respBody, err); propagates X-Request-ID
  middleware.go   requestID + accessLog (no bodies/tokens) + CORS (origins = SITE_FRONTEND_URLS, preflight)
  handler_test.go / integration_test.go (//go:build integration) / mock_store_test.go
  deploy/         Dockerfile, docker-compose.yml, azure-pipelines.yml
```

Consumer-defined interfaces: `TokenVerifier` (`Verify(ctx, token) (claims, error)`),
`DirectoryStore` (`LoadAll(ctx) ([]directoryRecord, error)`), `AuthForwarder`.

**Shutdown:** HTTP-only service — graceful HTTP stop then Mongo disconnect via
`pkg/shutdown.Wait`. No NATS connection anywhere in this service.

## 10. Configuration (`caarlos0/env`)

| env                    | default      | notes                                            |
|------------------------|--------------|--------------------------------------------------|
| `PORT`                 | `8080`       |                                                  |
| `DEV_MODE`             | `false`      | bypass token verification; `account`-only body; never in prod |
| `OIDC_ISSUER_URL`      | —            | required when `DEV_MODE=false` (JWKS)            |
| `OIDC_AUDIENCES`       | —            | required when `DEV_MODE=false`; frontend client audience |
| `MONGO_URI`            | — (required) |                                                  |
| `MONGO_USERNAME` / `MONGO_PASSWORD` | `""` | optional Mongo credentials (repo-standard `mongoutil.Connect` wiring) |
| `MONGO_DB`             | `chat`       |                                                  |
| `DIRECTORY_COLLECTION` | `directory`  |                                                  |
| `SITE_AUTH_URLS`       | — (required) | `siteId=url` map (portal → auth-service, internal) |
| `SITE_NATS_URLS`       | — (required) | `siteId=url` map (returned to client)            |
| `SITE_FRONTEND_URLS`   | — (required) | `siteId=origin` map (redirect targets + CORS allowlist) |
| `AUTH_FORWARD_TIMEOUT` | `10s`        | Resty timeout to site auth-service               |
| `ADMIN_TOKEN`          | — (required) | bearer token guarding `POST /admin/cache/reload` |
| `TLS_SKIP_VERIFY`      | `false`      | dev only                                          |

Secrets/connection strings are `required` with no default; non-critical values get
`envDefault`. Fail fast on missing required config **and** on a failed initial directory load.

## 11. Testing (TDD, per CLAUDE.md)

- **Unit** (`handler_test.go`): red-green-refactor, table-driven, all deps mocked. Cover:
  valid issue (verify → map hit → forward → relay incl. `natsUrl`); account derivation
  fallback (`preferred_username` → `name`) and blank-account reject; map miss →
  `account_not_ready`; empty/unconfigured `siteId` → `account_not_ready`; cross-site redirect
  (known foreign `Origin` → `{redirectTo}`, no forward); same-site and absent/unknown `Origin`
  → proceed; invalid/expired token (401); upstream error envelope relayed with original
  status/reason; transport failure → 500 internal; invalid `natsPublicKey` / missing fields
  (400); reload swaps the map atomically; reload failure keeps the old map and returns 500;
  reload auth (bearer) required; `DEV_MODE` account-only path. ≥80% coverage, target 90%+ for
  the handler.
- **Integration** (`//go:build integration`): `testutil.MongoDB` backing `LoadAll` +
  reload-after-write; an `httptest` server stubbing the site auth-service to assert forward
  (incl. `X-Request-ID` propagation) + relay; `TestMain` → `testutil.RunTests(m)`.
- `make lint` and `make sast` clean before push.

## 12. Deploy & docs

- Multi-stage Dockerfile (`golang:1.25.11-alpine` → `alpine:3.21`, repo-root context).
- `docker-compose.yml` wires portal + Mongo + Keycloak + a site auth-service end-to-end and
  seeds `directory` docs. **No new Keycloak client** — the portal only *validates* tokens from
  the existing frontend client.
- **Lockdown note:** production exposes only the portal (and the NATS gateway) publicly;
  `auth-service` has no public ingress (§2.1). Compose mirrors this by not publishing
  auth-service's port to the host.
- `azure-pipelines.yml` mirrors a sibling service.
- **`docs/client-api.md`:** document `POST /session/nats-jwt` (request, both `200` shapes,
  error cases) — it becomes the client-facing auth entry.

## 13. Assumptions & out-of-scope

**Assumptions**
- Keycloak authentication stays in chat-frontend; the portal is not an OIDC client and engages
  only after login.
- Readiness is a security control; `auth-service` is internal-only (network policy), portal is
  the sole public credential entry (§2.1).
- One portal deployment per site, single pod each; every pod holds the full global directory
  map and full site maps.
- The `directory` collection is replicated into each site's Mongo by the daily ops cron, which
  also triggers each portal's reload. The portal reads only.
- `auth-service` remains the sole minter; the supercluster routes by the JWT's signing
  account; the browser dials the portal-supplied gateway URL.
- The nkey seed never leaves the browser; the browser sends only the public key.
- `employeeId` stays in the directory record (informational; not consumed by the flow).
- All sites serve the same chat-frontend codebase (any site's frontend can run the redirect).

**Out-of-scope / future**
- Scaling a site's portal beyond one pod (reload would need to fan out to every pod — e.g. a
  headless-service loop or moving the map to shared Valkey).
- Deep-link preservation across the cross-site redirect (targets the home frontend origin).
- Immediate revocation of a live `natsJwt` (bounded by jwt lifetime + Keycloak user-disable).

## 14. chat-frontend changes (minimal)

The frontend keeps its Keycloak/OIDC login (`@/api/auth/oidcClient`), nkey keypair generation
(`nkeys.js` `createUser()` in `NatsContext.jsx`), and the natsJwt re-mint/refresh loop
(`useJwtRefresh.js`). Only the credential call moves and a redirect branch is added:

- **Repoint** both `${AUTH_URL}/auth` call sites — `connect()` in `NatsContext.jsx:52` and the
  refresh in `useJwtRefresh.js:92` — to `${PORTAL_URL}/session/nats-jwt`. Request body
  unchanged (`{ssoToken, natsPublicKey}`).
- **Handle the redirect in both call sites:** if the response contains `redirectTo`, call
  `window.location.assign(redirectTo)` instead of connecting (covers login on the wrong site
  and a mid-session site move surfacing at refresh).
- **Consume `natsUrl`** from the response (`{natsJwt, natsUrl, user}`) and connect to it,
  instead of reading a static `NATS_URL`.
- `runtimeConfig.js`: replace `AUTH_URL` + `NATS_URL` with `PORTAL_URL` (each site's frontend
  points at its own site's portal); keep `DEV_MODE` and the `OIDC_*` values (login unchanged).

Follow `chat-frontend/CLAUDE.md` for edits; update tests; `npm run typecheck` + `npm test`
green; clean `vite build`.
