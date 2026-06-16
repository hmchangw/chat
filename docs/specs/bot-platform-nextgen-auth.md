# Spec: Bot Platform NextGen Migration — Password Auth, Sessions & Cutover

> **Master spec, Part 2 of 3** — this is the **Technical Design**. See **[Part 1 — Architecture & Business Requirements](./bot-platform-nextgen-auth-part1-requirements.md)** and **[Part 3 — Components & Integration Guide](./bot-platform-nextgen-auth-part3-components.md)**.
>
> **Status:** DESIGN-COMPLETE — pending verification against the internal (legacy RC fork + nextgen) codebase. §22 is the verification checklist to run before this becomes an implementation plan. Open questions are tracked in §12 (all but two resolved).

*Bring password-based login (admins + bots) and durable session management to the nextgen NATS-native stack, migrating credentials from the legacy Rocket.Chat (RC) Mongo `users` collection without forcing any bot developer to change URLs, credentials, or client code, and cut over behind the shared Istio gateway with zero downtime.*

---

## 1. Goal & non-goals

### Goals
1. **Transparent migration for existing accounts.** Admins and bots that authenticate today via the legacy password endpoint keep the same URL, the same credentials, and the same request/response contract. No password resets, no client changes.
2. **Unlimited concurrent sessions, constant-time validation.** Remove the legacy per-user capped token array; support any number of live sessions per account with O(1) token lookup.
3. **Net-new operator surface.** A NATS-native operator UI (+ its request/reply handlers) for admin login and bot provisioning/management (create bot, set/rotate password, list/revoke sessions).
4. **Zero-downtime cutover** behind the shared Istio gateway, same public URL, new namespace.

### Non-goals (out of scope)
- The exact legacy REST **endpoint surface** and its full subject-mapping table (owned by the gateway track). This spec designs the auth model + the gateway's *responsibilities and topology* (§9), not the per-verb mapping.
- Room/message/federation dual-write consistency during cutover — a separate track (§10.4 flags the boundary only).

---

## 2. Current state (grounded)

Verified against the repo (`auth-service/`, `pkg/userstore`, `pkg/model`, `pkg/subject`):

- **`auth-service` is OIDC/SSO-only.** `POST /auth` (`auth-service/routes.go:5`) validates an SSO token (or a dev account name in dev mode), then signs a **NATS user JWT** with scoped pub/sub permissions using the account signing key (`AUTH_SIGNING_KEY`). See `auth-service/handler.go:234-249` for the grants (`chat.user.{account}.>`, `chat.room.>`, `_INBOX.>`).
- **Clients talk to NATS directly** after `/auth`. There is no HTTP→NATS gateway in the repo; all RPC is NATS request/reply via `pkg/natsrouter`.
- **No password storage, no bcrypt, no session/login-token store exists anywhere.** Clean slate — no legacy auth code in the nextgen repo to refactor around.
- **Identity already works for bots.** `model.User` (`pkg/model/user.go`) carries `Account`, `SiteID`, `Roles`, display names; `model.IsBotAccount` (`pkg/model/account.go`) classifies bots by `*.bot` suffix / `p_` prefix; `pkg/userstore` resolves any account through a pod-local LRU+singleflight cache.
- **JWT minting is reusable** — exactly what a password login needs after credential verification.

### 2.1 Legacy Rocket.Chat reference (verified against RC/Meteor)

The legacy system is Rocket.Chat. Confirmed behavior the migration must honor:

