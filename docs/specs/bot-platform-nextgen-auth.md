# Spec: Bot Platform NextGen Migration — Password Auth, Sessions & Cutover

> **Status:** DRAFT — design record for review. Forward-looking language ("the implementer must …", "Phase N") describes the proposed rollout, not shipped work. Open questions are tracked in §11; they must be resolved before this becomes an implementation plan.

*Bring password-based login (admins + bots) and durable session management to the nextgen NATS-native stack, migrating credentials from the legacy Mongo `users` collection without forcing any bot developer to change URLs, credentials, or client code, and cut over behind the shared Istio gateway with zero downtime.*

---

## 1. Goal & non-goals

### Goals
1. **Transparent migration for existing accounts.** Admins and bots that authenticate today via the legacy `/dev-login` password endpoint keep the same URL, the same credentials, and the same request/response contract. No password resets, no client changes.
2. **Unlimited concurrent sessions, constant-time validation.** Remove the legacy per-user capped token array; support any number of live sessions per account with O(1) token lookup.
3. **Net-new operator surface.** A NATS-native operator UI (+ its request/reply handlers) for admin login and bot provisioning/management (create bot, set/rotate password, list/revoke sessions).
4. **Zero-downtime cutover** behind the shared Istio gateway, same public URL, new namespace.

### Non-goals (handled elsewhere / explicitly out of scope)
- **The REST→NATS edge translation layer** (legacy REST verbs → nextgen NATS request/reply). Owned internally by another track; this spec treats it as a given component and designs only the **auth model behind it** (§11 Q1).
- The exact legacy REST **endpoint surface** and its mapping table (owned internally).
- Room/message/federation dual-write consistency during the cutover window — a separate migration track (§9.4 flags the boundary only).

---

## 2. Current state (grounded)

Verified against the repo (`auth-service/`, `pkg/userstore`, `pkg/model`, `pkg/subject`):

- **`auth-service` is OIDC/SSO-only.** `POST /auth` (`auth-service/routes.go:5`) validates an SSO token (or a dev account name in dev mode), then signs a **NATS user JWT** with scoped pub/sub permissions using the account signing key (`AUTH_SIGNING_KEY`). See `auth-service/handler.go:234-249` for the permission grants (`chat.user.{account}.>`, `chat.room.>`, `_INBOX.>`).
- **Clients talk to NATS directly** after `/auth`. There is no HTTP→NATS gateway in the repo; all RPC is NATS request/reply via `pkg/natsrouter`.
- **No password storage, no bcrypt, no session/login-token store exists anywhere.** This is a clean slate — there is no legacy auth code in the nextgen repo to refactor around.
- **Identity already works for bots.** `model.User` (`pkg/model/user.go`) carries `Account`, `SiteID`, `Roles []UserRole`, display names, etc.; `model.IsBotAccount` (`pkg/model/account.go`) classifies bots by `*.bot` suffix or `p_` prefix. `pkg/userstore` resolves any account through a pod-local LRU+singleflight cache (`FindUserByAccount`, `FindUsersByAccounts`).
- **JWT minting is reusable.** The existing `auth-service` JWT-signing path is exactly what a password login needs after credential verification.

**Implication:** identity is solved; we add only (a) a credential store, (b) a session store, (c) a password-login path that reuses JWT minting, and (d) provisioning handlers + UI.

---

## 3. Key design decisions

