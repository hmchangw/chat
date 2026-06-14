# Spec: Bot Platform NextGen Migration — Password Auth, Sessions & Cutover

> **Status:** DRAFT — design record for review. Forward-looking language ("the implementer must …", "Phase N") describes the proposed rollout, not shipped work. Open questions are tracked in §12; they must be resolved before this becomes an implementation plan.

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
- **Login response** — `{ "status": "success", "data": { "authToken": "<raw>", "userId": "<id>" } }`. Subsequent calls authenticate with headers **`X-Auth-Token: <raw>`** + **`X-User-Id: <id>`**.
- **Password storage** — `users.services.password.bcrypt` = `bcrypt(sha256_hex(password))` (Meteor accounts-password). Verification: hex-SHA-256 the incoming plaintext (or take the client-supplied `digest`), then `bcrypt.CompareHashAndPassword`.
- **Login-token storage** — `users.services.resume.loginTokens[]`, each `{ when, hashedToken }` where **`hashedToken = base64(sha256(rawToken))`** (Meteor `Accounts._hashLoginToken`). The raw token is the `X-Auth-Token`; the server hashes the inbound token and matches.

> Sources: RC REST auth (`developer.rocket.chat`, RocketChat/Rocket.Chat issue #5466), RC password format (forums.rocket.chat "Password format in database"), Meteor `Accounts._hashLoginToken` (Meteor forums / accounts-base). Cited in chat.

**Implication:** identity is solved; we add (a) a credential store, (b) a session store using RC's exact token-hash form, (c) a password-login path that reuses JWT minting, (d) provisioning handlers + UI, (e) the gateway (§9).

---

## 3. Key design decisions

| Concern | Decision | Why |
|---|---|---|
| Bot/admin **identity** | Stays in the shared `users` collection (roles distinguish admin/bot/user) | Every downstream service resolves accounts through the cached `userstore`; a second identity collection forces double lookups and breaks display-name/federation resolution. |
| **Credentials** | New `credentials` collection, owned by the auth service | `users` is read-only shared infra cached fleet-wide; a bcrypt hash must not ride in a broadly-cached identity doc, and only the auth service verifies passwords. |
| **Sessions** | New `sessions` collection, **one doc per session keyed by `base64(sha256(token))`** | Eliminates the legacy capped array; O(1) lookup independent of session count; TTL auto-expiry; per-session + per-account revocation. The hash form matches RC so one hash fn covers legacy + native tokens. |
| **Scope** | Credentials + sessions are **account-agnostic** (admins *and* bots) | The legacy login authenticates admins too — shared password-login infrastructure, not bot-only. |
| **NATS JWT** | Short-lived, **minted from a valid session**, refreshed without re-entering the password | Reuses existing JWT machinery; session = durable login, JWT = ephemeral capability. |
| **Validation hot path** | Mongo durable + **read-through cache (in-pod LRU + Valkey)** | Every REST call validates a token; a Mongo read per call is the bottleneck (§9.3). |

---

## 4. Data model

### 4.1 `credentials` collection
One document per password-login account (admin or bot), keyed to `users._id`.

| Field | Type | Tags | Notes |
|---|---|---|---|
| ID | `string` | `bson:"_id"` | = `users._id` (32-char UUIDv7 hex). 1:1 with identity. |
| Account | `string` | `bson:"account" json:"account"` | Denormalized for login lookup. Unique index. |
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
| UserID | `string` | `bson:"userId" json:"userId"` | = `users._id`. Indexed (revoke-all / list). |
| Account | `string` | `bson:"account" json:"account"` | Denormalized. |
| Scheme | `string` | `bson:"scheme" json:"scheme"` | `"legacy"` (imported RC tokens, §6.2) or `"v1"` (nextgen-issued). |
| IssuedAt | `int64` | `bson:"issuedAt" json:"issuedAt"` | ms since epoch. |
| LastUsedAt | `int64` | `bson:"lastUsedAt" json:"lastUsedAt"` | **Throttled** write (§12 Q4) — not every request. |
| ExpiresAt | `time.Time` | `bson:"expiresAt" json:"expiresAt"` | **Mongo TTL index** (`expireAfterSeconds:0`) auto-expires. |

Indexes: `_id` (token hash); `userId`; TTL on `expiresAt`.

**Why this beats the legacy array:** RC embedded a `loginTokens[]` array on the user doc → unbounded doc growth, O(n) scan per request, hence a cap. Per-session docs keyed by hash make validation O(1) regardless of session count, so the cap disappears.

---

## 5. Login & session flows

### 5.1 Password login (admin or bot)
1. Client `POST`s `{ user, password }` (plaintext **or** RC digest form) to the same public URL.
2. Auth service loads `credentials` by account; derives `sha256_hex(pw)` (or uses the supplied digest); `bcrypt.CompareHashAndPassword` against `PasswordHash`. Constant-time; uniform error on unknown-account vs bad-password.
3. On success: generate a raw token (`crypto/rand`), insert a `sessions` doc (`scheme:"v1"`, `ExpiresAt = now + loginTTL`), return the **RC-compatible envelope** (`{ status:"success", data:{ authToken, userId } }` + `X-Auth-Token`/`X-User-Id`).
4. If `RequirePasswordChange`, signal it (§5.4).

### 5.2 NATS JWT minting from a session
Present a valid session token → validate (§5.3) → mint a short-lived NATS user JWT scoped to `chat.user.{account}.>` (reusing `auth-service` grants). JWT expiry ≪ session expiry; refresh from the still-valid session without the password.

### 5.3 Session validation (every request)
`base64(sha256(token))` → cache lookup → `sessions.findOne({_id})` on miss. Hit + not expired → resolve `userId`/`account`, throttled `LastUsedAt` bump. The same hash matches both `v1` and imported `legacy` rows. TTL index reaps expired docs.

### 5.4 First-login `requirePasswordChange`
Bots can't fill a form, so this is **operator-time**: a provisioned account has `RequirePasswordChange:true`; the operator sets the real password via the change-password handler (§8), which updates `PasswordHash`, clears the flag, and revokes existing sessions.

---

## 6. Migration (legacy RC Mongo → nextgen Mongo)

### 6.1 Credential import
For each legacy `users` doc that is an admin or bot account:
- Ensure the nextgen `users` identity row exists (existing identity-sync path, not owned here).
- Insert `credentials`: `services.password.bcrypt` → `PasswordHash`, `PasswordScheme:"rc-sha256-bcrypt"`, legacy force-change flag → `RequirePasswordChange`, timestamps.

### 6.2 Live-session import (no-reauth cutover)
For each entry in `services.resume.loginTokens[]`: insert a `sessions` doc with `_id = hashedToken` (already `base64(sha256(token))`, copied verbatim), `Scheme:"legacy"`, `ExpiresAt = when + loginExpirationDays`. Because we reuse RC's hash form, an in-flight bot's existing `X-Auth-Token` validates against the imported row unchanged. The next login issues a native `v1` token.

### 6.3 Cutover source-of-truth
Decide when nextgen `credentials` becomes authoritative: (a) freeze legacy password changes at cutover, or (b) run a short legacy→nextgen credential sync until decommission (§12 Q3).

---

## 7. Service ownership
Proposal: **`auth-service` owns the `credentials` and `sessions` stores** and gains the password-login path (§5.1) plus the NATS provisioning handlers (§8). Single owner for credential/session state (per "each service owns its store"), reusing the resident JWT-signing key. The **gateway (§9) is a separate stateless service** that *reads* validated sessions via cache and never writes credential state (§9.3). Confirm (§12 Q5).

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

## 9. HTTP↔NATS gateway (the REST compatibility edge)

Legacy bots speak **REST with `X-Auth-Token`/`X-User-Id`**; nextgen speaks **NATS request/reply**. The gateway is the stateless edge that bridges them so bot code never changes. This section defines its responsibilities and the recommended, performance-first topology.

### 9.1 Responsibilities
1. **Protocol bridge** — terminate the legacy REST verb, translate to a NATS request/reply call on the mapped subject (per-verb mapping owned by the gateway track), translate the reply back to the REST body/status.
2. **Authentication boundary (for REST bots)** — validate `X-Auth-Token`+`X-User-Id` against the session store (§9.3); reject with the RC-shaped 401 on failure. This is where bot authz is enforced — *not* via NATS JWT scoping.
3. **Principal injection** — attach the validated `account`/`userId` + a request-id (`idgen.GenerateRequestID`) into the NATS request envelope/headers so downstream handlers know who is calling.
4. **Connection management** — own a small pool of **long-lived** `*nats.Conn` (never connect-per-request).
5. **Deadline & backpressure** — map the HTTP context deadline onto `Conn.RequestMsgWithContext`; apply a concurrency limit / load-shed (503) under saturation.
6. **Error mapping** — turn the `errcode` JSON envelope returned over NATS into the RC-shaped HTTP status/body (reuse `errcode` classification).
7. **Observability** — propagate request-id, emit per-request metrics + traces (mirrors the HTTP middleware rule in CLAUDE.md).

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

- *Single writer, many readers:* auth-service is the only writer of session state; gateway pods are cache readers. The gateway reads the Valkey session entry directly and falls back to an auth-service `session.validate` NATS call only on a cold miss — bounding ownership while keeping the hop off the hot path.

**(c) Throttle `LastUsedAt`** (§12 Q4): writing it per request would negate the cache. Update only when stale beyond a window (e.g. > 5 min), or drop the field.

**(d) Stateless gateway** → horizontal scale and instant Istio weight-shift/rollback (§10).

### 9.4 Build vs. buy
Build a **thin Go/Gin gateway**, consistent with the repo (auth-service is already Gin; reuse `errcode`, `idgen`, `pkg/subject`, the userstore cache pattern). Off-the-shelf options don't fit: Envoy/Istio have no NATS request/reply transport; NATS's own HTTP tools are config proxies, not RPC bridges. A purpose-built Go edge gives pooled NATS, typed envelopes, and exact RC-shape fidelity with minimal code.

---

## 10. Zero-downtime cutover (Istio, same URL, new namespace)

Old: namespace `chat`, serves `https://xx.chat.com/…`. Nextgen: new namespace (`chat-nextgen`), **same public URL**, reusing the shared Istio ingress gateway. Only the backend the gateway routes to changes, invisibly.

### 10.1 Routing
One `VirtualService` on the shared ingressgateway routes host + path to a backend `host` in the new namespace (`<gw-svc>.chat-nextgen.svc.cluster.local`), with `DestinationRule` subsets for weighting. TLS/DNS unchanged (gateway terminates the cert) → no cert churn.

### 10.2 Sequence
1. Deploy nextgen dark in `chat-nextgen`; gateway 100% → old; health-gate on `/healthz`.
2. **Canary:** weighted shift of a small % → nextgen; ramp while watching error/latency SLOs.
3. 100% → nextgen.
4. **Rollback** = shift weights back to the `chat` subset (instant).

### 10.3 Why either-stack routing is safe
nextgen honors **migrated credentials** (§6.1) *and* **migrated live legacy tokens** (§6.2, same hash form), so a request routed to either stack stays authenticated — the precondition that makes weighted routing non-disruptive.

### 10.4 Scope boundary
Clean no-downtime for the **login/session slice**. If both stacks also serve live room/message traffic in the window, dual-write/federation consistency is a **separate track**.

---

## 11. Security & rules compliance
- `PasswordHash` / raw tokens are **never** serialized (`json:"-"`) and **never** logged; `sessions._id` stores only `base64(sha256(token))`.
- Client-facing errors use `pkg/errcode` named constructors + a domain `reason` where the frontend must branch (`requirePasswordChange`, `invalidCredentials`); replied via `errnats.Reply`. Infra failures return raw wrapped errors (collapse to `internal`).
- Uniform auth-failure responses (no account-enumeration via differing errors/timing).
- New client-facing handlers → update `docs/client-api.md` in the same PR.

---

## 12. Open questions (resolve before implementation plan)

1. **Q1 — Exact legacy route + envelope.** Confirm the fork's login path (`/dev-login`?) and whether the response is the standard RC `{status,data:{authToken,userId}}` body, header-only, or both — so the gateway reproduces it byte-for-byte. (Underlying mechanics are standard RC/Meteor, §2.1.)
2. **Q2 — `loginExpirationDays`.** The legacy login-token TTL, to compute `ExpiresAt = when + TTL` for imported sessions (§6.2).
3. **Q3 — Cutover source-of-truth.** Freeze legacy password changes at cutover, or run ongoing credential sync until decommission?
4. **Q4 — `LastUsedAt`.** Throttle window (e.g. 5 min) or drop the field entirely?
5. **Q5 — Ownership.** Confirm `auth-service` owns credentials+sessions+provisioning, and the gateway is a separate read-only-cache consumer (§7, §9.3).
6. **Q6 — Cache layer.** In-pod LRU + Valkey for session validation from day one (recommended for the REST hot path), or Mongo-only until measured?
