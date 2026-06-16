# Bot Platform NextGen — Auth · Part 1: Architecture Decisions & Business Requirements

> **Master spec, Part 1.** This document covers the *why*, the *what*, the architecture decision, and the rollout. The **technical design** (data model, algorithms, NATS subjects, config, tests) lives in **[Part 2 — Technical Design](./bot-platform-nextgen-auth.md)**. A **Part 3 — Bot Platform Components Guide** is planned (to be provided).
>
> **Status:** for review. One architecture decision (Option A vs B) is pending sign-off; see §3 and Part 2 §12.

---

## 1. Executive summary

**What:** Build password-based authentication for **bots and admins**, migrating from the legacy v2 repo to the nextgen chat backend.

**Why:**
- The legacy system uses a **capped session array (50-token limit)** per user — both a scaling ceiling and an O(n) validation cost.
- The new system supports **unlimited sessions with O(1) lookup** (one document per session, keyed by token hash).
- It enables **better admin controls**: create bot, rotate password, list/revoke sessions.

**Key constraint:** existing bots using the bot SDK **must keep working with zero code changes** — same URL, same credentials, same request/response contract.

---

## 2. Document map
- **Part 1 (this doc)** — executive summary, architecture decision, business requirements (user stories), constraints, security, success criteria, rollout plan, scope.
- **[Part 2 — Technical Design](./bot-platform-nextgen-auth.md)** — `credentials`/`sessions` data model, hashing/verify algorithms, login & validation flows, NATS subjects, gateway topology & performance design, configuration, test plan, verification checklist, open questions.
- **[Part 3 — Components & Integration Guide](./bot-platform-nextgen-auth-part3-components.md)** — the bot-platform components (`botplatform-server`, websocket server, event consumer), what we build vs. what exists, and the integration points (API proxy, WebSocket auth, token-compatibility phases).

---

## 3. Architecture decision — DECIDED: Option B (new `bot-gateway` service)

Where do bot auth + the REST edge live? Two options were weighed (full breakdown in **Part 2 §7**):

- **Option A — Extend `auth-service`.** Add password login + stores + middleware + admin RPCs to the existing auth service.
- **Option B — New `bot-gateway` service. ✅ SELECTED (design-review decision, 2026-06-15).** A dedicated service for bot password auth + REST→NATS translation + admin ops; `auth-service` stays pure-SSO.

| Criterion | Option A | Option B |
|---|---|---|
| Time to implement | faster | slower |
| Operational complexity | lower | higher |
| Separation of concerns | poor | **good** |
| Risk to human auth | higher | **lower** |
| Long-term maintainability | moderate | **better** |
| Team ownership | single owner | **split ownership** |

