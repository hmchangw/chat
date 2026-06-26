# Portal-Service SSO Login Front-Door — Design

**Date:** 2026-06-24
**Status:** Approved
**Branch:** `claude/gallant-darwin-k0sc1h`

## Problem

`portal-service` is a cache-backed discovery directory (`GET
/api/userInfo?account=…`, no OIDC). chat-frontend owns the Keycloak login and
calls `/api/userInfo` at connect for its `authServiceUrl` / `natsUrl` / `siteId`.

Two goals:

1. **portal-service becomes the SSO front-door** — sign in with Keycloak once,
   get dispatched to the home-site chat-frontend (`baseUrl`) already
   authenticated, no second login.
2. **chat-frontend reverts to static per-site config** — each frontend is
   single-site and knows its own `AUTH_URL` / `NATS_URL` / `SITE_ID`, so it drops
   the `/api/userInfo` call.

## Flow

```text
Browser → portal /login → 302 Keycloak (interactive login — the only one)
        ← 302 portal /auth/callback?code
        portal: verify state, exchange code, read preferred_username,
                cache.Get(account).siteId → sites[siteId].baseUrl
        → 302 {baseUrl}/   (site root, no token in the URL)
chat-frontend → signinRedirect() → Keycloak sees SSO cookie → no prompt (silent re-auth)
        → /oidc-callback → connect() → POST {AUTH_URL}/auth → NATS
```

Redirect to the site **root** (`/`), not `/oidc-callback`: `/` renders
`LoginPage`, which auto-fires `signinRedirect()` — that non-interactive bounce is
the silent re-auth. The **direct path** (opening chat-frontend straight) is
unchanged.

**Why silent re-auth works:** the user authenticates at Keycloak *in the
browser*, setting the realm SSO cookie on the Keycloak origin. portal-service's
code→token exchange is a back-channel call that never touches browser cookies, so
chat-frontend's later top-level `signinRedirect()` finds the session and mints
its own `nats-chat` tokens with no prompt. (Only non-redirect grants would break
this.)

## Scope

| Component | Change |
|---|---|
| **portal-service** | Add `/login` + `/auth/callback` (confidential OIDC client). Keep `/api/userInfo` (caller-less, trimmed to 4 fields), `/healthz`, `/readyz`, cache, registry. |
| **chat-frontend** | Revert to static `AUTH_URL` / `NATS_URL` / `SITE_ID`; drop the `/api/userInfo` call; `user.siteId` from `SITE_ID`. |
| **auth-service** | None. |
| **Keycloak** | Add one confidential `portal` client in the existing realm. |

## portal-service

A confidential OIDC web-app login bolted onto the existing service. Stateless: no
session/token persisted after the redirect; the only state is the in-process
cache plus a transient handshake cookie.

### Endpoints (`routes.go`)

- `GET /login` *(new)* — prod: random `state`+`nonce` into a short-lived
  `HttpOnly`/`Secure`/`SameSite=Lax` cookie (~5 min), 302 to `auth.AuthCodeURL`.
  Dev: 302 straight to the dev-fallback site's `baseUrl`.
- `GET /auth/callback` *(new)* — verify `query.state == cookie.state`;
  `ExchangeAndVerify(code, cookie.nonce)` → account; clear cookie;
  `resolveSite(account)` → 302 to its `baseUrl`.
- `GET /api/userInfo`, `/healthz`, `/readyz` — kept; `/api/userInfo` trimmed (below).

### baseUrl resolution (shared, in-process)

The callback reads the same cache + registry the endpoint does — no HTTP
self-call. Factor the existing `resolve()` core into a helper both use
(`HandleUserInfo` needs the `employee`, the callback needs only `baseUrl`):

```go
func (h *PortalHandler) resolveSite(account string) (employee, siteURL, error) {
    e, ok := h.cache.Get(account)   // hr_employee cache: account -> {siteId, ...}
    if !ok { /* dev fallback / errAccountNotReady */ }
    site, ok := h.sites[e.SiteID]   // registry map: siteId -> {baseUrl}
    if !ok { /* errSiteMissing */ }
    return e, site, nil
}
```

### `/api/userInfo` trim

