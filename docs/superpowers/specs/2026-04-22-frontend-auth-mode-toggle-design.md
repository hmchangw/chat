# Frontend Auth Mode Toggle — Design

**Date:** 2026-04-22
**Scope:** `chat-frontend/` only. No backend changes.

## Problem

The `chat-frontend` test UI only supports the auth-service's dev-mode login flow. It collects an account name and posts `{account, natsPublicKey}` to `POST /auth`. When the auth-service runs with `DEV_MODE=false`, that endpoint expects `{ssoToken, natsPublicKey}` instead, so the frontend cannot log in against a production-configured auth-service.

## Goal

Add a build-time toggle in `chat-frontend` that switches the login flow between:

1. **Dev mode** — current behavior; account-name login, posts `{account, natsPublicKey}`.
2. **OIDC mode** — new Keycloak SSO flow; posts `{ssoToken, natsPublicKey}`.

The operator picks whichever mode matches the backend's `DEV_MODE` setting when building or starting the dev server. No runtime mode switch, no backend changes.

## Non-Goals

- No changes to `auth-service`, its endpoints, or its config.
- No changes to NATS connection logic once the NATS JWT is obtained.
- No support for OIDC providers other than Keycloak. (The implementation uses standards-based OAuth2 Authorization Code + PKCE, so other providers would likely work, but only Keycloak is tested.)
- No token refresh handling beyond what `oidc-client-ts` does by default; the NATS JWT already expires on its own schedule.

## Environment Variables

Added to `chat-frontend`:

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `VITE_AUTH_MODE` | no | `dev` | `dev` or `oidc`. Anything else is a config error. |
| `VITE_OIDC_AUTHORITY` | when `oidc` | — | Keycloak realm URL, e.g. `https://keycloak.example.com/realms/chat`. |
| `VITE_OIDC_CLIENT_ID` | when `oidc` | — | Keycloak client ID configured as a public SPA client. |

Existing `VITE_AUTH_URL`, `VITE_NATS_URL`, and `VITE_DEFAULT_SITE_ID` are unchanged.

## Dependency

Add `oidc-client-ts` (the maintained successor to `oidc-client`). It handles Authorization Code + PKCE, discovery document lookup, redirect callback parsing, session storage, and silent token refresh. No other new dependencies.

## Component Design

### `src/lib/oidc.js` (new)

Owns the `oidc-client-ts` `UserManager` instance.

- Reads `VITE_OIDC_AUTHORITY` and `VITE_OIDC_CLIENT_ID` at module load.
- Exports:
  - `getUserManager()` — returns a singleton `UserManager`. Throws a config error if the env vars are missing.
  - `signinRedirect()` — starts the redirect to Keycloak.
  - `completeSigninIfCallback()` — if the current URL contains OIDC callback params (`code`, `state`), completes the code exchange and returns the resulting user; otherwise returns `null`. Also clears the callback params from the URL via `history.replaceState` so refreshes don't re-trigger the exchange.
  - `getIdToken()` — returns the current ID token or `null`.

The ID token is sent to the backend as `ssoToken`. The auth-service uses `coreos/go-oidc` and validates against the configured `OIDC_AUDIENCE`; the Keycloak client must be set up so the ID token's `aud` claim contains that audience. If an access-token-based setup is needed later, swap `user.id_token` for `user.access_token` at the call site — no other change required.

### `src/pages/LoginPage.jsx` (modified)

Branches on `VITE_AUTH_MODE`:

- **`dev`** — unchanged: account-name input, optional site-ID input, submits to `NatsContext.connect({ account, siteId })`.
- **`oidc`** — renders a single "Login with SSO" button plus the optional site-ID input. Clicking the button calls `signinRedirect()` and the browser navigates to Keycloak.

After Keycloak redirects back, the page mounts again and `App.jsx` (or a top-level effect) completes the callback exchange (see below), then triggers `NatsContext.connect({ ssoToken, siteId })`.