**Why B, despite A being faster** (the scope crossed a threshold — it is now **more than a JSON API**):
- **Blast radius / key safety:** `auth-service` holds the JWT signing key and does human SSO; a browser-facing web UI (HTML, cookies, CSRF) is a much larger attack surface that must not share a process with the signing key. Process isolation > file separation.
- **Signing key stays put:** `bot-gateway` never holds the key — it calls `auth-service` to mint JWTs (and only for *native* bot logins; REST bots use the gateway's service-account connection, no per-bot JWT).
- **Independent scaling & deploy:** bot load (10k logins/min, 1M validations/min) and the 1-week bot canary are decoupled from human SSO.
- **Clean sunset:** legacy bot auth (Phase 5) is far easier to retire as a standalone service.

A would have shipped faster but mixes a web app's security model into the human-auth signer. *(Note: A was the right call for the original narrow scope; the web-UI + dual-token additions are what tip it to B.)*

---

## 3a. Interfaces & endpoint paths

`botplatform-service` is the **auth provider** — it does **not** proxy `/api/v2/*` (the existing **ApiGW** routes that to `Server`, calling our validate endpoint for auth). All endpoints are **REST** (Q15):

| Surface | Path | Method | Returns | Auth |
|---|---|---|---|---|
| Web — login form/submit | `/dev-login` | GET/POST | **HTML** / redirect + **cookie** | CSRF (POST) |
| Web — change-pwd | `/changepwd` | GET/POST | **HTML** / redirect | session cookie + CSRF |
| API — legacy bot login | `/api/v1/login` | POST | **JSON** (`authToken`,`userId`,`me`) | — |
| API — new bot login | `/v1/bot/login` | POST | **JSON** (new token) | — |
| API — token validation | `/v1/auth/validate` | POST | **JSON** (`valid`,principal) | `{userId,authToken}` |
| Admin | `/v1/admin/bots…` | POST/GET/DELETE | JSON | admin session |

- **Web routes** use **session cookies** (HttpOnly/Secure/SameSite) + **CSRF**.
- **`/v1/auth/validate`** is called by **ApiGW, the WebSocket server, and EventConsumer** to authenticate bot traffic (replacing legacy-proxy validation).
- **`/api/v1/login`** preserves the legacy contract verbatim; **`/v1/bot/login`** is the new path. Both via Istio at the same hostnames so bots don't change URLs.

---

## 4. Business requirements — user stories

### US1 — Bot login
*As a bot, I want to log in with username/password to get an auth token.*
- Accept `{ user, password }` (plaintext or RC digest form).
- Return `authToken`, `userId`, `me`.
- **Performance: P99 < 200 ms** (stretch goal < 100 ms).
- **Must match the legacy response format exactly.**

### US2 — Session-based API access
*As an authenticated bot, I want to make API calls using my token.*
- Headers: `X-Auth-Token` + `X-User-Id`.
- **No session limits** — replaces the legacy 50-cap.
- Validation latency: **< 5 ms cached, < 50 ms uncached**.
- Support **1,000,000 validations / minute**.

### US3 — Long-lived sessions
*As a bot, I want my sessions to stay valid while I'm active.*
- No expiry while active.
- Auto-expire after **180 days idle**.
- Extend on each use (**sliding window**).

### US4 — Admin bot creation
*As an admin, I want to create new bot accounts.*
- Set a temporary password.
- Force password change on first login.
- **Only admins** can create.

### US5 — Password rotation
*As an admin, I want to reset bot passwords.*
- Change the password immediately.
- **Revoke all existing sessions.**
- Force re-login.

### US6 — Session management
*As an admin, I want to see and revoke bot sessions.*
- List all active sessions.
- Show last-used time.
- Revoke an individual session or all of them.

### US7 — Web login (browser)
*As an admin/developer, I want to log in through a web page.*
- `GET /dev-login` serves an **HTML** form; `POST /dev-login` submits it.
- On success, set a **session cookie** (HttpOnly/Secure/SameSite) and redirect.
- **CSRF-protected.**

### US8 — Web change-password (browser)
*As a logged-in user, I want to change my password through a web page.*
- `GET /changepwd` serves an **HTML** form; `POST /changepwd` submits it.
- Requires a valid session cookie + **CSRF** token.
- On success, **revoke other sessions** and force re-login (consistent with US5).

---

## 5. Critical constraints
- **User IDs:** 17-char Meteor format (**not** UUID).
- **Passwords:** `bcrypt(sha256_hex(plaintext))`, **cost = 10**.
- **Tokens:** stored as `base64(sha256(rawToken))`.
- **IDs must be preserved from legacy** — no remapping layer (the v2 Go repo already preserves the 17-char `_id`).

---

## 6. Migration
- **Import password hashes verbatim** — never rehash or recompute (we don't hold the plaintext).
- **Import active login tokens only.**
- **Skip personal access tokens** (`type:"personalAccessToken"`) — not used by bots.
- **Zero bot code changes required.**

**Dual-token validation (during migration):**
- **Accept old Rocket.Chat tokens** (imported, validated against the same store).
- **Issue new botplatform tokens** on every fresh login.
- **Gradually phase out old tokens** — as bots re-login they get new tokens; legacy tokens age out via the 180-day sliding window and can eventually be rejected outright once telemetry shows none in use.

---

## 7. Security
- **Never log** tokens or passwords (or their hashes).
- **Timing-safe credential comparison** (run bcrypt even on unknown accounts; uniform error/timing — no account enumeration).
- **Rate limiting:** 5 failed attempts → **15-minute lockout**.
- **HTTPS only.**
- **CSRF protection on all web (form) routes** (`/dev-login`, `/changepwd`); API/token routes are exempt (no ambient cookie credential).
- **Session cookies for web** — HttpOnly, Secure, SameSite — distinct from API bearer tokens. Both resolve to the same session store.

---

## 8. Success criteria

**Performance**
- Login **P99 < 200 ms** (ideally < 100 ms).
- Token validation **P99 < 5 ms cached**.
- **Cache hit ratio > 95%**.
- Sustain **10k logins/min, 1M validations/min**.

**Migration**
- **Zero downtime** via Istio canary.
- **1% → 100% traffic over ~1 week**.
- **Rollback within 1 hour.**
- **Zero data loss.**

---

## 9. Migration plan (phases)

**Phase 1 — Foundation**
- Deploy the auth service with the login API.
- Create the session store.
- Build the migration script.

**Phase 2 — Integration**
- Update the API gateway with token validation.
- Test bot-platform server routing.
- Fix WebSocket authentication.

**Phase 3 — Data migration**
- Freeze legacy auth.
- Run migration (dry-run + live).
- Verify counts.

**Phase 4 — Cutover**
- Istio canary 1% → 10% → 50% → 100%.
- Monitor 24h.
- Dashboard gate: error rate **< 0.1%**.

**Phase 5 — Cleanup**
- Monitor stability.
- Sunset legacy auth (v2 Go repo auth, and possibly v1 auth).
- Update documentation.

---

## 10. Out of scope
- Human SSO/OIDC (unchanged; stays in `auth-service`).
- Bot permissions (separate system).
- Message routing (separate).
- Admin UI (next phase).
- **Personal access tokens** — bots don't use them. **Recommendation: do not support in this phase** — the session model already covers every bot need (login, long-lived tokens, unlimited sessions). PATs are a human-user feature with no bot benefit here; revisit only if/when human REST API access moves to the nextgen stack.