Response → `{account, employeeId, siteId, baseUrl}`: `account`/`employeeId`/
`siteId` from the `hr_employee` cache entry, `baseUrl` from the registry map
(`PORTAL_SITE_URLS`). `authServiceUrl` and `natsUrl` were served only here, so
their source fields are removed too:

- `userInfoResponse` keeps the four fields; `siteURL`/`parseSiteURLs` → `{baseUrl}` only.
- cache/`employee` drop `NATSURL` (and the `hr_employee` `$lookup` projection);
  `PORTAL_DEV_FALLBACK_NATS_URL` retired.

### Login authenticator (interface, mocked in tests)

```go
// oidclogin.go
type loginAuthenticator interface {
    AuthCodeURL(state, nonce string) string
    // Exchange code, verify ID token (sig/aud/exp/nonce), return account
    // (preferred_username); a blank account is an error.
    ExchangeAndVerify(ctx context.Context, code, expectedNonce string) (string, error)
}
```

Production impl wraps `oauth2.Config` + `*oidc.IDTokenVerifier`, built in
`main.go` (nil in dev). **go-oidc does not check nonce automatically** — the impl
passes `oidc.Nonce(expectedNonce)` (or compares `idToken.Nonce`), and a test
asserts wrong-nonce rejection.

### Security

- No token stored after the redirect. The handshake cookie holds only random
  `state`/`nonce` (`crypto/rand`), `HttpOnly`/`Secure`/`SameSite=Lax`, cleared on
  callback. `state` defends CSRF; `nonce` defends replay.
- Client secret from env, never defaulted/logged. The `baseUrl` redirect carries
  no token.

### Browser-facing errors (HTML, not the JSON `errcode` envelope)

- State mismatch / exchange / verify failure → 302 `/login`.
- Account not provisioned (prod) → 403 HTML; copy hard-coded in the template (it
  is not in `pkg/errcode` — only the `account_not_ready` reason string is); tag
  the log with the reason constant.
- Site missing from registry / internal → 500 HTML, logged once (no token/PII).

### Dev mode

`DEV_MODE=true`: no OIDC, `loginAuthenticator` nil, `/login` → dev-fallback site's
`baseUrl`. Read `sites[devFallbackSiteID]` comma-ok (missing → 500, never an empty
redirect) and validate its presence at startup.

### Config (`caarlos0/env`)

Keycloak fields use the **same env names** as the rest of the stack (the
codebase convention auth-service set): `OIDC_ISSUER_URL` / `OIDC_CLIENT_ID` /
`OIDC_CLIENT_SECRET` / `OIDC_REDIRECT_URL` / `OIDC_SCOPES` / `TLS_SKIP_VERIFY`.
`OIDC_ISSUER_URL` (and `TLS_SKIP_VERIFY`) carry the **same values** as
auth-service, and the issuer is the **same realm** (`chatapp`) chat-frontend
uses — that shared realm is what gives the `portal` and `nats-chat` clients one
SSO session. `OIDC_CLIENT_ID`/secret are the portal's own client (the frontend's
is `nats-chat`). The account is `preferred_username`, the same claim both use.

| Env | Required | Notes |
|---|---|---|
| `OIDC_ISSUER_URL` | prod | same value/realm as auth-service + chat-frontend: `chatapp` (e.g. `http://localhost:8180/realms/chatapp`) |
| `OIDC_CLIENT_ID` | prod | the portal's confidential client (frontend's is `nats-chat`) |
| `OIDC_CLIENT_SECRET` | prod | portal client secret; never defaulted/logged |
| `OIDC_REDIRECT_URL` | prod | absolute `/auth/callback` URL |
| `OIDC_SCOPES` | no (`openid profile email`) | matches chat-frontend |
| `PORTAL_COOKIE_SECURE` | no (`true`) | `false` only for local http |
| `TLS_SKIP_VERIFY` | no (`false`) | dev/staging only; same as auth-service |

OIDC fields validate in `run()` when `DEV_MODE=false` (can't be unconditionally
`required`). `PORTAL_DEV_FALLBACK_NATS_URL` is removed.

## chat-frontend

Static per-site config, no runtime discovery. The login flow is unchanged
(auto-`signinRedirect()` → `OidcCallback` → `connect()`); only `connect()`'s
post-token steps change.

