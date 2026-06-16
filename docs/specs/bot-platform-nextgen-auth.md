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

**New native bot SDK (re-architecture) — decided (Q1b):** the internal session→JWT exchange ships anyway; the **public `session.refresh` resume RPC is deferred to the native-SDK milestone** (no caller until that SDK exists). Legacy REST bots are untouched (header reuse).

---

## 6. Migration (legacy RC Mongo → nextgen Mongo)

### 6.1 Credential import
For each legacy `users` doc that is an admin or bot account **with a local password** (`services.password.bcrypt`):
- Ensure the nextgen `users` identity row exists (existing identity-sync path, not owned here) — it carries the **same 17-char `_id`**.
- Insert `credentials` with `_id = legacy users._id` (verbatim, 17-char Meteor ID): **copy `services.password.bcrypt` → `PasswordHash` byte-for-byte — do NOT rehash or recompute** (we don't have the plaintext, and the verify path already matches RC's `bcrypt(sha256_hex(pw))`). `PasswordScheme:"rc-sha256-bcrypt"`, legacy force-change flag → `RequirePasswordChange`, timestamps.

### 6.2 Live-session import (no-reauth cutover)
Import **only regular login tokens** from `services.resume.loginTokens[]` — i.e. entries where **`type` is empty/absent**. **Explicitly skip `type:"personalAccessToken"`** (human PATs, out of scope, §2.2). For each imported entry: insert a `sessions` doc with `_id = hashedToken` (already `base64(sha256(token))`, copied verbatim), `UserID = legacy _id`, `Scheme:"legacy"`, and `ExpiresAt = now + idleWindow` (sliding, §5.5 — legacy `when` ignored since sessions are effectively infinite). Because we reuse RC's hash form **and** the same `_id`, an in-flight bot's existing `X-Auth-Token`+`X-User-Id` validate against the imported row unchanged. The next login issues a native `v1` token.

### 6.3 Cutover source-of-truth — real-time users-collection sync (Q3)

The migration has **no risky data "flip"** — two separable axes:

- **Credentials (passwords) — solved by a real-time sync.** Credentials are static and imported verbatim (§6.1); to also cover any password *change* during the ~1-week ramp, ride the **real-time legacy→nextgen `users`-collection sync** (the same identity-sync that preserves the 17-char `_id`), **extended to carry `services.password.bcrypt` → nextgen `credentials`**. With that, a password change on legacy propagates automatically — both sides hold the same hash, **no freeze required**. Conditions: (a) the sync must route the hash into the `credentials` collection (it lives there, not on the `users` doc); (b) keep **one write-authority** during the window — legacy writes, sync is one-directional legacy→nextgen, and nextgen-side password changes (`/changepwd`, rotation) stay disabled until 100% cutover (avoids bidirectional races). *(A literal read-only "freeze" is the trivial fallback if the sync can't carry credentials.)*
- **Tokens — a separate axis (not solved by the users sync).** Legacy tokens import/validate on both sides, but **nextgen-issued `v1` tokens don't exist on the legacy side**, so any downstream that re-validates a bearer token must use our dual-token validator — see **Q14 / §9.8**. Token continuity during the ramp rests on dual-token import + a **monotonic ramp** (only shift weight toward nextgen) + routing affinity, not on the credential sync.

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

## 8. Admin = role-gated web UI (Q15, Q18)

There is **no separate admin API.** `/dev-login` is the **one** web login for humans (admins *and* bot-account holders / devs); after auth the **server-rendered UI is role-aware**:

- **admin** (`roles ∋ admin`) → an **admin console**: create bot, set/rotate password, list/revoke sessions.
- **non-admin** (bot account / dev) → a **simple page**: you're logged in; change your own password.

Admin actions are **CSRF-protected form POSTs on role-gated web routes** within the same UI — *not* a JSON API the front-end "calls again," and *not* NATS. Each is a Gin handler that renders HTML / redirects (errors via the web flow). Bot **processes** never use these — they authenticate programmatically via `POST /api/v1/login` / `/v1/bot/login`.

| Admin action (web, `role==admin`) | Route | Writes |
|---|---|---|
| console / list bots | `GET /admin` | reads |
| create bot | `POST /admin/bots` | `users` + `credentials` (`RequirePasswordChange:true`) |
| set/rotate password | `POST /admin/bots/{id}/password` | `credentials`, revoke `sessions` |
| list sessions | `GET /admin/bots/{id}/sessions` | reads `sessions` by `userId` |
| revoke session / all | `POST /admin/bots/{id}/sessions/revoke` | `sessions` |

> A **JSON** admin API would only be added later **if** programmatic provisioning is needed (e.g. CI creating bots) — not now (Q18). `docs/client-api.md` (NATS `chat.user.` RPCs) does not apply; document these web routes in `botplatform-service`'s own README/API doc.

---

## 9. `botplatform-service` — the auth provider (login · validate · sessions · web UI · admin)

`botplatform-service` (the §7 Option-B service; Part 2's earlier "bot-gateway") is **not** a data-path proxy. It is the **auth provider**: it owns the `credentials`/`sessions` stores, issues and validates tokens, serves the bot/admin web UI, and exposes the admin surface. The existing **ApiGW** keeps routing/rate-limit/metrics and **delegates auth to us** (Q17).

> **Topology (Part 3 §4.1).** `bot → ApiGW → Server(/api/v2/*)`. ApiGW validates each request by calling our **`POST /v1/auth/validate`** (replacing today's slow proxy-to-legacy validation), then routes to `Server` with the validated principal in headers; `Server` trusts ApiGW under **Istio mTLS**. The WebSocket server and EventConsumer likewise call `/v1/auth/validate`. So **we never sit in the `/api/v2/*` data path** — no reverse proxy, no REST→NATS bridge here (that's downstream, Q13). Our only NATS use is a possible control-plane JWT-mint call to `auth-service` for *native* bots (future).

### 9.1 Responsibilities
1. **Web UI (server-rendered HTML)** — `GET/POST /dev-login` and `GET/POST /changepwd` (§9.6); render forms, handle submits, set/clear **session cookies**, enforce **CSRF** on POST.
2. **Login** — `POST /api/v1/login` (legacy contract) + `POST /v1/bot/login` (new); verify credentials, issue a token into the `sessions` store, return the RC-compatible envelope.
3. **Validation** — **`POST /v1/auth/validate`** (§9.8): the single dual-token (`legacy`+`v1`) authority, cache-fronted, called by ApiGW / WS / EventConsumer.
4. **Stores** — own `credentials` + `sessions` (Mongo) and the Valkey validation cache.
5. **Admin surface** — REST endpoints for bot provisioning / password rotation / session management (§9.6, §15), consumed by a web operator UI.
### 9.2 Topology

```
bot ──HTTP──▶ ApiGW ──(POST /v1/auth/validate)──▶ botplatform-service ──▶ Valkey/Mongo (sessions)
              │ rate-limit, metrics                  │ login · validate · admin · web UI
              └──route (principal in hdrs, mTLS)──▶ Server (/api/v2/*)
WS server :8899 ──(POST /v1/auth/validate)──▶ botplatform-service
EventConsumer    ──(POST /v1/auth/validate)──▶ botplatform-service
```

ApiGW/WS/EventConsumer are the **callers**; `botplatform-service` is the **auth provider**. We are not on the `/api/v2/*` data path — `Server` sits behind ApiGW and trusts the principal ApiGW injects (mTLS).

### 9.3 Performance — the validation hot path
The 1M/min hot path is **`/v1/auth/validate`**, so the whole design optimizes it:

**(a) Cache-fronted validation.** In-pod LRU + cross-pod **Valkey** in front of Mongo (same pattern as `pkg/userstore`). Login writes Mongo + warms the cache; revoke deletes Mongo + busts the cache (pub/sub invalidation or short TTL). Common case = a Valkey GET (<5 ms), not a Mongo round-trip. ApiGW may add an optional short-TTL micro-cache on top.

**(b) Throttle `LastUsedAt`** (§12 Q4): writing per request would negate the cache and double as the sliding-expiry refresh (§5.5) — update only when stale > 5 min.

**(c) O(1) lookup, unlimited sessions** — keyed by `base64(sha256(token))` (§4.2); session count never affects validate cost.

**(d) Stateless service** → horizontal scale; the validate endpoint is read-mostly so it scales out trivially.

> Data-path connection pooling / principal injection to `Server` is **ApiGW's** concern, not ours — we just answer validate calls fast.

### 9.4 Build vs. buy
Build a **thin Go/Gin service**, consistent with the repo (`auth-service` is already Gin; reuse `errcode`, `idgen`, the `userstore` cache pattern). It's a focused auth provider — login + validate + stores + server-rendered web UI + admin REST — no proxy, no NATS data bridge.

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

These drive the design choices in §9.3 (cache-fronted validation, throttled `LastUsedAt`) and are asserted by a load-test stage before cutover. Metrics in §17 expose each (`auth_session_validate_latency_seconds`, `auth_session_cache_hits_total`, `auth_login_total`) so the SLAs are observable in prod.

### 9.6 Endpoint inventory — all REST (Q15)

| Surface | Path | Method | Returns | Credential | CSRF |
|---|---|---|---|---|---|
| Web — login form | `/dev-login` | GET | HTML | — | — |
| Web — login submit | `/dev-login` | POST | redirect + Set-Cookie | form | **yes** |
| Web — change-pwd | `/changepwd` | GET/POST | HTML / redirect | session cookie | **yes** (POST) |
| Web — admin console *(role==admin)* | `/admin`, `/admin/bots[...]` | GET/POST | HTML / redirect | admin session cookie | **yes** (POST) |
| API — legacy bot login | `/api/v1/login` | POST | JSON (`authToken`,`userId`,`me`) | — | n/a |
| API — new bot login | `/v1/bot/login` | POST | JSON (new token) | — | n/a |
| API — token validation | `/v1/auth/validate` | POST | JSON `{valid,account,userId}` | `{userId,authToken}` body | n/a |
| Health | `/healthz` | GET | 200 | — | — |

- **There is no `/api/v2/*` here** — ApiGW (existing) routes that to `Server`; we only answer ApiGW's `/v1/auth/validate` calls (Q17).
- **Admin is part of the role-gated web UI** (not a separate JSON API, not NATS — Q15/Q18): the same `/dev-login` session, `roles ∋ admin`, server-rendered pages + CSRF form POSTs (§8).

- **Web** = server-rendered HTML, **session cookies** (HttpOnly/Secure/SameSite=Lax), CSRF on every POST.
- **API** = bearer tokens only; **no cookies, no CSRF** (no ambient credential to forge).
- `/api/v1/login` reproduces the legacy RC contract verbatim (existing SDK). `/v1/bot/login` is the new re-architected path. Both write to the same `sessions` store.
- `/v1/auth/validate` is the **once-per-connection** hook the websocket server (:8899) calls before accepting a connection (Part 3 §4.2). `/api/v2/*` is validated then reverse-proxied to `botplatform-server:8080` (Part 3 §4.1).

### 9.7 Dual-token validation (migration)
Validation (§5.3) accepts **both** token schemes against one store: imported legacy RC tokens (`scheme:"legacy"`) and gateway-issued (`scheme:"v1"`). As bots re-login they receive `v1` tokens; legacy tokens age out via the 180-day sliding window. A `auth_session_validate_total{scheme}` metric tracks the legacy share so legacy acceptance can be **switched off** once it trends to zero — the planned phase-out.

### 9.8 `/v1/auth/validate` — the single dual-token authority (Q14)
`POST /v1/auth/validate` is the **one** place token validation lives; it runs §5.3 (dual-token: `legacy` + `v1`) and returns `{valid, account, userId}`. **The caching is part of this API** — Valkey hot path (<5 ms, >95% hit), Mongo on miss, legacy-fallback, sliding-expiry bump, and lockout all live behind it — so callers get fast, correct validation without re-implementing any of it or coupling to our cache schema. Downstreams **must not** re-implement token logic or blindly trust a raw `X-User-Id`:

- **ApiGW** — the front-door router; **calls `/v1/auth/validate`** before routing (replacing today's proxy-to-legacy validation, which added latency + failure points). May add an optional short-TTL micro-cache.
- **WebSocket server (:8899)** and **EventConsumer** — not behind ApiGW, so they **call `/v1/auth/validate`** directly.
- **Server / legacy v2 backend** — sits *behind* ApiGW, so it **trusts the principal ApiGW injects** under **Istio mTLS service-identity** + `X-User-Id` overwrite. No double-validation of the 1M/min hot path.

Rule of thumb: **front-door / no trusted upstream → call `/v1/auth/validate`; behind a trusted (mTLS) validator → trust the injected principal.**

---

## 10. Zero-downtime cutover (Istio, same URL — cross-cluster)

**Actual topology.** Legacy runs in cluster **fz2**, namespace **chat**, behind the **chat gateway**; the bot/chat domain (`botpltfr-{site}.chat.f15.com`) resolves to fz2. Nextgen runs in cluster **fz1**, namespace **wsp**, behind the **wsp gateway**. "No URL change" only constrains the **hostname the bot dials** — DNS, gateway routing, and TLS are all server-side, so the migration is invisible to bots.

### 10.1 Routing — front door stays the chat gateway (recommended)
- **DNS unchanged:** chat domain → **chat gateway (fz2)**. No bot-facing DNS or cert change.
- The bot host's `VirtualService` on the chat gateway gets **two weighted backends**: legacy (local, `chat` ns) and **nextgen cross-cluster** to the **wsp gateway (fz1)** — reached via Istio multi-cluster mesh **or** a `ServiceEntry` to the wsp gateway's address.
- The **wsp gateway accepts the chat host** (a Gateway server block + cert/SNI for the chat domain alongside the wsp domain) and routes it to nextgen `botplatform-service`/ApiGW in the `wsp` ns.
- **Alternative (final state):** repoint the chat-domain DNS to the wsp gateway and let it route migrated→nextgen-local / unmigrated→legacy(fz2). Coarser canary (weighted DNS + TTL) and slower rollback — prefer it *after* migration, not as the canary mechanism.

### 10.2 Sequence
1. Deploy nextgen dark in `fz1`/`wsp`; chat-gateway weight 100% → legacy (fz2); health-gate on `/healthz`.
2. **Canary ramp over ~1 week:** `1% → 10% → 50% → 100%` of the chat-gateway VirtualService weight shifted **cross-cluster to fz1/wsp**, holding at each step on SLOs; **gate on error rate < 0.1%** + §9.5 latency. Monitor 24h at 100%.
3. **Rollback within 1 hour** = shift weights back to the fz2 subset (instant via `VirtualService`).
4. **Zero data loss** — credentials/sessions migrated + kept current by the real-time users-sync (§6.3); either stack authenticates identically (§10.3).

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

> "Confirmed" entries were verified against the internal codebase or decided in design review. As of 2026-06-16 **all open questions are decided** — the recommendation was accepted for each; Q12/Q13 remain subject to external-team wiring confirmation but the design assumes the recommended answer.

### Decided 2026-06-16 (recommendation accepted)
- **Q1b — Resume RPC.** ✅ **Defer the public `session.refresh` verb to the native-SDK milestone**; the underlying session→JWT exchange (§5.2) ships anyway. Legacy REST bots use header reuse (§5.6).
- **Q3 — Cutover source-of-truth.** ✅ **Real-time `users`-collection sync extended to carry the bcrypt hash into `credentials`** → password changes propagate, **no freeze** (§6.3). One write-authority during the ramp (legacy writes; nextgen-side changes off until cutover). Tokens are a separate axis (Q14). *Infra: confirm the sync can carry the credential field; read-only freeze is the trivial fallback.*
- **Q10 — Token format.** ✅ **Same opaque format** as legacy; validate our-store-first then legacy-fallback. Optional `bp1_` fast-path prefix only if profiling demands it (Part 3 §7).
- **Q12 — WebSocket validation.** ✅ WS server **calls `/v1/auth/validate`** (Part 3 §7). *Wired during implementation/migration — not a design blocker.*
- **Q13 — REST→NATS bridge ownership.** ✅ Bridge lives in **`Server`/data-plane track**, never our auth service (Part 3 §4.1). *Wired during implementation/migration — not a design blocker.*

### Confirmed — closed
- **Q18 — Admin surface.** ✅ **No separate admin API.** `/dev-login` is one role-aware web login; admin = role-gated server-rendered web routes + CSRF form POSTs (§8). Bot processes use the login API only. A JSON admin API is deferred until programmatic provisioning is actually needed. *(2026-06-16.)*
- **Q17 — Service scope.** ✅ **`botplatform-service` is the auth provider, not a data-path proxy** (Option (b), 2026-06-16). ApiGW (existing) keeps routing/rate-limit/metrics and calls our `/v1/auth/validate`; `Server` serves `/api/v2/*`. We own login + validate + `credentials`/`sessions` + web UI + admin (§9).
- **Q16 — Sequencing.** ✅ **Validation-first** (§19): move validation off the legacy proxy first (biggest win), then login, then sunset legacy. Login is phase-2.
- **Q15 — Admin/auth surface protocol.** ✅ **REST, not NATS** (§8, §15). Every caller (bots, browsers, ApiGW, WS) speaks HTTP; NATS is reserved for the nextgen backend's own comms. Admin ops are REST endpoints on `botplatform-service`.
- **Q14 — Downstream validation.** ✅ `/v1/auth/validate` is the single dual-token authority (§9.8); ApiGW/WS/EventConsumer call it; `Server` trusts ApiGW's mTLS-injected principal (no double-validate).
- **Q11 — `/api/v1` scope.** ✅ **`/api/v1/login` only** + `/v1/bot/login`; data is `/api/v2/*` on `Server` (legacy v2 REST), never proxied by us. *(confirmed 2026-06-16.)*
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

## 15. Routes & error reasons (REST — Q15)

`botplatform-service` is a **REST/HTTP service** (Gin). Its full route surface is the §9.6 inventory: web (`/dev-login`, `/changepwd`), login (`/api/v1/login`, `/v1/bot/login`), validation (`/v1/auth/validate`), and admin (`/v1/admin/bots…`). **No NATS subjects** are exposed by this service.

> **NATS is out of this service's surface.** It's reserved for the nextgen chat backend's internal service-to-service comms and native-client access. The *only* possible NATS use is a future control-plane call to `auth-service` to mint a NATS JWT for *native* bots — not part of the July scope.

**Error reasons** (attached via `errcode.WithReason`, surfaced through `errhttp.Write`):
`invalidCredentials`, `sessionExpired`, `requirePasswordChange`, `accountExists`, `notBotAccount`, `forbiddenNotAdmin`, `rateLimited`.

---

## 16. Configuration (env, `caarlos0/env`)

**`botplatform-service`** (the §7 Option-B auth provider): Mongo URI/DB (required, owns `credentials`+`sessions`), `VALKEY_ADDRS` (required), `SESSION_CACHE_TTL` (`"5m"`), `BCRYPT_COST` (`"10"` — match legacy), `LOGIN_MAX_ATTEMPTS` (`"5"`), `LOGIN_LOCKOUT` (`"15m"`), `SESSION_IDLE_WINDOW` (`"4320h"` ≈180d), `SESSION_LASTUSED_THROTTLE` (`"5m"`), web-surface vars `COOKIE_DOMAIN`, `COOKIE_SECURE` (`"true"`), `CSRF_KEY` (required secret), and `HTTP_MAX_CONCURRENCY` (`"500"`), `PORT` (`"8080"`). No NATS/proxy vars (not a data-path proxy). *Future native-bot JWT mint would add `AUTH_SERVICE_URL`.* Secrets never defaulted (house rule).

---

## 17. Observability (metrics)
- `auth_login_total{result}`, `auth_login_lockouts_total`, `auth_session_validate_total{source=cache|mongo,result,scheme}`, `auth_session_validate_latency_seconds`, `auth_session_cache_hits_total{hit}`, `auth_active_sessions` (gauge).
- HTTP middleware: `http_request_duration_seconds{route,status}`.
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
- **Validate endpoint** (unit): `/v1/auth/validate` happy/expired/unknown, `errhttp` status mapping, cache hit vs Mongo miss path.
- **Integration** (`//go:build integration`, `pkg/testutil`): `credentials`/`sessions` Mongo store incl. partial-TTL index behavior; Valkey read-through + invalidation; **migration script** against a Mongo seeded with legacy-RC-shaped docs (password + non-PAT `resume.loginTokens`). Assert 17-char `_id`s are preserved verbatim into `credentials._id`.

---

## 19. Implementation phases (each = TDD + own PR)
*Architecture: Option B — `botplatform-service` as the auth provider (§7, §9). **Validation-first** sequencing (Q16): the urgent win is moving validation off the legacy proxy.*

1. Scaffold `botplatform-service` (repo service template) + `model` types + `credentials`/`sessions` stores (Mongo) + indexes + mocks.
2. **Migration script** (credential import — verbatim hashes; non-PAT live-token import) with `--dry-run` + verify; ensure existing tokens land in the new store (+ real-time token sync, or rely on legacy-fallback until login moves).
3. **Validation (the win): `POST /v1/auth/validate`** — dual-token (legacy+v1), Valkey read-through cache + invalidation, throttled `LastUsedAt`. Wire **ApiGW + WS + EventConsumer** to call it (replacing legacy-proxy validation).
4. **Login:** `POST /api/v1/login` + `POST /v1/bot/login` — verify credentials, issue v1 tokens into the new store. Reroute `/login` to us at the Istio layer (same URL).
5. **Web UI:** `/dev-login` + `/changepwd` HTML forms, session cookies, CSRF middleware.
6. **Admin REST** (`/v1/admin/bots…`) + admin authz + service API doc.
7. **Load test** against the §9.5 SLAs/criteria — gate cutover on pass.
8. Istio routing + canary runbook (validation cutover, then login); monitor → sunset legacy auth.
9. Operator UI frontend + (future) native-bot `session.refresh` (Q1b) — likely separate tracks.

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
- [ ] **ApiGW** can be wired to call `/v1/auth/validate`; **Server** trusts ApiGW's injected principal via mTLS. **(Q14/Q17)**
- [ ] External-dev confirmations: **WS server** calls `/v1/auth/validate` (Q12); **Server/data-plane** owns the REST→NATS bridge (Q13).
- [ ] Real-time `users`-collection sync can carry the bcrypt hash into `credentials` (Q3).

**Decisions (§12) — all decided 2026-06-16** *(Q12/Q13 get wired during implementation/migration — not blockers)*
- [x] Q1 contract · [x] Q1b defer-resume · [x] Q2 sliding-infinite · [x] Q3 sync-not-freeze · [x] Q4 throttle=5m · [x] Q5 Option B · [x] Q6 Valkey · [x] Q7 180d · [x] Q8 password-only · [x] Q9 id-preserved · [x] Q10 same-format · [x] Q11 v1-login-only · [x] Q14 validate-authority · [x] Q15 REST-admin · [x] Q16 validation-first · [x] Q17 auth-provider
