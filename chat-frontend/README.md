# chat-frontend

Test frontend for the chat system. React 19 + Vite 6.

## Scripts

- `npm run dev` — start the Vite dev server on port 3000
- `npm run build` — produce a production build
- `npm run preview` — preview the production build
- `npm test` — run unit tests with vitest

## Environment Variables

| Name | Default | Required | Description |
|------|---------|----------|-------------|
| `VITE_AUTH_URL` | `""` (proxied to `http://localhost:8080` in dev) | no | Base URL of the auth-service |
| `VITE_NATS_URL` | `ws://localhost:4223` | no | NATS WebSocket URL |
| `VITE_DEFAULT_SITE_ID` | `site-A` | no | Default value for the Site ID input |
| `VITE_AUTH_MODE` | `dev` | no | `dev` (account-name login) or `oidc` (Keycloak SSO) |
| `VITE_OIDC_AUTHORITY` | — | yes when `VITE_AUTH_MODE=oidc` | Keycloak realm URL, e.g. `https://keycloak.example.com/realms/chat` |
| `VITE_OIDC_CLIENT_ID` | — | yes when `VITE_AUTH_MODE=oidc` | Keycloak client ID (public SPA client with PKCE) |

## Auth Modes

### `dev` mode (default)

Matches the auth-service running with `DEV_MODE=true`. The login page collects an account name; the frontend posts `{account, natsPublicKey}` to `POST /auth` and receives a NATS JWT.

### `oidc` mode

Matches the auth-service running with `DEV_MODE=false` and real OIDC configured. The login page shows a single "Login with SSO" button that redirects to Keycloak's login page via OAuth2 Authorization Code + PKCE. After Keycloak redirects back, the frontend exchanges the code for tokens, extracts the ID token, and posts `{ssoToken, natsPublicKey}` to `POST /auth`.

To run against a local Keycloak:

1. Configure a Keycloak realm with a public SPA client:
   - Valid redirect URI: `http://localhost:3000/`
   - Web origin: `http://localhost:3000`
   - Standard flow (Authorization Code + PKCE) enabled
2. Start the auth-service with `DEV_MODE=false`, `OIDC_ISSUER_URL=<realm URL>`, `OIDC_AUDIENCE=<client-id>`.
3. Start the frontend:
   ```bash
   VITE_AUTH_MODE=oidc \
     VITE_OIDC_AUTHORITY=https://<keycloak-host>/realms/<realm> \
     VITE_OIDC_CLIENT_ID=<client-id> \
     npm run dev
   ```