- `runtimeConfig.js`: drop `PORTAL_URL`; add `AUTH_URL` / `NATS_URL` / `SITE_ID`
  (local defaults `http://localhost:8080`, `ws://localhost:9222`, `site-local`).
- `NatsContext.connect()`: drop the `/api/userInfo` fetch → `POST {AUTH_URL}/auth`
  → `natsConnect({servers: NATS_URL})` → `setUser({...userInfo, siteId: SITE_ID})`.
- `user.siteId = SITE_ID` keeps the federated room ops working
  (`fetchMessageHistory`, `markRoomRead`, `fetchSurroundingMessages`, `ensureKey`).
- `useJwtRefresh` re-mints against the static `AUTH_URL`.
- Deploy: `config.js.template`, `30-render-config.sh`, `docker-compose.yml`,
  `docker-local/setup.sh` swap `PORTAL_URL` → `AUTH_URL`/`NATS_URL`/`SITE_ID`.

## Keycloak

New confidential `portal` client in the **same `chatapp` realm** as `nats-chat`,
modeled on it (protocol `openid-connect`, `standardFlowEnabled`) but confidential
(`publicClient: false`, a secret, `redirectUris: [OIDC_REDIRECT_URL]`).
`nats-chat` stays as-is. Same realm → one shared SSO session → silent re-auth.
Add the `portal` client to `realm-export.json` for local.

**Multi-site (load-bearing):** chat-frontend's `redirect_uri` is
`${origin}/oidc-callback` on the **`nats-chat`** client, so each home-site origin
must be a registered redirect URI / Web Origin on `nats-chat` (not just `portal`),
or Keycloak rejects the post-dispatch `signinRedirect()`. Ops/IaC-owned; the realm
export has only localhost today.

## Dependencies

No new module: `golang.org/x/oauth2` is in the graph (`// indirect` → direct on
`go mod tidy`); `github.com/coreos/go-oidc/v3` is already direct.

## Testing (TDD, ≥80%)

- **Go (`handler_test.go`, mocked `loginAuthenticator`, cache populated directly):**
  `/login` prod cookie+redirect; `/login` dev redirect + missing-dev-site → 500;
  `/auth/callback` happy path; state mismatch / exchange error → `/login`;
  not-provisioned → 403 HTML; site-missing → 500 HTML; `/api/userInfo` asserts the
  trimmed shape; `parseSiteURLs` for `{baseUrl}`-only.
- **`oidclogin_test.go`:** `AuthCodeURL` carries state+nonce (pure);
  `ExchangeAndVerify` via an `httptest` fake issuer (signed ID token + JWKS), incl.
  wrong-nonce rejection and custom-`httpClient` propagation; `newOIDCLogin` covered
  by `TestNewOIDCLogin` against an `httptest` discovery endpoint.
- **vitest:** `runtimeConfig` resolves the new vars; `connect()` skips
  `/api/userInfo`, hits `AUTH_URL`, dials `NATS_URL`, sets `siteId = SITE_ID`;
  `useJwtRefresh` uses static `AUTH_URL`.

## Files

- **portal-service:** `main.go`, `handler.go`, `routes.go`, `store.go` (drop
  `NATSURL`), `store_mongo.go` (drop projection), `oidclogin.go` *(new)*,
  `*_test.go` + `mock_oidclogin_test.go`, `deploy/docker-compose.yml`.
- **chat-frontend:** `runtimeConfig.js`, `NatsContext.jsx`, `useJwtRefresh.js`
  (+ tests); `deploy/config.js.template`, `deploy/30-render-config.sh`,
  `deploy/docker-compose.yml`, `docker-local/setup.sh`.
- **Keycloak/docs:** `auth-service/deploy/keycloak/realm-export.json` (+ `portal`
  client); `docs/client-api.md` (note the SSO front-door; trim §2.3 to 4 fields;
  drop the `/api/userInfo` step from §2.1).

## Out of scope

auth-service changes; removing `/api/userInfo` (retained, trimmed); iframe
`prompt=none`; deep-link preservation; a live-Keycloak integration test; prod
realm wiring / redirect URIs.