| Concern | Decision | Why |
|---|---|---|
| Bot/admin **identity** | Stays in the shared `users` collection (roles distinguish admin/bot/user) | Every downstream service resolves accounts through the cached `userstore`; a second identity collection would force double lookups and break display-name/federation resolution. |
| **Credentials** (password hash, `requirePasswordChange`) | New `credentials` collection, owned by the auth service | `users` is read-only shared infra cached across the fleet; a bcrypt hash must not ride in a broadly-cached identity doc, and only the auth service ever verifies a password. |
| **Sessions** (durable login tokens) | New `sessions` collection, **one doc per session keyed by `sha256(token)`** | Eliminates the legacy capped array; O(1) lookup independent of session count; TTL-indexed auto-expiry; per-session and per-account revocation. |
| **Scope** | Credentials + sessions are **account-agnostic** (admins *and* bots) | `/dev-login` authenticates admin accounts too — this is shared password-login infrastructure, not a bot-only feature. |
| **NATS JWT** | Short-lived, **minted from a valid session**, refreshed without re-entering the password | Reuses the existing `auth-service` JWT machinery; session = durable login, JWT = ephemeral capability. |
| **Token store backend** | Mongo (durable), with a Valkey read-through cache **only if** validation latency is later measured as a bottleneck | Bots are long-lived automated clients; an in-memory-only store would 401 every bot on a cache flush/restart. Durability first; cache is a measured optimization, not day-1 complexity. |

---

## 4. Data model

### 4.1 `credentials` collection

One document per password-login account (admin or bot), keyed to its `users._id`.

| Field | Type | Tags | Notes |
|---|---|---|---|
| ID | `string` | `bson:"_id"` | = `users._id` (32-char UUIDv7 hex). 1:1 with identity. |
| Account | `string` | `bson:"account" json:"account"` | Denormalized for lookup-by-account at login. Unique index. |
| PasswordHash | `string` | `bson:"passwordHash" json:"-"` | Migrated verbatim from legacy. **Never serialized to clients.** |
| PasswordScheme | `string` | `bson:"passwordScheme" json:"-"` | Tags the verification algorithm so it can evolve. Initial: `"rc-sha256-bcrypt"` (SHA-256-hex the password, then bcrypt). |
| RequirePasswordChange | `bool` | `bson:"requirePasswordChange" json:"requirePasswordChange"` | Migrated; drives first-login flow (§5.4). |
| CreatedAt | `int64` | `bson:"createdAt" json:"createdAt"` | ms since epoch. |
| UpdatedAt | `int64` | `bson:"updatedAt" json:"updatedAt"` | ms since epoch; bumped on password change. |

Indexes: unique on `account`.

### 4.2 `sessions` collection

One document per live session. **No array, no cap.**

| Field | Type | Tags | Notes |
|---|---|---|---|
| TokenHash | `string` | `bson:"_id"` | `sha256(rawToken)` hex. The lookup key — raw token is never stored. |
| UserID | `string` | `bson:"userId" json:"userId"` | = `users._id`. Indexed (revoke-all / list-sessions). |
| Account | `string` | `bson:"account" json:"account"` | Denormalized. |
| Scheme | `string` | `bson:"scheme" json:"scheme"` | `"legacy"` (migrated live tokens, §6.2) or `"v1"` (issued by nextgen). |
| IssuedAt | `int64` | `bson:"issuedAt" json:"issuedAt"` | ms since epoch. |
| LastUsedAt | `int64` | `bson:"lastUsedAt" json:"lastUsedAt"` | ms since epoch; refreshed on validation (throttled write, §11 Q4). |
| ExpiresAt | `time.Time` | `bson:"expiresAt" json:"expiresAt"` | **Mongo TTL index** drives auto-expiry (matches legacy login-expiration). |

Indexes: `_id` (token hash, implicit); `userId`; TTL index on `expiresAt` (`expireAfterSeconds: 0`).

**Why this beats the legacy array:** legacy embedded a hashed-token array on the user doc, forcing (a) unbounded doc growth, (b) O(n) scans per request, (c) a cap to bound growth. Per-session docs keyed by hash make validation O(1) regardless of session count, so the cap simply disappears.

---

## 5. Login & session flows

### 5.1 Password login (admin or bot) — `/dev-login` equivalent

