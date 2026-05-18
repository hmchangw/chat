# auth-load scenario endpoints

Audit result for auth-service (commit dbde60b15bc6035d562422d9f9335474475e8ee7, run 2026-05-18):

| Method | Path      | Body                                                              | Purpose                    |
|--------|-----------|-------------------------------------------------------------------|----------------------------|
| POST   | /auth     | `{"ssoToken":"...","natsPublicKey":"..."}`                        | Issue NATS JWT (prod mode) |
| POST   | /auth     | `{"account":"...","natsPublicKey":"..."}` (DEV_MODE=true)        | Issue NATS JWT (dev mode)  |
| GET    | /healthz  | (none)                                                            | Health check               |

**Notes vs spec:**

The spec listed `POST /login`, `POST /refresh`, `GET /validate` as the expected endpoint set.
The actual auth-service does not match:

- There is **no** `/login`, `/refresh`, or `/validate` endpoint.
- The single auth endpoint is `POST /auth`, which both issues and implicitly validates.
- Auth-service issues NATS JWTs (not HTTP Bearer JWTs); the "refresh" concept does not exist
  because clients hold a signed NATS credential — expiry triggers a re-auth via `POST /auth`.
- `/healthz` is available for health probing.

The `auth-load` scenario and `doAuth*` helpers are written against the actual endpoints.
`doAuthLogin` → `POST /auth` (dev mode: `account` + `natsPublicKey`).
`doAuthValidate` → `GET /healthz` (stand-in probe; full NATS-JWT validation is SUT-internal).
`doAuthRefresh` → `POST /auth` (re-auth, same path as login — NATS JWT refresh pattern).