- **Login request** — `POST /api/v1/login` (the deployment's `/dev-login` is a fork-specific route, mechanics identical; exact path/body to confirm, §12 Q1). Body accepts **either** plaintext `{ user, password }` **or** a pre-hashed `{ user, password: { digest: <sha256-hex>, algorithm: "sha-256" } }`. Clients may use either form, so the nextgen login path must accept **both**.
- **Login response** — `{ "status":"success", "data":{ "authToken":"<raw>", "userId":"<17-char>", "me":{ "_id", "username", "name", "active", "roles":["bot"] } } }`. Subsequent calls authenticate with headers **`X-Auth-Token: <raw>`** + **`X-User-Id: <17-char>`**. (Full contract confirmed, §12 Q1.)
- **Password storage** — `users.services.password.bcrypt` = `bcrypt(sha256_hex(password))` (Meteor accounts-password). Verification: hex-SHA-256 the incoming plaintext (or take the client-supplied `digest`), then `bcrypt.CompareHashAndPassword`.
- **Login-token storage** — `users.services.resume.loginTokens[]`, each `{ when, hashedToken }` where **`hashedToken = base64(sha256(rawToken))`** (Meteor `Accounts._hashLoginToken`). The raw token is the `X-Auth-Token`; the server hashes the inbound token and matches.

> Sources: RC REST auth (`developer.rocket.chat`, RocketChat/Rocket.Chat issue #5466), RC password format (forums.rocket.chat "Password format in database"), Meteor `Accounts._hashLoginToken` (Meteor forums / accounts-base). Cited in chat.

**Implication:** identity is solved; we add (a) a credential store, (b) a session store using RC's exact token-hash form, (c) a password-login path that reuses JWT minting, (d) provisioning handlers + UI, (e) the gateway (§9).

### 2.2 Confirmed by internal codebase analysis (2026-06-15)

- **Bots authenticate with password only**, via the Node.js bot SDK calling `POST /api/v1/login`. **No PAT usage among bots** — Personal Access Tokens are a *human-user* feature. ⇒ PATs are **out of scope**; the session model carries no PAT `type`/`name` fields and migration imports no PAT tokens (§6 simplified, Q8 resolved).
- **`users._id` is a 17-char Meteor ID**, and the v2 Go repo **preserves the legacy `_id` verbatim** through identity-sync. ⇒ nextgen `users._id` == legacy `_id` == the `X-User-Id` a bot sends. **No ID-mapping layer, no `LegacyUserID` field** (Q9 resolved).

---

## 3. Key design decisions

| Concern | Decision | Why |
|---|---|---|
| Bot/admin **identity** | Stays in the shared `users` collection (roles distinguish admin/bot/user) | Every downstream service resolves accounts through the cached `userstore`; a second identity collection forces double lookups and breaks display-name/federation resolution. |
| **Credentials** | New `credentials` collection, owned by the bot-auth service (auth-service under §7 Option A, bot-gateway under Option B) | `users` is read-only shared infra cached fleet-wide; a bcrypt hash must not ride in a broadly-cached identity doc, and only the auth service verifies passwords. |
| **Sessions** | New `sessions` collection, **one doc per session keyed by `base64(sha256(token))`** | Eliminates the legacy capped array; O(1) lookup independent of session count; sliding idle expiry (§5.5); per-session + per-account revocation. The hash form matches RC so one hash fn covers legacy + native tokens. |
| **Scope** | Credentials + sessions are **account-agnostic** (admins *and* bots) | The legacy login authenticates admins too — shared password-login infrastructure, not bot-only. |
| **NATS JWT** | Short-lived, **minted from a valid session**, refreshed without re-entering the password | Reuses existing JWT machinery; session = durable login, JWT = ephemeral capability. |
| **Validation hot path** | Mongo durable + **read-through cache (in-pod LRU + Valkey)** | Every REST call validates a token; a Mongo read per call is the bottleneck (§9.3). |

---

## 4. Data model

### 4.1 `credentials` collection
One document per password-login account (admin or bot), keyed to `users._id`.

| Field | Type | Tags | Notes |
|---|---|---|---|
| ID | `string` | `bson:"_id"` | = `users._id`, a **17-char Meteor ID** preserved verbatim from legacy by identity-sync (Q9). **Not** a generated UUIDv7 — these IDs come from the legacy system, not `idgen`. |
| Account | `string` | `bson:"account" json:"account"` | Login username (`username@domain` form). Unique index. |
| PasswordHash | `string` | `bson:"passwordHash" json:"-"` | Migrated verbatim from `services.password.bcrypt`. **Never serialized.** |
| PasswordScheme | `string` | `bson:"passwordScheme" json:"-"` | `"rc-sha256-bcrypt"` = `bcrypt(sha256_hex(pw))`. Verification accepts plaintext **or** RC `{digest,algorithm:"sha-256"}` input (§2.1). |
| RequirePasswordChange | `bool` | `bson:"requirePasswordChange" json:"requirePasswordChange"` | Migrated; drives first-login (§5.4). |
| CreatedAt / UpdatedAt | `int64` | `bson:"createdAt"` / `bson:"updatedAt"` | ms since epoch; `UpdatedAt` bumped on password change. |

Indexes: unique on `account`.

### 4.2 `sessions` collection
One document per live session. **No array, no cap.**

| Field | Type | Tags | Notes |
|---|---|---|---|
| TokenHash | `string` | `bson:"_id"` | **`base64(sha256(rawToken))`** — RC/Meteor form. Lookup key; raw token never stored. |
| UserID | `string` | `bson:"userId" json:"userId"` | = `users._id` (17-char Meteor ID; same id space the bot sends as `X-User-Id` — no mapping). Indexed (revoke-all / list). |
| Account | `string` | `bson:"account" json:"account"` | Denormalized (`username@domain`). **Kept** so validation returns the account directly — the gateway needs it to build `chat.user.{account}.…` subjects and inject the principal, avoiding a second `userstore` lookup per request. |
| Scheme | `string` | `bson:"scheme" json:"scheme"` | `"legacy"` (imported RC tokens, §6.2) or `"v1"` (nextgen-issued). No PAT type — bots are password-only; PATs are a human-user feature, out of scope (§2.2). |
| IssuedAt | `int64` | `bson:"issuedAt" json:"issuedAt"` | ms since epoch. |
| LastUsedAt | `int64` | `bson:"lastUsedAt" json:"lastUsedAt"` | **Throttled** write (§12 Q4) — not every request. |
| ExpiresAt | `*time.Time` | `bson:"expiresAt,omitempty" json:"expiresAt,omitempty"` | **Sliding idle expiry** (§5.5): pushed to `now + idleWindow` on the throttled use-bump. `nil`/absent ⇒ never expires (pure-infinite mode). |

Indexes: `_id` (token hash); `userId`; **partial TTL** on `expiresAt` (`expireAfterSeconds:0`, partial filter `{expiresAt:{$exists:true}}` so null/infinite sessions are never reaped).

**Why this beats the legacy array:** RC embedded a `loginTokens[]` array on the user doc → unbounded doc growth, O(n) scan per request, hence a cap. Per-session docs keyed by hash make validation O(1) regardless of session count, so the cap disappears.

---

## 5. Login & session flows

### 5.1 Password login (admin or bot)
1. Client `POST`s `{ user, password }` (plaintext **or** RC digest form) to **`POST /api/v1/login`** (the confirmed path, §12 Q1).
2. Auth service loads `credentials` by account; derives `sha256_hex(pw)` (or uses the supplied digest); `bcrypt.CompareHashAndPassword` against `PasswordHash`. Constant-time; uniform error on unknown-account vs bad-password.
3. On success: generate a raw token (`crypto/rand`), insert a `sessions` doc (`scheme:"v1"`, `ExpiresAt = now + idleWindow`, §5.5), return the **RC-compatible envelope** (`{ status:"success", data:{ authToken, userId, me } }`) + `X-Auth-Token`/`X-User-Id`.
4. If the request carries a `natsPublicKey` (native client, e.g. operator UI), also mint and return a NATS user JWT (§5.2) so the client can connect to NATS immediately — REST bots omit this field and get the headers only.
5. If `RequirePasswordChange`, signal it (§5.4).

### 5.2 NATS JWT minting from a session
Present a valid session token → validate (§5.3) → mint a short-lived NATS user JWT scoped to `chat.user.{account}.>` (reusing `auth-service` grants). JWT expiry ≪ session expiry; refresh from the still-valid session without the password.

### 5.3 Session validation (every request)
`base64(sha256(token))` → cache lookup → `sessions.findOne({_id})` on miss. Hit + not expired → resolve `userId`/`account`, throttled `LastUsedAt` bump. The same hash matches both `v1` and imported `legacy` rows. TTL index reaps expired docs.

### 5.4 First-login `requirePasswordChange`
Bots can't fill a form, so this is **operator-time**: a provisioned account has `RequirePasswordChange:true`; the operator sets the real password via the change-password handler (§8), which updates `PasswordHash`, clears the flag, and revokes existing sessions.

### 5.5 Session lifetime — effectively infinite (sliding idle)
Sessions do **not** have a fixed TTL. On each (throttled) use-bump, `ExpiresAt` is pushed to `now + idleWindow` (recommend `idleWindow` ≈ 180d), so an actively-used token never expires — infinite for any real bot. Only a token left unused past the window is reaped by the partial TTL index, bounding collection growth and limiting the blast radius of a leaked-then-abandoned token. Revocation (operator action, or rotate-password revoking all of an account's sessions) remains the immediate kill switch. Pure literally-never mode = leave `ExpiresAt` null; then revocation is the only cleanup and the collection grows unbounded.

### 5.6 "Resume" / session reuse
**Legacy REST bots:** no resume verb — a bot logs in once (username + password) and reuses its `X-Auth-Token` header on every call; the header *is* session reuse.

**Internal primitive:** §5.2 — a native client or the REST edge exchanges a still-valid session token for a fresh short-lived NATS JWT, without re-entering the password. Session token = durable resume credential; JWT = ephemeral capability.

**New native bot SDK (re-architecture) — recommended (Q1b):** surface that internal primitive as an explicit **`session.refresh` resume RPC** so a re-architected bot can reconnect / re-mint a JWT from its stored session token after a restart, without storing the password. This is the §5.2 exchange exposed to native clients — cheap (already designed), and it gives new bots a first-class resume path while legacy REST bots are untouched. *Decision pending: expose it now vs. defer to the native-SDK milestone.*

---

## 6. Migration (legacy RC Mongo → nextgen Mongo)

### 6.1 Credential import
For each legacy `users` doc that is an admin or bot account **with a local password** (`services.password.bcrypt`):
- Ensure the nextgen `users` identity row exists (existing identity-sync path, not owned here) — it carries the **same 17-char `_id`**.
- Insert `credentials` with `_id = legacy users._id` (verbatim, 17-char Meteor ID): **copy `services.password.bcrypt` → `PasswordHash` byte-for-byte — do NOT rehash or recompute** (we don't have the plaintext, and the verify path already matches RC's `bcrypt(sha256_hex(pw))`). `PasswordScheme:"rc-sha256-bcrypt"`, legacy force-change flag → `RequirePasswordChange`, timestamps.

### 6.2 Live-session import (no-reauth cutover)
Import **only regular login tokens** from `services.resume.loginTokens[]` — i.e. entries where **`type` is empty/absent**. **Explicitly skip `type:"personalAccessToken"`** (human PATs, out of scope, §2.2). For each imported entry: insert a `sessions` doc with `_id = hashedToken` (already `base64(sha256(token))`, copied verbatim), `UserID = legacy _id`, `Scheme:"legacy"`, and `ExpiresAt = now + idleWindow` (sliding, §5.5 — legacy `when` ignored since sessions are effectively infinite). Because we reuse RC's hash form **and** the same `_id`, an in-flight bot's existing `X-Auth-Token`+`X-User-Id` validate against the imported row unchanged. The next login issues a native `v1` token.

### 6.3 Cutover source-of-truth — freeze legacy, one-time import (decided)
At canary start, make legacy credentials **read-only** (disable password change / admin user-mgmt on the old stack) and run the one-time `credentials` import; nextgen is authoritative from that moment. Frozen-and-matched credentials mean a request landing on either stack authenticates identically (§10.3). A legacy→nextgen credential sync is the fallback **only** if a multi-week dual-run with live password changes is required.

---

## 7. Architecture decision — where bot auth + the REST edge live

Two viable placements. Both implement the *same* responsibilities (§9); they differ only in **which service hosts them**. **DECIDED: Option B** (design-review decision, 2026-06-15) — the bot edge grew into a browser-facing web app (§9.6: HTML forms, CSRF, cookies) plus dual-token validation; isolating that from the JWT-signing, human-SSO `auth-service` wins on **blast-radius/key safety** first and **independent scaling/lifecycle** second. The signing key stays in `auth-service`; `bot-gateway` calls it to mint JWTs (native-bot logins only — not the hot path). Option A (faster, but mixes a web security model into human auth) remains documented below as the considered alternative; it was the right call for the *original* narrow scope.

### Option A — Extend `auth-service` (recommended)
Add password login + the bot REST edge to the existing `auth-service`.

```
auth-service
  existing:  POST /auth              OIDC  -> NATS JWT        (main.go, handler.go)
  new:       POST /api/v1/login      password -> session (+ optional NATS JWT)
             model/credential.go, model/session.go
             store: credential_mongo.go, session_mongo.go
             passwordverify.go       (bcrypt over sha256-hex)
             middleware/bot_auth.go   (X-Auth-Token/X-User-Id validation)
             admin handlers           (NATS RPC for bot ops, §8)
```

**Pros:** single service/deployment; **reuses the existing JWT-minting code**; shared Mongo+Valkey connections; one Helm chart update; clear single-team ownership (auth team).
**Cons:** mixes concerns (SSO + password auth in one service); larger surface area; **risk that bot-auth changes regress human auth**.

### Option B — New `bot-gateway` service (alternative)
A dedicated service for the bot REST API + password auth.

```
Istio ingress
  ├─▶ auth-service:8080   (human OIDC)      POST /auth
  └─▶ bot-gateway:8080    (bot password)    POST /api/v1/login
                                            GET/POST /api/v2/*   (REST -> NATS translation)
                                            admin operations     (NATS RPC)
      bot-gateway owns credentials+sessions (own Mongo+Valkey);
      calls auth-service to mint NATS JWTs.
```

**Pros:** clean separation of concerns; `auth-service` stays pure-SSO; independent scaling (bots vs humans); easier to sunset legacy bot auth later; blast-radius isolation.
**Cons:** two services to maintain; more complex deployment; **service-to-service JWT-minting calls**; duplicated Mongo/Valkey connection logic.

### Decision criteria

| Criterion | Option A (extend auth-service) | Option B (new bot-gateway) |
|---|---|---|
| Time to implement | **faster** | slower |
| Operational complexity | **lower** | higher |
| Separation of concerns | poor | **good** |
| Risk to human auth | higher | **lower** |
| Long-term maintainability | moderate | **better** |
| Team ownership | single owner | split ownership |

**Decision: Option B.** A dedicated `bot-gateway` was chosen because the service is **more than a JSON bridge** — it serves a web UI (§9.6) with CSRF + session cookies and must validate legacy + new tokens, concerns best kept out of the pure-SSO `auth-service`. `bot-gateway` owns `credentials`+`sessions` (its own Mongo+Valkey) and calls `auth-service` to mint NATS JWTs. (Option A would have been faster but mixes those web/auth concerns into human auth.)

> The rest of this spec (stores, flows, §9 responsibilities) is **placement-agnostic** — it holds under either option. Under the chosen Option B they live in `bot-gateway`; the JWT-mint is a service-to-service call to `auth-service`.

---

## 8. Operator UI + NATS request/reply handlers
The operator UI is a **NATS-native frontend** (gets a JWT, issues request/reply) — the confirmed nextgen client pattern. Each op = one `pkg/natsrouter` handler returning a typed `*errcode.Error`, documented in `docs/client-api.md`.

| Operation | Purpose | Writes |
|---|---|---|
| admin login | Human admin password login | `sessions` |
| create bot | Provision bot identity + initial password | `users` + `credentials` (`RequirePasswordChange:true`) |
| set/rotate password | Change password, clear flag | `credentials`, revoke `sessions` |
| list sessions | Show a bot's live sessions | reads `sessions` by `userId` |
| revoke session / revoke all | Kill one/all sessions | `sessions` |

Subjects follow `chat.user.{account}.request.…` via `pkg/subject`. `docs/client-api.md` updated in the same PR (project rule).

---

## 9. `bot-gateway` (a.k.a. `botplatform-service`) — web UI + auth + API proxy

`bot-gateway` (the §7 Option-B service; called **`botplatform-service`** in the integration guide, **Part 3**) is the stateless edge that serves the bot web UI and authenticates bot/admin traffic so bot code never changes.

> **Data-plane reconciliation (Part 3 §4.1).** The data path is **HTTP all the way**: our service validates, then reverse-proxies `/api/v2/*` to **`botplatform-server:8080`**, which in turn reverse-proxies to the **`/api/v2/*` REST APIs exposed by the legacy v2 code** (the v2 Go repo). There is **no REST→NATS bridge and no `/api/v1` in the data plane**. So our service's data-plane job is **validate → reverse-proxy (principal in headers)** over a **pooled HTTP client**; the performance design below (pooled connections, cache-fronted validation, principal injection) applies with the proxy target = `botplatform-server`. Our service uses **NATS only for the control plane** — calling `auth-service` to mint JWTs and the admin provisioning RPCs (§15).

### 9.1 Responsibilities
1. **Web UI (server-rendered HTML)** — `GET/POST /dev-login` and `GET/POST /changepwd` (§9.6); render forms, handle submits, set/clear **session cookies**, enforce **CSRF** on POST.
2. **API proxy** — validate, then reverse-proxy `/api/v2/*` to `botplatform-server:8080` (`BOTPLATFORM_SERVER_URL`), carrying the validated principal in headers. *(`botplatform-server` reverse-proxies `/api/v2/*` to the REST APIs exposed by the legacy v2 code; there is no `/api/v1` data path — Q11.)*
3. **Authentication boundary** — validate the credential against the session store (§9.3): `X-Auth-Token`+`X-User-Id` for API, **session cookie** for web. Also exposes **`POST /v1/auth/validate`** for the websocket server (Part 3 §4.2). **Dual-token aware** — accepts legacy RC tokens (`scheme:"legacy"`) and new botplatform tokens (`scheme:"v1"`), §9.7. Reject with the RC-shaped 401 (API) / redirect-to-login (web).
4. **Principal injection** — attach the validated `account`/`userId` + a request-id (`idgen.GenerateRequestID`) into the NATS request headers so downstream handlers know who is calling.
5. **Connection management** — own a small pool of **long-lived** `*nats.Conn` (never connect-per-request); call `auth-service` to mint a NATS JWT only for native-bot logins.
6. **Deadline & backpressure** — map the HTTP context deadline onto `Conn.RequestMsgWithContext`; apply a concurrency limit / load-shed (503) under saturation.
7. **Error mapping** — turn the `errcode` JSON envelope returned over NATS into the RC-shaped HTTP status/body (API) or a form error (web).
8. **Observability** — propagate request-id, emit per-request metrics + traces (mirrors the HTTP middleware rule in CLAUDE.md).

### 9.2 Recommended topology — two client trust models

```
Legacy REST bot ──HTTP──▶ [Gateway pods] ──NATS (service-acct conn pool)──▶ handlers
                              │ validates session, injects principal
Operator UI / native ──NATS (per-user scoped JWT)──────────────────────────▶ handlers
                              │ JWT pub/sub scoping enforces authz
```

- **Native NATS clients** (operator UI, future native bots): connect directly with a **per-user JWT** scoped to `chat.user.{self}.>`. NATS permissions *are* the authz; no gateway involved.
- **Legacy REST bots**: HTTP → gateway → NATS over a **shared service-account connection** with broad publish rights. The gateway already authenticated the bot (step 2), so it injects the principal and the handler trusts it.

### 9.3 Why this is the most performant design

**(a) Shared service-account connection, not a NATS connection per bot.** The naïve alternative — mint a JWT per bot and open a NATS connection as that user per request — means a TCP+TLS handshake and JWT auth per call (or thousands of pinned connections). Instead the gateway keeps **O(pods)** long-lived connections; the nats.go request mux multiplexes *all* in-flight replies over a single shared `_INBOX` subscription, so concurrency costs almost nothing. Connection count is independent of bot count.

- *Trust boundary:* only the gateway's service account is permitted to publish on behalf of arbitrary accounts; native user JWTs are scoped to `self`, so a bot connecting directly cannot forge another account's principal. Handlers accept the gateway-set principal **because NATS permissions guarantee only the gateway can set it.**

**(b) Cache-fronted session validation — the real hot path.** Every REST call validates a token. A Mongo read per request is the bottleneck, so put a **read-through cache** in front (same pattern as `pkg/userstore`): in-pod LRU for the hottest tokens + a cross-pod **Valkey** layer. Login (auth-service) writes Mongo and warms the cache; revoke deletes from Mongo and busts the cache (pub/sub invalidation or short TTL). Common-case validation = a Valkey GET (sub-ms), not a Mongo round-trip. **This is where Valkey earns its place (§12 Q6): the validation hot path, not the durable store.**

- *Single writer, many readers (Option B):* the bot-gateway reads the Valkey session entry directly and falls back to an `auth-service` `session.validate` NATS call only on a cold miss — bounding ownership while keeping the hop off the hot path. *Under Option A* the same service owns the store and the cache, so there is **no cross-service validate hop at all** — a Mongo miss reads the local store directly. (Another point in A's favor.)

**(c) Throttle `LastUsedAt`** (§12 Q4): writing it per request would negate the cache. Update only when stale beyond a window (e.g. > 5 min), or drop the field.

**(d) Stateless gateway** → horizontal scale and instant Istio weight-shift/rollback (§10).

### 9.4 Build vs. buy
Build a **thin Go/Gin gateway**, consistent with the repo (auth-service is already Gin; reuse `errcode`, `idgen`, `pkg/subject`, the userstore cache pattern). Off-the-shelf options don't fit: Envoy/Istio have no NATS request/reply transport; NATS's own HTTP tools are config proxies, not RPC bridges. A purpose-built Go edge gives pooled NATS, typed envelopes, and exact RC-shape fidelity with minimal code.

### 9.5 Performance targets (SLA) & load criteria

**SLA targets**

| Path | Target |
|---|---|
| Login latency (`POST /api/v1/login`) | **P99 < 200 ms** |
| Token validation — hot path (Valkey cache hit) | **< 5 ms** |
| Token validation — cold path (Mongo miss) | **< 50 ms** |
| Concurrent sessions per account | **unlimited**, O(1) lookup (hash `_id`) |
| Session cache hit ratio | **> 95%** |

**Load criteria (must sustain)**
- **10,000 logins / minute** sustained.
- **100,000 active sessions**.
- **1,000,000 token validations / minute**.

These drive the design choices in §9.3 (pooled service-account connection, cache-fronted validation, throttled `LastUsedAt`) and are asserted by a load-test stage before cutover. Metrics in §17 expose each (`auth_session_validate_latency_seconds`, `gw_session_cache_hits_total`, `auth_login_total`) so the SLAs are observable in prod.

### 9.6 Endpoint inventory

| Surface | Path | Method | Returns | Credential | CSRF |
|---|---|---|---|---|---|
| Web — login form | `/dev-login` | GET | HTML | — | — |
| Web — login submit | `/dev-login` | POST | redirect + Set-Cookie | form | **yes** |
| Web — change-pwd form | `/changepwd` | GET | HTML | session cookie | — |
| Web — change-pwd submit | `/changepwd` | POST | redirect | session cookie | **yes** |
| API — legacy bot login | `/api/v1/login` | POST | JSON (`authToken`,`userId`,`me`) | — | n/a |
| API — new bot login | `/v1/bot/login` | POST | JSON (new token) | — | n/a |
| API — WS validation | `/v1/auth/validate` | POST | JSON `{valid,account,userId}` | `{userId,authToken}` body | n/a |
| API — authenticated proxy | `/api/v2/*` | * | JSON ← `botplatform-server:8080` | `X-Auth-Token`+`X-User-Id` | n/a |
| Health | `/healthz` | GET | 200 | — | — |

- **Web** = server-rendered HTML, **session cookies** (HttpOnly/Secure/SameSite=Lax), CSRF on every POST.
- **API** = bearer tokens only; **no cookies, no CSRF** (no ambient credential to forge).
- `/api/v1/login` reproduces the legacy RC contract verbatim (existing SDK). `/v1/bot/login` is the new re-architected path. Both write to the same `sessions` store.
- `/v1/auth/validate` is the **once-per-connection** hook the websocket server (:8899) calls before accepting a connection (Part 3 §4.2). `/api/v2/*` is validated then reverse-proxied to `botplatform-server:8080` (Part 3 §4.1).

### 9.7 Dual-token validation (migration)
Validation (§5.3) accepts **both** token schemes against one store: imported legacy RC tokens (`scheme:"legacy"`) and gateway-issued (`scheme:"v1"`). As bots re-login they receive `v1` tokens; legacy tokens age out via the 180-day sliding window. A `auth_session_validate_total{scheme}` metric tracks the legacy share so legacy acceptance can be **switched off** once it trends to zero — the planned phase-out.

---

## 10. Zero-downtime cutover (Istio, same URL, new namespace)

Old: namespace `chat`, serves `https://xx.chat.com/…`. Nextgen: new namespace (`chat-nextgen`), **same public URL**, reusing the shared Istio ingress gateway. Only the backend the gateway routes to changes, invisibly.

### 10.1 Routing
One `VirtualService` on the shared ingressgateway routes host + path to a backend `host` in the new namespace (`<gw-svc>.chat-nextgen.svc.cluster.local`), with `DestinationRule` subsets for weighting. TLS/DNS unchanged (gateway terminates the cert) → no cert churn.

### 10.2 Sequence
1. Deploy nextgen dark in `chat-nextgen`; gateway 100% → old; health-gate on `/healthz`.
2. **Canary ramp over ~1 week:** `1% → 10% → 50% → 100%`, holding at each step while watching SLOs; **gate on error rate < 0.1%** and the §9.5 latency SLAs before advancing. Monitor 24h at 100%.
3. **Rollback within 1 hour** at any step = shift weights back to the `chat` subset (effectively instant via `VirtualService`).
4. **Zero data loss** — sessions/credentials are migrated, not recreated; either-stack auth holds (§10.3).

### 10.3 Why either-stack routing is safe
nextgen honors **migrated credentials** (§6.1) *and* **migrated live legacy tokens** (§6.2, same hash form), so a request routed to either stack stays authenticated — the precondition that makes weighted routing non-disruptive.

### 10.4 Scope boundary
Clean no-downtime for the **login/session slice**. If both stacks also serve live room/message traffic in the window, dual-write/federation consistency is a **separate track**.

---

## 11. Security & rules compliance
- `PasswordHash` / raw tokens are **never** serialized (`json:"-"`) and **never** logged; `sessions._id` stores only `base64(sha256(token))`.
- **Timing-safe credential comparison** — run the bcrypt compare even on unknown accounts (dummy hash); uniform error + timing (no account enumeration).
- **Login rate limiting / lockout:** **5 failed attempts → 15-minute lockout** (keyed by account, ideally also by source IP). Backed by Valkey (shared across pods); lockout returns a uniform auth error, not a distinct "locked" leak. Successful login resets the counter.
- **HTTPS only** — TLS terminated at the Istio ingress gateway; the edge service speaks plain HTTP only inside the mesh.
- **CSRF protection on web (form) routes** (`/dev-login`, `/changepwd`) — synchronizer-token (or double-submit) pattern; verified on every POST. **API/token routes are exempt** (bearer token is not an ambient credential, so not CSRF-forgeable).
- **Session cookies (web)** — `HttpOnly`, `Secure`, `SameSite=Lax`, scoped path; the cookie carries the same session token (validated identically to the API header, §5.3). Distinct surface from API bearer tokens; both resolve to the one `sessions` store.
- Client-facing errors use `pkg/errcode` named constructors + a domain `reason` where the frontend must branch (`requirePasswordChange`, `invalidCredentials`); replied via `errnats.Reply`. Infra failures return raw wrapped errors (collapse to `internal`).
- New client-facing handlers → update `docs/client-api.md` in the same PR.

**WebSocket auth (integration note).** The bot SDK also opens a realtime/WebSocket connection; that transport must authenticate against the **same** `sessions` store (validate the token on connect/upgrade, reuse §5.3). Captured here because Part 1's rollout calls out "fix WebSocket authentication" (Phase 2); the WebSocket handshake path and any per-frame auth belong in the Part 3 components guide.

---

## 12. Open questions & decisions

> "Recommended" entries are the proposed answer with rationale, not yet locked. "Confirmed" entries were verified against the internal codebase or decided in design review.

### Decisions pending sign-off
- **Q1b — Resume RPC for the new native bot SDK.** Recommend **exposing the §5.2 session→JWT exchange as a `session.refresh` RPC** for re-architected native bots (lets them reconnect from a stored token, no password). Legacy REST bots unaffected. *Decision: expose now or defer to the native-SDK milestone (§5.6).*

### Recommended — pending infra confirmation
- **Q3 — Cutover source-of-truth.** **Freeze legacy credentials at canary start + one-time import**; nextgen authoritative thereafter (§6.3). *Confirm:* dual-run window length; whether live password changes must keep working on legacy.

### Integration decisions (Part 3) — recommended, pending confirmation
- **Q10 — Token format.** Recommend **same opaque format** as legacy (indistinguishable to bots/WS); validate our-store-first then legacy-fallback. Optional `bp1_` prefix as a fast-path only if the fallback double-lookup is shown to matter. (Part 3 §7.)
- **Q12 — WebSocket validation.** Recommend the WS server **calls our `POST /v1/auth/validate`** (once per connection), not direct Valkey — single source of truth, no cold-miss false-rejects. (Part 3 §7.)

### Confirmed — closed
- **Q11 — `/api/v1` scope.** ✅ **`/api/v1/login` only** + `/v1/bot/login`; all data via `/api/v2/*` → `botplatform-server`. The `/api/v2/*` endpoints are the REST APIs exposed by the **legacy v2 code**; `botplatform-server` reverse-proxies to **`/api/v2/`** (not `/api/v1`), so our service never proxies any `/api/v1/*` data route. *(confirmed 2026-06-16.)*
- **Q5 — Architecture.** ✅ **Option B — dedicated `bot-gateway`** (design review 2026-06-15). Driven by the web-UI scope (HTML/CSRF/cookies, §9.6) + dual-token validation; blast-radius/key-safety and independent scaling settle it. `bot-gateway` owns `credentials`+`sessions`; `auth-service` keeps the signing key and mints JWTs on request (§7).
- **Q6 — Cache layer.** ✅ In-pod LRU + **Valkey from day one** for the validation hot path (§9.3); **Valkey cluster confirmed available in prod**.
- **Q1 — Login contract.** ✅ `POST /api/v1/login`; body `{ user, password }` plaintext **or** `{ user, password:{ digest, algorithm:"sha-256" } }` (digest optional, for compatibility); response `{ status:"success", data:{ authToken, userId:<17-char>, me:{ _id, username, name, active, roles:["bot"] } } }`; subsequent headers `X-User-Id` + `X-Auth-Token`. Legacy bots use header reuse, not a resume verb (new-SDK resume = Q1b). *(Q1a folded in: path is `/api/v1/login`.)*
- **Q2 — Session lifetime.** ✅ **Sliding idle expiry**, effectively infinite at **180-day idle** — matches current v2 Go repo behavior (§5.5). Refreshed on the throttled use-bump; revocation is the kill switch.
- **Q4 — `LastUsedAt` throttle.** ✅ **5-minute window (300s)** — prevents write amplification while keeping reasonable activity tracking; doubles as the sliding-expiry refresh (§5.5).
- **Q7 — `idleWindow`.** ✅ **180 days** (`SESSION_IDLE_WINDOW=4320h`, §16).
- **Q8 — Bot auth mechanism.** ✅ Bots use **password login only**; PATs are human-only, **out of scope** (§2.2, §6).
- **Q9 — Identity ID mapping.** ✅ v2 Go repo **preserves the 17-char legacy Meteor `_id`**; nextgen `users._id` == `X-User-Id`. No `LegacyUserID`, no mapping (§2.2, §4).

---

## 13. Go types (proposed)

`pkg/model` (shared domain types, both `json` + `bson` tags per house rule):

```go
// model/credential.go
type Credential struct {
    ID                    string `json:"id"                    bson:"_id"` // 17-char Meteor ID (legacy-preserved)
    Account               string `json:"account"               bson:"account"`
    PasswordHash          string `json:"-"                     bson:"passwordHash"`
    PasswordScheme        string `json:"-"                     bson:"passwordScheme"`
    RequirePasswordChange bool   `json:"requirePasswordChange" bson:"requirePasswordChange"`
    CreatedAt             int64  `json:"createdAt"             bson:"createdAt"`
    UpdatedAt             int64  `json:"updatedAt"             bson:"updatedAt"`
}

// model/session.go
type Session struct {
    TokenHash    string     `json:"-"                    bson:"_id"`          // base64(sha256(token))
    UserID       string     `json:"userId"               bson:"userId"`        // 17-char Meteor ID
    Account      string     `json:"account"              bson:"account"`
    Scheme       string     `json:"scheme"               bson:"scheme"`       // "legacy" | "v1"
    IssuedAt     int64      `json:"issuedAt"             bson:"issuedAt"`
    LastUsedAt   int64      `json:"lastUsedAt"           bson:"lastUsedAt"`
    ExpiresAt    *time.Time `json:"expiresAt,omitempty"  bson:"expiresAt,omitempty"`
}
```

Wire envelopes (login is HTTP; field names mirror RC — confirm Q1):

```go
// login request (plaintext OR digest form)
type loginRequest struct {
    User     string          `json:"user"`
    Password json.RawMessage `json:"password"`      // "secret"  OR  {"digest","algorithm"}
    NATSKey  string          `json:"natsPublicKey,omitempty"` // native clients only (§5.1.4)
}
type loginResponse struct {
    Status string `json:"status"`                   // "success"
    Data   struct {
        AuthToken string  `json:"authToken"`         // raw token
        UserID    string  `json:"userId"`            // 17-char Meteor ID
        Me        meBlock `json:"me"`                // identity summary (RC parity)
        JWT       string  `json:"jwt,omitempty"`     // native clients only
    } `json:"data"`
}
type meBlock struct {
    ID       string   `json:"_id"`
    Username string   `json:"username"`
    Name     string   `json:"name"`
    Active   bool     `json:"active"`
    Roles    []string `json:"roles"`                // e.g. ["bot"]
}
```

Gateway→handler principal injection rides as **NATS message headers** (not body, so the per-verb body mapping is untouched):
`X-Chat-Account`, `X-Chat-User-Id`, `X-Request-ID`.

---

## 14. Algorithms (precise) — ✅ confirmed 2026-06-15

**Token hashing (one function, legacy + v1):** ✅ confirmed to match Meteor `Accounts._hashLoginToken`.
`tokenHash = base64.StdEncoding(sha256(rawToken))` (a golden fixture still gates the implementation, §18).

**Password verify:** ✅ confirmed flow.
```
input = digest (if {digest,algorithm:"sha-256"} supplied)  else  hex(sha256(plaintext))   // SHA-256 -> hex string
ok    = bcrypt.CompareHashAndPassword(cred.PasswordHash /* = services.password.bcrypt */, input) == nil
```
i.e. **SHA-256 the plaintext → hex string, then `bcrypt.CompareHashAndPassword` against `services.password.bcrypt`.** Run the bcrypt compare even on unknown-account (against a dummy hash) to keep timing uniform.

**Session validation (gateway hot path):**
```
h := tokenHash(xAuthToken)
s := valkey.Get(h)                       // hot path
if miss: s = nats.Request(auth.session.validate, h)   // cold path; auth-svc reads Mongo + warms Valkey
if s == nil           -> 401 invalidCredentials
if s.ExpiresAt != nil && now > s.ExpiresAt -> 401 sessionExpired
if xUserId != "" && xUserId != s.UserID  -> 401 invalidCredentials   // sanity check; same 17-char id space, no mapping
if now - s.LastUsedAt > throttleWindow: async bump LastUsedAt + ExpiresAt=now+idleWindow (Mongo + cache)
return principal{account, userId}
```

---

## 15. NATS subjects & error reasons

**Server-internal (gateway/edge → auth-service; caller not yet user-scoped or is the trusted gateway):**
- `chat.server.request.auth.login` — verify credentials, create session.
- `chat.server.request.auth.session.validate` — token hash → principal (cold-miss).
- `chat.server.request.auth.session.refresh` — session token → fresh JWT (HTTP-frontable for native clients).

**Operator, user-scoped (admin JWT; handler checks caller `roles ∋ admin` via `userstore`):**
- `chat.user.{account}.request.admin.bot.create`
- `chat.user.{account}.request.admin.bot.password.set`
- `chat.user.{account}.request.admin.session.list`
- `chat.user.{account}.request.admin.session.revoke`
- `chat.user.{account}.request.admin.session.revokeAll`

Add builders to `pkg/subject` (never `fmt.Sprintf` at call sites).

**Error reasons** (`codes_authservice.go`, attached via `errcode.WithReason`):
`invalidCredentials`, `sessionExpired`, `requirePasswordChange`, `accountExists`, `notBotAccount`, `forbiddenNotAdmin`.

---

## 16. Configuration (env, `caarlos0/env`)

**auth-service** (additions): `SESSION_IDLE_WINDOW` (`envDefault:"4320h"` ≈180d), `SESSION_LASTUSED_THROTTLE` (`"5m"`), `JWT_TTL` (`"15m"`), `BCRYPT_COST` (`"10"` — match legacy), `LOGIN_MAX_ATTEMPTS` (`"5"`), `LOGIN_LOCKOUT` (`"15m"`), `VALKEY_ADDRS` (required), Mongo URI/DB (required). Reuses existing `AUTH_SIGNING_KEY`.

**`bot-gateway`** (the §7 Option-B service): `NATS_URL` + service-account creds (required), `GATEWAY_NATS_POOL_SIZE` (`"4"`), `GATEWAY_REQUEST_TIMEOUT` (`"10s"`), `GATEWAY_MAX_CONCURRENCY` (`"500"`), `VALKEY_ADDRS` (required), Mongo URI/DB (required, owns `credentials`+`sessions`), `SESSION_CACHE_TTL` (`"5m"`), `BCRYPT_COST` (`"10"`), `LOGIN_MAX_ATTEMPTS` (`"5"`), `LOGIN_LOCKOUT` (`"15m"`), `SESSION_IDLE_WINDOW` (`"4320h"`), `SESSION_LASTUSED_THROTTLE` (`"5m"`), and web-surface vars: `COOKIE_DOMAIN`, `COOKIE_SECURE` (`"true"`), `CSRF_KEY` (required secret), `AUTH_SERVICE_SUBJECT` (for JWT-mint calls). `PORT` (`"8080"`). Secrets never defaulted (house rule).

---

## 17. Observability (metrics)
- `auth_login_total{result}`, `auth_login_lockouts_total`, `auth_session_validate_total{source=cache|mongo,result}`, `auth_session_validate_latency_seconds`, `auth_active_sessions` (gauge).
- gateway: `gw_request_duration_seconds{route,status}`, `gw_nats_request_total{result=ok|timeout|err}`, `gw_session_cache_hits_total{hit}`.
- Logging: `log/slog` JSON, request-id propagated; **never** log tokens / password input / hashes.

---

## 18. Test plan (TDD, ≥80% / 90% core)
- **Golden hash fixture** — `TestHashAuthToken`: a known `(rawToken → base64(sha256))` pair proving parity with Meteor `Accounts._hashLoginToken`. **First test; gates all store code.**
- **Password verify** — `TestVerifyPassword` (table): plaintext-correct, digest-correct, wrong password, unknown account (uniform timing/error), `requirePasswordChange` surfaced.
- **Migration filter** — `TestImportLoginTokens_SkipsPAT`: a seed mixing `type:""` and `type:"personalAccessToken"` entries; assert only the regular tokens become `sessions` rows.
- **Rate limit** — `TestLoginRateLimit`: 5 failures → 6th is locked out (uniform error); successful login resets the counter; lockout expires after the window.
- **Web/CSRF** — `TestDevLoginForm` (GET renders form + CSRF token), `TestDevLoginSubmit` (POST sets HttpOnly/Secure cookie on success; **rejects POST without/with bad CSRF token**), `TestChangePwd` (requires cookie + CSRF; revokes other sessions on success).
- **Dual-token** — `TestValidate_AcceptsLegacyAndV1`: both a `scheme:"legacy"` and a `scheme:"v1"` token validate via the same path.
- **Session lifecycle** (unit, mocked store): create, validate hit, expired, `X-User-Id` mismatch, throttled-bump skip vs. apply, revoke, revoke-all.
- **Provisioning handlers** (table, mocked store): create bot (happy/`accountExists`/`notBotAccount`), set-password (clears flag + revokes), non-admin caller → `forbiddenNotAdmin`.
- **Gateway** (unit): principal-header injection, `errcode`→HTTP status mapping, timeout→503, deadline propagation.
- **Integration** (`//go:build integration`, `pkg/testutil`): `credentials`/`sessions` Mongo store incl. partial-TTL index behavior; Valkey read-through + invalidation; **migration script** against a Mongo seeded with legacy-RC-shaped docs (password + non-PAT `resume.loginTokens`). Assert 17-char `_id`s are preserved verbatim into `credentials._id`.

---

## 19. Implementation phases (each = TDD + own PR)
*Architecture is decided: Option B — a new `bot-gateway` service (§7).*
1. Scaffold `bot-gateway` service (per repo service template) + `model` types + `credentials`/`sessions` stores (Mongo) + indexes + mocks.
2. Password verify + session lifecycle + `POST /api/v1/login` + `POST /v1/bot/login` + JWT-refresh (service-to-service mint call to `auth-service`).
3. Web UI: `/dev-login` + `/changepwd` HTML forms, session cookies, CSRF middleware.
4. Valkey read-through cache + invalidation; dual-token (legacy + v1) validation.
5. Migration script (credential + non-PAT live-token import) with `--dry-run` + verify mode.
6. REST↔NATS edge (principal injection, error mapping, pooled conn, load-shed).
7. Operator provisioning NATS handlers + admin authz + `docs/client-api.md`.
8. **Load test** against the §9.5 SLAs/criteria — gate cutover on pass.
9. Istio routing + canary runbook (separate from app PRs).
10. Operator UI frontend (likely a separate repo/track).

---

## 20. `docs/client-api.md` delta
- **§2.2** — add the password-login HTTP endpoint (request: `user`+`password` plaintext/digest, optional `natsPublicKey`; response envelope; error cases incl. `requirePasswordChange`).
- **New admin section** — the five provisioning RPCs (§15) with request/response field tables + JSON examples + error reasons.
- Updated in the **same PR** as the handlers (house rule).

---

## 21. Risks
- ~~PAT vs password (Q8)~~ — **closed:** bots are password-only, PATs out of scope (§2.2).
- ~~ID mapping (Q9)~~ — **closed:** legacy 17-char `_id` preserved end-to-end, no mapping (§2.2).
- **Infinite sessions** — standing bearer tokens; mitigated by sliding reap + revocation, but operators must have working revoke before go-live.
- **Cache/Mongo divergence on revoke** — a revoke that updates Mongo but not the cache leaves a token live until cache TTL; prefer explicit invalidation over TTL-only.

---

## 22. Verification checklist (run against the internal codebase before implementation)

Tick each before promoting this spec to a plan. These are the assumptions the design rests on.

**Legacy RC fork**
- [x] **Login route = `POST /api/v1/login`**; request `{ user, password }` plaintext or `{digest, algorithm}`; full success envelope incl. `me` + `X-Auth-Token`/`X-User-Id` headers. *(confirmed 2026-06-15, Q1)*
- [x] **Password verify confirmed:** `services.password.bcrypt` = `bcrypt(sha256_hex(pw))`; verify = SHA-256→hex then `bcrypt.CompareHashAndPassword`. *(confirmed 2026-06-15, §14)* — *still capture the bcrypt cost for the migration.*
- [ ] Are there **admin** accounts with **no** local password (SSO/LDAP-only) that can't use operator-UI password login? (Bots are all password — confirmed.)
- [ ] Login-token storage: `services.resume.loginTokens[]`, `hashedToken = base64(sha256(raw))`, field names. **(§6.2)**
- [x] **Bots use password login only; PATs are human-only / out of scope.** *(confirmed 2026-06-15, Q8)*
- [x] **Token hashing matches Meteor `Accounts._hashLoginToken`** = `base64(sha256(token))`. *(confirmed 2026-06-15, §14)* — golden fixture still gates the code.
- [x] **Sliding-infinite chosen; `loginExpirationDays` irrelevant.** *(confirmed 2026-06-15, Q2/Q7)*

**Nextgen / identity**
- [ ] Does identity-sync already populate **admin + bot** accounts in nextgen `users`, with `roles`? **(§6.1)**
- [x] **Nextgen `users._id` == legacy 17-char Meteor `_id`** (v2 Go repo preserves it); no `LegacyUserID`, no mapping. *(confirmed 2026-06-15, Q9)*
- [ ] `model.IsBotAccount` covers every real bot naming pattern in production.

**Infra / platform**
- [x] **Valkey cluster available in prod** to the host service. *(confirmed 2026-06-15, Q6)*
- [ ] Shared Istio ingress gateway + who owns the `VirtualService`/`DestinationRule`; the new namespace name; cert ownership. **(§10)**
- [ ] A NATS **service account** for the REST edge with publish rights on `chat.user.*.request.>` (and that user JWTs remain scoped to self). **(§9.3)**

**Decisions to sign off (§12)**
- [ ] **Q1b** expose `session.refresh` now or defer · [ ] Q3 freeze-legacy (dual-run window)
- [x] **Q5 architecture = Option B (`bot-gateway`)** · [x] Q1 contract · [x] Q2 sliding-infinite · [x] Q4 throttle=5m · [x] Q6 Valkey day-one · [x] Q7 idleWindow=180d · [x] Q8 password-only · [x] Q9 id-preserved