### `src/App.jsx` (modified)

On initial mount, when `VITE_AUTH_MODE === 'oidc'`:

1. Call `completeSigninIfCallback()`.
2. If it returns a user, pull `siteId` from `sessionStorage` (stashed before the redirect so it survives the round-trip), then call `NatsContext.connect({ ssoToken: user.id_token, siteId })`.
3. If it returns `null`, render `LoginPage` as normal.

In `dev` mode, `App.jsx` behavior is unchanged.

### `src/context/NatsContext.jsx` (modified)

The `connect` function's signature becomes a discriminated input:

```js
// dev mode
connect({ account, siteId })
// oidc mode
connect({ ssoToken, siteId })
```

Internally it picks the request body shape based on which field is present and posts to the same `POST /auth` URL. The rest of the function — NATS key generation, `jwtAuthenticator`, state updates — is unchanged.

## Data Flow

### Dev mode (unchanged)
1. User enters account name → `LoginPage` → `NatsContext.connect`.
2. Frontend generates NATS key pair.
3. `POST /auth` with `{account, natsPublicKey}`.
4. Receive `{natsJwt, user}`.
5. Connect to NATS using `jwtAuthenticator(natsJwt, seed)`.

### OIDC mode
1. User clicks "Login with SSO" on `LoginPage`.
2. `signinRedirect()` stashes `siteId` in `sessionStorage`, then redirects to Keycloak.
3. Keycloak authenticates the user and redirects back with `?code=...&state=...`.
4. `App.jsx` on mount calls `completeSigninIfCallback()`; `oidc-client-ts` exchanges the code for tokens and clears the URL params.
5. Frontend generates NATS key pair.
6. `POST /auth` with `{ssoToken: user.id_token, natsPublicKey}`.
7. Receive `{natsJwt, user}`.
8. Connect to NATS using `jwtAuthenticator(natsJwt, seed)`.

## Error Handling

- **Missing `VITE_OIDC_AUTHORITY` or `VITE_OIDC_CLIENT_ID` when mode is `oidc`:** `getUserManager()` throws; `LoginPage` surfaces a clear "OIDC config missing" error instead of rendering the login button.
- **Keycloak redirect returns an error** (`?error=...`): `completeSigninIfCallback()` surfaces the Keycloak error message through the existing `NatsContext` error state.
- **`POST /auth` returns 401 with an OIDC token** (token rejected by backend): surface the backend error verbatim, just like the dev path already does.
- **`VITE_AUTH_MODE` set to an invalid value:** treat as a config error at startup — the login page renders an error instead of a form.

## Testing

Existing frontend smoke test (`chat-frontend/smoke-test.mjs`) continues to cover the `dev` path and must still pass unchanged when `VITE_AUTH_MODE` is unset or `dev`.

For the `oidc` path:

- **Unit:** test the body-shape branching in `NatsContext.connect` (given `{account}`, posts dev body; given `{ssoToken}`, posts OIDC body).
- **Unit:** test `LoginPage` conditional rendering based on `VITE_AUTH_MODE`.
- **Unit:** test `completeSigninIfCallback` URL-param clearing logic.
- **Manual:** end-to-end against a local Keycloak is out of scope for automated tests; add a short README section describing how to run the frontend in `oidc` mode against a Keycloak instance.

## Files Changed

| File | Change |
|------|--------|
| `chat-frontend/package.json` | Add `oidc-client-ts` dependency. |
| `chat-frontend/src/lib/oidc.js` | New — `UserManager` singleton and helpers. |
| `chat-frontend/src/pages/LoginPage.jsx` | Branch UI on `VITE_AUTH_MODE`. |
| `chat-frontend/src/App.jsx` | Handle OIDC callback on mount. |
| `chat-frontend/src/context/NatsContext.jsx` | Accept `{account}` or `{ssoToken}` in `connect`. |
| `chat-frontend/README.md` (or equivalent) | Document `VITE_AUTH_MODE` and the OIDC env vars. |
