# Bot Platform NextGen — Auth · Part 3: Components & Integration Guide

> **Master spec, Part 3 of 3.** Integration-focused companion to **[Part 1 — Architecture & Requirements](./bot-platform-nextgen-auth-part1-requirements.md)** and **[Part 2 — Technical Design](./bot-platform-nextgen-auth.md)**. This part maps the bot-platform **components**, what we build vs. what already exists, and the **integration points** between them.
>
> **Scope/timeline:** External devs are building the bot-platform stack; target **July (integration only, not a full re-architecture)**. This doc covers how our auth service slots into the existing components.

---

## 1. Naming
- **`botplatform-service`** — *the new service we build* (Part 2 calls it `bot-gateway`; same thing). Auth + web UI + token validation + API proxy.
- **`botplatform-server` :8080** — *existing* service (external-dev track): serves `/api/v2/*`. Today it reverse-proxies to the **legacy v2 REST code**; as the data plane migrates it will **bridge REST→NATS** to the nextgen backend (Q13). We proxy to it **after auth**, transparently.
- **websocket server :8899** — *existing*: real-time bot connections; **calls our service to authenticate**.
- **event consumer** — *existing*: NATS → webhook delivery.

> ⚠️ `botplatform-service` (new, ours) vs `botplatform-server` (existing) are one letter apart — keep them straight.

---

## 2. Component map — build vs. exists

| Component | Owner | Role |
|---|---|---|
| **Login web pages** (`/dev-login`, `/changepwd`) | **we build** | HTML forms + submit |
| **Token validation** (`/v1/auth/validate`, Valkey + Mongo) | **we build** | dual-token, cache-fronted authority |
| **New token issuance** (login) | **we build** | our store issues session tokens |
| **Admin REST** (`/v1/admin/bots…`) | **we build** | provision / rotate / revoke |
| **ApiGW** (routing, rate-limit, metrics) | **exists** | **calls our `/v1/auth/validate`**, routes to `Server` |
| Room / message APIs (`/api/v2/*`) | exists | `Server` (legacy v2 REST today) |
| Webhook delivery (event consumer) | exists | NATS → webhook; **calls our validate** |
| WebSocket transport + logic | exists | websocket server :8899 (**calls our validate**) |

**Net:** we own **auth** (login + validate + stores + web UI + admin). The **data plane is ApiGW → Server** (existing); we are **not** in it — every component just calls our `/v1/auth/validate` for auth (Q17).

---

## 3. The new service — `botplatform-service` (auth provider)

Endpoints (all REST):
- `GET/POST /dev-login` — HTML login form + submit (session cookie, CSRF).
- `POST /api/v1/login` (legacy contract) + `POST /v1/bot/login` (new) — issue tokens.
- `GET/POST /changepwd` — HTML password change.
- `POST /v1/auth/validate` — **the dual-token validation authority** called by ApiGW / WS / EventConsumer (§4.1–§4.2).
- `/v1/admin/bots…` — admin REST (provision / rotate / sessions).
- Backed by **Valkey cache + Mongo** (`credentials` + `sessions`).

**Not here:** the `/api/v2/*` proxy (ApiGW owns routing → `Server`), and any NATS surface.

---

## 4. Critical integration points

### 4.1 API routing (all `/api/v2/*` calls) — ApiGW validates, then routes
Flow when a bot calls `GET /api/v2/rooms`:
1. Request hits **ApiGW** with `X-User-Id` + `X-Auth-Token`.
2. ApiGW **calls `POST /v1/auth/validate`** on `botplatform-service` (Valkey → Mongo → legacy fallback, §4.3) — *the caching lives behind this API*.
3. On success, ApiGW **routes to `Server`** with the validated principal in headers (`Server` trusts ApiGW via mTLS).
4. ApiGW returns the response to the bot.

