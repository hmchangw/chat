# Bot Platform NextGen — Auth · Part 3: Components & Integration Guide

> **Master spec, Part 3 of 3.** Integration-focused companion to **[Part 1 — Architecture & Requirements](./bot-platform-nextgen-auth-part1-requirements.md)** and **[Part 2 — Technical Design](./bot-platform-nextgen-auth.md)**. This part maps the bot-platform **components**, what we build vs. what already exists, and the **integration points** between them.
>
> **Scope/timeline:** External devs are building the bot-platform stack; target **July (integration only, not a full re-architecture)**. This doc covers how our auth service slots into the existing components.

---

## 1. Naming
- **`botplatform-service`** — *the new service we build* (Part 2 calls it `bot-gateway`; same thing). Auth + web UI + token validation + API proxy.
- **`botplatform-server` :8080** — *existing* service: API routing, message posting, room management (and the NATS bridge to the nextgen backend). We proxy to it **after auth**.
- **websocket server :8899** — *existing*: real-time bot connections; **calls our service to authenticate**.
- **event consumer** — *existing*: NATS → webhook delivery.

> ⚠️ `botplatform-service` (new, ours) vs `botplatform-server` (existing) are one letter apart — keep them straight.

---

## 2. Component map — build vs. exists

| Component | Owner | Role |
|---|---|---|
| **Login web pages** (`/dev-login`, `/changepwd`) | **we build** | HTML forms + submit |
| **Token validation** (Valkey + Mongo) | **we build** | dual-token, cache-fronted |
| **New token issuance** | **we build** | our store issues session tokens |
| **API proxy / routing** | **we build** | validate → forward to `botplatform-server:8080` |
| Room / message APIs (`/api/v2/*`) | exists | `botplatform-server:8080` |
| Webhook delivery (event consumer) | exists | NATS → webhook |
| WebSocket transport + logic | exists | websocket server :8899 (**needs an auth hook**) |

**Net:** we own **auth + web UI + token validation + proxy**; the data plane (rooms/messages), webhooks, and the WS transport already exist — we plug auth into them.

---

## 3. The new service — `botplatform-service`

Endpoints:
- `GET/POST /dev-login` — HTML login form + submit (session cookie, CSRF).
- `POST /v1/bot/login` — new bot login API *(or the same legacy path, depending on the Istio `VirtualService` routing we choose — see §6 / Q11)*.
- `GET/POST /changepwd` — HTML password change + API.
- `POST /v1/auth/validate` — **token-validation endpoint the websocket server calls** (§4.2).
- `/api/v2/*` (and legacy `/api/v1/*` as needed) — **authenticated proxy** to `botplatform-server:8080` (§4.1).
- Token validation backed by **Valkey cache + Mongo**.

---

## 4. Critical integration points

### 4.1 API routing (all `/api/v2/*` calls)
Flow when a bot calls `GET /api/v2/rooms`:
1. Request hits `botplatform-service` with headers `X-User-Id` + `X-Auth-Token`.
2. **Validate the token** (Valkey → Mongo → legacy fallback, §4.3).
3. **Forward** to `http://botplatform-server:8080/api/v2/rooms` (principal carried in headers).
4. **Return** the response to the bot.

Config: `BOTPLATFORM_SERVER_URL=http://botplatform-server:8080`.

> **Reconciliation with Part 2 §9:** the data-plane downstream is **HTTP to `botplatform-server:8080`**, *not* a direct REST→NATS bridge in our service. `botplatform-server` owns the room/message logic and any NATS work. Our service is **auth + reverse-proxy + principal-header injection**.

### 4.2 WebSocket auth (security fix required)
Today the WS connection is unauthenticated. Required flow:
1. Bot opens a WS connection and sends `{ userId, authToken }` (e.g. `ws.send(JSON.stringify({ userId, authToken }))`).
2. The **websocket server calls our service** to validate before accepting:
   `POST http://botplatform-service:8080/v1/auth/validate` with `{ userId, authToken }`.
3. Our service validates (same dual-token logic) and returns accept/reject (+ principal).
4. WS server **accepts or rejects** the connection accordingly.

Validation happens **once per connection** (not per frame), so an HTTP hop is cheap; the WS server caches the result for the connection lifetime.