1. Client `POST`s `{ account, password, natsPublicKey? }` to the same public URL.
2. Auth service loads `credentials` by `account`; verifies `password` against `PasswordHash` per `PasswordScheme` (`rc-sha256-bcrypt`). Constant-time compare; uniform error on miss vs. bad password.
3. On success: generate a raw session token (`crypto/rand`), insert a `sessions` doc (`scheme:"v1"`, `ExpiresAt = now + loginTTL`), and return the **legacy-compatible envelope** (`{ data: { authToken, userId } }` / `X-Auth-Token` + `X-User-Id` — exact shape pinned in §11 Q1).
4. If `RequirePasswordChange` is set, the response signals it (§5.4).

### 5.2 NATS JWT minting from a session

When the (internal edge or NATS-native) client needs to act over NATS:
- Present a valid session token → auth service validates it (§5.3) → mints a short-lived NATS user JWT scoped to `chat.user.{account}.>` (reusing `auth-service/handler.go` grants) → returns it.
- JWT expiry ≪ session expiry; the client refreshes the JWT from the still-valid session **without** re-entering the password.

### 5.3 Session validation (every request)

`sha256(token)` → `sessions.findOne({_id})`. Miss → check legacy-token fallback (§6.2). Hit + not expired → resolve `userId`/`account`, (throttled) bump `LastUsedAt`. Expired docs are removed by the TTL index.

### 5.4 First-login `requirePasswordChange`

Bots are automated and cannot fill a form, so this is an **operator-time** step, not bot-runtime:
- A newly provisioned account has `RequirePasswordChange:true`. The operator sets the real password via the change-password handler (§8), which updates `PasswordHash`, clears the flag, bumps `UpdatedAt`, and (recommended) revokes existing sessions for that account.

---

## 6. Migration (legacy Mongo → nextgen Mongo)

### 6.1 One-time credential import
For each legacy `users` doc that is an admin or bot account:
- Ensure the identity row exists in nextgen `users` (via the existing identity sync path — not owned here).
- Insert `credentials`: copy `services.password.bcrypt` → `PasswordHash`, set `PasswordScheme:"rc-sha256-bcrypt"`, carry the legacy password-change flag → `RequirePasswordChange`, set timestamps.

### 6.2 Live-session import (no-reauth cutover)
To avoid logging in-flight bots out at cutover, also import **currently-valid** legacy login tokens (already stored hashed) into `sessions` with `Scheme:"legacy"` and the legacy expiry mapped to `ExpiresAt`. Validation (§5.3) accepts these transparently; the next login issues a native `v1` token. (Exact legacy token storage shape to confirm — §11 Q2.)

### 6.3 Cutover source-of-truth
Decide the moment nextgen `credentials` becomes authoritative. If passwords can still change on legacy during the dual-run window, either (a) freeze legacy password changes at cutover, or (b) run a short ongoing credential sync legacy→nextgen until decommission (§11 Q3).

---

## 7. Service ownership

Proposal: **`auth-service` owns the `credentials` and `sessions` stores** and gains:
- the password-login HTTP path (§5.1) alongside the existing OIDC `/auth`,
- the NATS request/reply **provisioning handlers** (§8) for the operator UI (a service may serve both HTTP and NATS).

This keeps a single owner for credential/session state (per the "each service owns its store" rule) and reuses the JWT-signing key already resident in `auth-service`. Alternative — a sibling `botadmin` service for provisioning — is rejected unless it can avoid two services writing the same collections (§11 Q5).

---

## 8. Operator UI + NATS request/reply handlers

The operator UI is a **NATS-native frontend** (gets a JWT, issues request/reply) — confirmed as the nextgen client pattern. Each operation = one new `pkg/natsrouter` handler returning a typed `*errcode.Error`, documented in `docs/client-api.md` (§2.2 / a new admin section). Proposed operations:

| Operation | Purpose | Writes |
|---|---|---|
| admin login | Human admin password login | `sessions` |
| create bot | Provision a bot identity + initial password | `users` + `credentials` (`RequirePasswordChange:true`) |
| set/rotate password | Change password, clear flag | `credentials`, revoke `sessions` |
| list sessions | Show a bot's live sessions | reads `sessions` by `userId` |
| revoke session / revoke all | Kill one or all sessions | `sessions` |

Subjects follow the `chat.user.{account}.request.…` convention via `pkg/subject` builders (exact subjects in the implementation plan). `docs/client-api.md` must be updated in the same PR (project rule for client-facing handlers).

---

## 9. Zero-downtime cutover (Istio, same URL, new namespace)

Old stack: namespace `chat`, serves `https://xx.chat.com/dev-login`. Nextgen: new namespace (e.g. `chat-nextgen`), **same public URL**, reusing the shared Istio ingress gateway. Bot developers cannot change URLs — so only the *backend the gateway routes to* changes, invisibly.

### 9.1 Cross-namespace routing (Istio)
One `VirtualService` on the shared ingressgateway routes host `xx.chat.com` + path `/dev-login` to a backend `host` in the new namespace (`<svc>.chat-nextgen.svc.cluster.local`), with `DestinationRule` subsets for weighting. TLS/DNS unchanged (gateway terminates the cert) → no cert churn.

### 9.2 Sequence
1. Deploy nextgen dark in `chat-nextgen`; gateway 100% → old. Health-gate on `/healthz`.
2. **Canary:** weighted `VirtualService` shifts a small % of `/dev-login` traffic → nextgen; ramp while watching error/latency SLOs.
3. 100% → nextgen.
4. **Rollback** at any point = shift weights back to the `chat`-ns subset (instant).

### 9.3 Why either-stack routing is safe during canary
Because nextgen honors **migrated credentials** (§6.1) *and* **migrated live legacy tokens** (§6.2), a request routed to either stack stays authenticated. This is the precondition that makes weighted routing non-disruptive — without it, canary would 401 any request that crossed stacks.

### 9.4 Scope boundary
This gives clean no-downtime for the **login/session slice**. If both stacks also serve live room/message traffic during the window, dual-write/federation consistency is a **separate track** — not solved by the gateway flip alone.

---

## 10. Security & rules compliance
- `PasswordHash` / raw tokens are **never** serialized (`json:"-"`) and **never** logged (CLAUDE.md secret-logging rule). `sessions._id` stores only `sha256(token)`.
- Client-facing errors use `pkg/errcode` named constructors + a domain `reason` where the frontend must branch (e.g. `requirePasswordChange`, `invalidCredentials`); replied via `errnats.Reply`. Infra failures return raw wrapped errors (collapse to `internal`).
- Uniform auth-failure responses (no account-enumeration via differing errors/timing).
- New client-facing handlers → update `docs/client-api.md` in the same PR.

---

## 11. Open questions (resolve before implementation plan)

1. **Q1 — Bot-facing contract.** Confirm the exact legacy `/dev-login` request fields and response envelope (`X-Auth-Token`/`X-User-Id` headers vs. `{data:{authToken,userId}}` body) that nextgen must reproduce byte-for-byte, and confirm the REST→NATS edge is the component presenting it (this spec designs the auth model behind that edge).
2. **Q2 — Legacy token storage shape.** The exact legacy collection/field for live login tokens (RC-style `services.resume.loginTokens[]`?) and their hashing, so §6.2 can import them.
3. **Q3 — Cutover source-of-truth.** Freeze legacy password changes at cutover, or run ongoing credential sync until decommission?
4. **Q4 — `LastUsedAt` write throttling.** Bumping on every request is a write per request; throttle (e.g. only if stale > N min) or drop `LastUsedAt`?
5. **Q5 — Service ownership.** Confirm `auth-service` owns credentials+sessions+provisioning (HTTP + NATS), vs. a sibling service.
6. **Q6 — Valkey cache.** Accept Mongo-only sessions for v1 (cache later only if measured), or require the Valkey read-through cache from day one?