> **Reconciliation with Part 2 §9 (Q17):** we are **not** a reverse-proxy. ApiGW (existing) keeps routing/rate-limit/metrics and just delegates auth to our `/v1/auth/validate` — replacing today's slow proxy-to-legacy validation. `Server` serves `/api/v2/*` from the legacy v2 REST code today and will bridge REST→NATS to nextgen later (owned by the data-plane track, Q13) — transparent to us.

### 4.2 WebSocket auth (security fix required)
Today the WS connection is unauthenticated. Required flow:
1. Bot opens a WS connection and sends `{ userId, authToken }` (e.g. `ws.send(JSON.stringify({ userId, authToken }))`).
2. The **websocket server calls our service** to validate before accepting:
   `POST http://botplatform-service:8080/v1/auth/validate` with `{ userId, authToken }`.
3. Our service validates (same dual-token logic) and returns accept/reject (+ principal).
4. WS server **accepts or rejects** the connection accordingly.

Validation happens **once per connection** (not per frame), so an HTTP hop is cheap; the WS server caches the result for the connection lifetime. `/v1/auth/validate` is the **single dual-token authority** (Q14) — the WS server uses it precisely because it isn't behind our HTTP gateway.

### 4.3 Token compatibility (migration support)
| Phase | Web login | API validation |
|---|---|---|
| **1** | bot → `/dev-login` → **legacy v2 code** | `botplatform-server` validates against legacy v2 |
| **2 (hybrid)** | bot → `/dev-login` → **our service** (issues new token) | our service validates **new tokens + also accepts old** |
| **3** | bot → `/dev-login` → **our service only** | our service (new tokens); **legacy code off** |

**Validation logic (hybrid phase) — `/v1/auth/validate`:**
```
look up token in OUR store (Valkey→Mongo)   → if found, validate (legacy or v1 scheme)
else fall back to legacy validation          → (call legacy code / legacy store)
keep both working through the transition
```

**Who validates (Q14).** New `v1` tokens don't exist in the legacy store, so any downstream that re-checks a bearer token must use **our** validator, not its own:
- **WS server** and **legacy v2 if reachable directly** → **call `POST /v1/auth/validate`** (dual-token).
- **Gateway-fronted `/api/v2/*`** → our service already validated once before proxying, so legacy v2 **trusts the injected principal**, enforced by **Istio mTLS service-identity** + the gateway **overwriting** client-supplied `X-User-Id`. This avoids double-validating the hot path.

---

## 5. Endpoint transition (current → after our deploy)

| Endpoint | Current | After our deploy |
|---|---|---|
| Bot login (web) | legacy `/dev-login` | **our** `/dev-login` |
| Bot login (API) | legacy `/api/v1/login` | **our** `/v1/bot/login` (and/or `/api/v1/login`, Q11) |
| API auth validation | ApiGW proxies to legacy `/login` | ApiGW **calls our `/v1/auth/validate`** |
| WebSocket | **not authenticated** | WS server **calls our `/v1/auth/validate`** |

---