### 4.3 Token compatibility (migration support)
| Phase | Web login | API validation |
|---|---|---|
| **1** | bot → `/dev-login` → **legacy v2 code** | `botplatform-server` validates against legacy v2 |
| **2 (hybrid)** | bot → `/dev-login` → **our service** (issues new token) | our service validates **new tokens + also accepts old** |
| **3** | bot → `/dev-login` → **our service only** | our service (new tokens); **legacy code off** |

**Validation logic (hybrid phase):**
```
look up token in OUR store (Valkey→Mongo)   → if found, validate
else fall back to legacy validation          → (call legacy code / legacy store)
keep both working through the transition
```

---

## 5. Endpoint transition (current → after our deploy)

| Endpoint | Current | After our deploy |
|---|---|---|
| Bot login (web) | legacy `/dev-login` | **our** `/dev-login` |
| Bot login (API) | legacy `/api/v1/login` | **our** `/v1/bot/login` (and/or `/api/v1/login`, Q11) |
| API gateway path | Istio → `botplatform-server` | Istio → **our service** → `botplatform-server` |
| WebSocket | **not authenticated** | **our service auth** → websocket server |

---

## 6. Data-flow summary
- **`/dev-login`** → our service: show HTML form, validate (phase-appropriate), issue new token + set cookie.
- **`/api/v2/*`** → our service: validate token (Valkey/Mongo + legacy fallback) → proxy to `botplatform-server:8080` → room/message logic runs there.
- **WebSocket** → websocket server :8899: sends auth to our `/v1/auth/validate` → accept/reject.

---

## 7. Resolved integration decisions

### Q10 — Is the new token format the same as legacy, or different?
**Recommendation: same opaque wire format** (a random string in `X-Auth-Token`, stored as `base64(sha256(token))`). Bots and the WS server treat the token as opaque, so keeping the shape identical means **zero client/WS changes** and the hybrid validator just does *our-store-first, legacy-fallback* (no format sniffing). *Optional fast-path:* a fixed namespace prefix on new tokens (e.g. `bp1_…`) lets the validator skip the legacy lookup for known-new tokens — adopt only if profiling shows the fallback double-lookup matters. **Default: same format, store-based routing.**

### Q11 — `/api/v1` support — login only, or other endpoints too?
**Recommendation: `/api/v1/login` only** (a backward-compat login shim), with `/v1/bot/login` as the new path; route **all data calls via `/api/v2/*` → `botplatform-server`**. *Open:* confirm whether any existing bots still call **non-login** `/api/v1/*` data endpoints — if so, those must be proxied/mapped too. Whether the path is `/v1/bot/login` vs reusing `/api/v1/login` can be handled at the **Istio `VirtualService`** layer (same backend, different route), so bots needn't change URLs.

### Q12 — WebSocket auth: call our HTTP endpoint, or read shared Valkey directly?
**Recommendation: call our HTTP `/v1/auth/validate` endpoint** (not direct Valkey). Reasons:
- **Single source of truth** — dual-token/legacy-fallback, cache-miss→Mongo, lockout, and sliding-expiry logic live in *one* place; direct Valkey access would force the WS server to reimplement all of it and couple to our cache-key schema.
- **No false rejects** — a Valkey-only read fails on cache miss (valid-in-Mongo, not-yet-cached); our endpoint handles the miss correctly.
- **Cost is negligible** — WS auth is **once per connection** (long-lived), not per message, so the <5 ms per-API-call budget doesn't apply. Keep the call mesh-internal (Istio mTLS) and cache the result for the connection lifetime.

Use direct-Valkey only if per-connection latency ever becomes a measured problem (it won't, at once-per-connect).

---

## 8. Month-end (July) deliverables
- **Must:** `/dev-login` web + login APIs, token validation (Valkey+Mongo), `/api/v2/*` proxy, `/v1/auth/validate` for WS.
- **Nice-to-have:** `/changepwd` UI, WebSocket auth integration.
- **August:** Phase-3 legacy sunset.

---

## 9. Cross-references
- Service internals (data model, hashing, sessions, performance SLAs, config, tests): **Part 2**.
- Business requirements, architecture decision (Option B), security, rollout: **Part 1**.
- WebSocket handshake details and the `botplatform-server` API surface are **owned by the external-dev track**; this guide defines only the **auth contract** between them and us.