## 6. Data-flow summary
- **`/dev-login`** → our service: show HTML form, validate credentials, issue token + set cookie.
- **`/api/v2/*`** → **ApiGW**: call our `/v1/auth/validate` (Valkey/Mongo + legacy fallback) → route to `Server` (with principal) → room/message logic runs there. *(We're not in this path beyond the validate call.)*
- **WebSocket** → websocket server :8899: sends auth → calls our `/v1/auth/validate` → accept/reject.

---

## 7. Resolved integration decisions

### Q10 — Is the new token format the same as legacy, or different?
**Recommendation: same opaque wire format** (a random string in `X-Auth-Token`, stored as `base64(sha256(token))`). Bots and the WS server treat the token as opaque, so keeping the shape identical means **zero client/WS changes** and the hybrid validator just does *our-store-first, legacy-fallback* (no format sniffing). *Optional fast-path:* a fixed namespace prefix on new tokens (e.g. `bp1_…`) lets the validator skip the legacy lookup for known-new tokens — adopt only if profiling shows the fallback double-lookup matters. **Default: same format, store-based routing.**

### Q11 — `/api/v1` support — login only, or other endpoints too? ✅ resolved
**`/api/v1/login` only** (a backward-compat login shim), with `/v1/bot/login` as the new path; **all data calls go via `/api/v2/*` → `botplatform-server`**. The `/api/v2/*` endpoints are the **REST APIs exposed by the legacy v2 code** (the v2 Go repo); `botplatform-server` is itself a reverse proxy that forwards to **`/api/v2/`** (not `/api/v1`). So bots never hit `/api/v1/*` data routes and our service never proxies them. Whether the login path is `/v1/bot/login` vs reusing `/api/v1/login` can be handled at the **Istio `VirtualService`** layer (same backend, different route), so bots needn't change URLs.

### Q12 — WebSocket auth: call our HTTP endpoint, or read shared Valkey directly?
**Recommendation: call our HTTP `/v1/auth/validate` endpoint** (not direct Valkey). Reasons:
- **Single source of truth** — dual-token/legacy-fallback, cache-miss→Mongo, lockout, and sliding-expiry logic live in *one* place; direct Valkey access would force the WS server to reimplement all of it and couple to our cache-key schema.
- **No false rejects** — a Valkey-only read fails on cache miss (valid-in-Mongo, not-yet-cached); our endpoint handles the miss correctly.
- **Cost is negligible** — WS auth is **once per connection** (long-lived), not per message, so the <5 ms per-API-call budget doesn't apply. Keep the call mesh-internal (Istio mTLS) and cache the result for the connection lifetime.

Use direct-Valkey only if per-connection latency ever becomes a measured problem (it won't, at once-per-connect).

### Q13 — Where does the REST→NATS bridge live?
A bridge **is** required: nextgen is all **NATS request/reply RPCs**; the legacy v2 code was **pure REST**. So bot REST `/api/v2/*` calls must, eventually, become NATS RPCs against the nextgen backend. **Recommendation: the bridge lives in `botplatform-server` / the data-plane track — downstream of our auth proxy — never in our auth service.** Our service stays a **transparent HTTP reverse-proxy** that doesn't know whether `botplatform-server` is hitting legacy v2 REST or bridging to nextgen NATS. Putting the bridge in our auth service would couple auth to every `/api/v2/*` subject + request/response schema — the entire data API surface — which is exactly what Option B (§Part 1 §3) avoids. *Confirm ownership with the external-dev team.*

### Q14 — How does the legacy v2 backend (and WS) accept the new tokens?
New `v1` tokens don't exist in the legacy store, so a downstream that re-validates a bearer token would 401 them. **Don't have legacy v2 invent its own check or blindly trust `X-User-Id`** — instead make **`/v1/auth/validate` the single dual-token authority** (it checks legacy + `v1`):
- **WS server** and **legacy v2 if reachable directly** → **call `/v1/auth/validate`**.
- **Gateway-fronted `/api/v2/*`** → our service already validated once; legacy v2 **trusts the injected principal** under **Istio mTLS service-identity** + `X-User-Id` overwrite — so the 1M/min hot path isn't validated twice.

Rule: **no trusted upstream → call validate; mTLS-fronted upstream → trust the injected principal.** *Confirm with the external-dev team* whether legacy v2 calls validate or sits strictly behind the gateway.

---

## 8. Month-end (July) deliverables
- **Must (validation-first, Q16):** session store + token import; **`/v1/auth/validate`** (Valkey+Mongo, dual-token) wired into **ApiGW + WS + EventConsumer**; login APIs (`/api/v1/login`, `/v1/bot/login`).
- **Nice-to-have:** `/dev-login` + `/changepwd` web UI, full WebSocket auth integration.
- **August:** Phase-3 legacy sunset.

---

## 9. Cross-references
- Service internals (data model, hashing, sessions, performance SLAs, config, tests): **Part 2**.
- Business requirements, architecture decision (Option B), security, rollout: **Part 1**.
- WebSocket handshake details and the `botplatform-server` API surface are **owned by the external-dev track**; this guide defines only the **auth contract** between them and us.
