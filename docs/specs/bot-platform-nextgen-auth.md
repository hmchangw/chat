# Spec: Bot Platform NextGen Migration — Password Auth, Sessions & Cutover

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
| ID | `string` | `bson:"_id"` | = `users._id` (32-char UUIDv7 hex). 1:1 with identity. |
| Account | `string` | `bson:"account" json:"account"` | Denormalized for login lookup. Unique index. |
| LegacyUserID | `string` | `bson:"legacyUserId,omitempty" json:"-"` | Legacy Meteor `_id` (17-char). Present only if nextgen `_id` ≠ legacy id; used to match the bot-supplied `X-User-Id` (Q9). Omit if identity-sync preserves the legacy `_id`. |
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
| UserID | `string` | `bson:"userId" json:"userId"` | = nextgen `users._id`. Indexed (revoke-all / list). |
| LegacyUserID | `string` | `bson:"legacyUserId,omitempty" json:"-"` | Denormalized from `credentials`; matched against `X-User-Id` during validation when ids differ (Q9). |
| Account | `string` | `bson:"account" json:"account"` | Denormalized. |
| Scheme | `string` | `bson:"scheme" json:"scheme"` | `"legacy"` (imported RC tokens, §6.2) or `"v1"` (nextgen-issued). |
| IssuedAt | `int64` | `bson:"issuedAt" json:"issuedAt"` | ms since epoch. |
| LastUsedAt | `int64` | `bson:"lastUsedAt" json:"lastUsedAt"` | **Throttled** write (§12 Q4) — not every request. |
| ExpiresAt | `*time.Time` | `bson:"expiresAt,omitempty" json:"expiresAt,omitempty"` | **Sliding idle expiry** (§5.5): pushed to `now + idleWindow` on the throttled use-bump. `nil`/absent ⇒ never expires (pure-infinite mode). |

Indexes: `_id` (token hash); `userId`; **partial TTL** on `expiresAt` (`expireAfterSeconds:0`, partial filter `{expiresAt:{$exists:true}}` so null/infinite sessions are never reaped).

**Why this beats the legacy array:** RC embedded a `loginTokens[]` array on the user doc → unbounded doc growth, O(n) scan per request, hence a cap. Per-session docs keyed by hash make validation O(1) regardless of session count, so the cap disappears.

---

## 5. Login & session flows

### 5.1 Password login (admin or bot)
1. Client `POST`s `{ user, password }` (plaintext **or** RC digest form) to the same public URL.
2. Auth service loads `credentials` by account; derives `sha256_hex(pw)` (or uses the supplied digest); `bcrypt.CompareHashAndPassword` against `PasswordHash`. Constant-time; uniform error on unknown-account vs bad-password.
3. On success: generate a raw token (`crypto/rand`), insert a `sessions` doc (`scheme:"v1"`, `ExpiresAt = now + idleWindow`, §5.5), return the **RC-compatible envelope** (`{ status:"success", data:{ authToken, userId } }` + `X-Auth-Token`/`X-User-Id`).
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
There is **no bot-facing resume verb**. A REST bot logs in once (username + password) and reuses its `X-Auth-Token` on every subsequent call — the header *is* session reuse. The only resume primitive is internal (§5.2): a native client or the gateway exchanges a still-valid session token for a fresh short-lived NATS JWT, without re-entering the password. Session token = durable resume credential; JWT = ephemeral capability.

---

## 6. Migration (legacy RC Mongo → nextgen Mongo)

### 6.1 Credential import
For each legacy `users` doc that is an admin or bot account:
- Ensure the nextgen `users` identity row exists (existing identity-sync path, not owned here).
- Insert `credentials`: `services.password.bcrypt` → `PasswordHash`, `PasswordScheme:"rc-sha256-bcrypt"`, legacy force-change flag → `RequirePasswordChange`, timestamps.

### 6.2 Live-session import (no-reauth cutover)
For each entry in `services.resume.loginTokens[]`: insert a `sessions` doc with `_id = hashedToken` (already `base64(sha256(token))`, copied verbatim), `Scheme:"legacy"`, and `ExpiresAt = now + idleWindow` (sliding, §5.5 — legacy `when` is ignored since sessions are effectively infinite). Because we reuse RC's hash form, an in-flight bot's existing `X-Auth-Token` validates against the imported row unchanged. The next login issues a native `v1` token.

### 6.3 Cutover source-of-truth — freeze legacy, one-time import (decided)
At canary start, make legacy credentials **read-only** (disable password change / admin user-mgmt on the old stack) and run the one-time `credentials` import; nextgen is authoritative from that moment. Frozen-and-matched credentials mean a request landing on either stack authenticates identically (§10.3). A legacy→nextgen credential sync is the fallback **only** if a multi-week dual-run with live password changes is required.

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

## 12. Open questions & decisions

> **These stay OPEN until you confirm each against the internal codebase (§22).** "Recommended" entries are the proposed answer with rationale — they are not locked. Nothing here is final until signed off.

### Recommended — pending your confirmation
- **Q1 — Login contract.** Username + password only (plaintext or RC `{digest, algorithm:"sha-256"}`). **No bot-facing `resume` verb**; the only resume primitive is the internal session→JWT exchange (§5.2, §5.6). *Confirm:* the fork's exact path (`/dev-login`?) and whether the success envelope is body, header, or both — needed for byte-for-byte gateway fidelity.
- **Q2 — Session lifetime.** Effectively **infinite via sliding idle expiry** (§5.5), refreshed on the throttled use-bump; legacy `when`/`loginExpirationDays` ignored on import. Revocation is the kill switch. *Confirm:* sliding-infinite acceptable vs. literally-never.
- **Q3 — Cutover source-of-truth.** **Freeze legacy credentials at canary start + one-time import**; nextgen authoritative thereafter (§6.3). *Confirm:* dual-run window length; whether live password changes must keep working on legacy.
- **Q4 — `LastUsedAt`.** Throttled write (recommend ≈5 min window), which doubles as the sliding-expiry refresh (§5.5). *Confirm:* window value, or drop the field.
- **Q5 — Ownership.** **`auth-service` owns** credentials + sessions + JWT minting + provisioning; the **gateway is a separate stateless read-only cache consumer** (§7, §9.3). *Confirm:* gateway is built by the internal track and can read the Valkey session cache directly.
- **Q6 — Cache layer.** In-pod LRU + **Valkey from day one** for the session-validation hot path (§9.3). *Confirm:* Valkey cluster available to the gateway/auth-service in prod.

### Still fully open (need an answer)
- **Q1a — Exact `/dev-login` path + success-envelope placement** (body vs. headers vs. both), to lock gateway response fidelity.
- **Q7 — `idleWindow` value.** Recommend 180d; confirm.
- **Q8 — Bot auth mechanism.** Do bots authenticate with username/password each start, or with a long-lived **Personal Access Token**? RC stores PATs in the same `services.resume.loginTokens[]` (with `type:"personalAccessToken"`, `name`). This changes which migration path (§6.2) carries the bots — confirm against real bot accounts (§22).
- **Q9 — Identity ID mapping.** Legacy Meteor `users._id` (17-char) vs. nextgen `users._id` (UUIDv7 hex). Bots send the legacy id as `X-User-Id`; the session/credentials must carry `LegacyUserID` to match it (or identity-sync must preserve the legacy `_id`). Confirm which (§22).

---

## 13. Go types (proposed)

`pkg/model` (shared domain types, both `json` + `bson` tags per house rule):

```go
// model/credential.go
type Credential struct {
    ID                    string `json:"id"                    bson:"_id"`
    Account               string `json:"account"               bson:"account"`
    LegacyUserID          string `json:"-"                     bson:"legacyUserId,omitempty"`
    PasswordHash          string `json:"-"                     bson:"passwordHash"`
    PasswordScheme        string `json:"-"                     bson:"passwordScheme"`
    RequirePasswordChange bool   `json:"requirePasswordChange" bson:"requirePasswordChange"`
    CreatedAt             int64  `json:"createdAt"             bson:"createdAt"`
    UpdatedAt             int64  `json:"updatedAt"             bson:"updatedAt"`
}

// model/session.go
type Session struct {
    TokenHash    string     `json:"-"                    bson:"_id"`          // base64(sha256(token))
    UserID       string     `json:"userId"               bson:"userId"`
    LegacyUserID string     `json:"-"                    bson:"legacyUserId,omitempty"`
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
        AuthToken string `json:"authToken"`
        UserID    string `json:"userId"`
        JWT       string `json:"jwt,omitempty"`      // native clients only
    } `json:"data"`
}
```

Gateway→handler principal injection rides as **NATS message headers** (not body, so the per-verb body mapping is untouched):
`X-Chat-Account`, `X-Chat-User-Id`, `X-Request-ID`.

---

## 14. Algorithms (precise)

**Token hashing (one function, legacy + v1):**
`tokenHash = base64.StdEncoding(sha256(rawToken))` — must match Meteor `Accounts._hashLoginToken` byte-for-byte (verify with a golden fixture, §18).

**Password verify:**
```
input = digest (if {digest,algorithm:"sha-256"} supplied)  else  hex(sha256(plaintext))
ok    = bcrypt.CompareHashAndPassword(cred.PasswordHash, input) == nil
```
Run the bcrypt compare even on unknown-account (against a dummy hash) to keep timing uniform.

**Session validation (gateway hot path):**
```
h := tokenHash(xAuthToken)
s := valkey.Get(h)                       // hot path
if miss: s = nats.Request(auth.session.validate, h)   // cold path; auth-svc reads Mongo + warms Valkey
if s == nil           -> 401 invalidCredentials
if s.ExpiresAt != nil && now > s.ExpiresAt -> 401 sessionExpired
if s.LegacyUserID != "" && xUserId != s.LegacyUserID  -> 401 invalidCredentials
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

**auth-service** (additions): `SESSION_IDLE_WINDOW` (`envDefault:"4320h"` ≈180d), `SESSION_LASTUSED_THROTTLE` (`"5m"`), `JWT_TTL` (`"15m"`), `BCRYPT_COST` (`"10"` — match legacy), `VALKEY_ADDRS` (required), Mongo URI/DB (required). Reuses existing `AUTH_SIGNING_KEY`.

**gateway** (new service): `NATS_URL` + service-account creds (required), `GATEWAY_NATS_POOL_SIZE` (`"4"`), `GATEWAY_REQUEST_TIMEOUT` (`"10s"`), `GATEWAY_MAX_CONCURRENCY` (`"500"`), `VALKEY_ADDRS` (required), `SESSION_CACHE_TTL` (`"5m"`), `PORT` (`"8080"`). Secrets never defaulted (house rule).

---

## 17. Observability (metrics)
- `auth_login_total{result}`, `auth_session_validate_total{source=cache|mongo,result}`, `auth_session_validate_latency_seconds`, `auth_active_sessions` (gauge).
- gateway: `gw_request_duration_seconds{route,status}`, `gw_nats_request_total{result=ok|timeout|err}`, `gw_session_cache_hits_total{hit}`.
- Logging: `log/slog` JSON, request-id propagated; **never** log tokens / password input / hashes.

---

## 18. Test plan (TDD, ≥80% / 90% core)
- **Golden hash fixture** (first test): a known `(rawToken → base64(sha256))` pair proving parity with Meteor — gate before any store code.
- **Password verify** (table): plaintext-correct, digest-correct, wrong password, unknown account (uniform timing/error), `requirePasswordChange` surfaced.
- **Session lifecycle** (unit, mocked store): create, validate hit, expired, `X-User-Id` mismatch, throttled-bump skip vs. apply, revoke, revoke-all.
- **Provisioning handlers** (table, mocked store): create bot (happy/`accountExists`/`notBotAccount`), set-password (clears flag + revokes), non-admin caller → `forbiddenNotAdmin`.
- **Gateway** (unit): principal-header injection, `errcode`→HTTP status mapping, timeout→503, deadline propagation.
- **Integration** (`//go:build integration`, `pkg/testutil`): `credentials`/`sessions` Mongo store incl. partial-TTL index behavior; Valkey read-through + invalidation; **migration script** against a Mongo seeded with legacy-RC-shaped docs (password + loginTokens + a PAT entry).

---

## 19. Implementation phases (each = TDD + own PR)
1. `model` types + `credentials`/`sessions` stores (Mongo) + indexes + mocks.
2. Password verify + session lifecycle + HTTP login/refresh in auth-service (reuse JWT minting).
3. Valkey read-through cache + invalidation.
4. Migration script (credential + live-token/PAT import) with `--dry-run` + verify mode.
5. Gateway service (REST↔NATS, principal injection, error mapping, pooled conn, load-shed).
6. Operator provisioning NATS handlers + admin authz + `docs/client-api.md`.
7. Istio routing + canary runbook (separate from app PRs).
8. Operator UI frontend (likely a separate repo/track).

---

## 20. `docs/client-api.md` delta
- **§2.2** — add the password-login HTTP endpoint (request: `user`+`password` plaintext/digest, optional `natsPublicKey`; response envelope; error cases incl. `requirePasswordChange`).
- **New admin section** — the five provisioning RPCs (§15) with request/response field tables + JSON examples + error reasons.
- Updated in the **same PR** as the handlers (house rule).

---

## 21. Risks
- **PAT vs password (Q8)** — if bots use PATs, §6.2 must import PAT `loginTokens`, and "rotate password revokes sessions" must not silently kill PAT-based bots. Resolve before phase 4.
- **ID mapping (Q9)** — wrong `X-User-Id` matching locks every bot out at cutover. Gate phase 5 on a confirmed answer.
- **Infinite sessions** — standing bearer tokens; mitigated by sliding reap + revocation, but operators must have working revoke before go-live.
- **Cache/Mongo divergence on revoke** — a revoke that updates Mongo but not the cache leaves a token live until cache TTL; prefer explicit invalidation over TTL-only.

---

## 22. Verification checklist (run against the internal codebase before implementation)

Tick each before promoting this spec to a plan. These are the assumptions the design rests on.

**Legacy RC fork**
- [ ] Exact login route + method (is it `/dev-login`? path, verb). **(Q1a)**
- [ ] Login request field names (`user` vs `username`) and whether digest-form is used by any client. **(Q1)**
- [ ] Login success envelope: body shape, header set (`X-Auth-Token`/`X-User-Id`), status code, and the **error** body shape. **(Q1a)**
- [ ] Password storage really is `services.password.bcrypt` = `bcrypt(sha256_hex(pw))`, and the bcrypt cost. Spot-check one account by test-comparing. **(Q4/§14)**
- [ ] Are there admin/bot accounts with **no** local password (SSO/LDAP-only) that can't migrate to password login?
- [ ] Login-token storage: `services.resume.loginTokens[]`, `hashedToken = base64(sha256(raw))`, field names. **(Q2/§6.2)**
- [ ] **Do bots use password login or Personal Access Tokens?** If PAT, capture the PAT entry shape (`type`, `name`). **(Q8)**
- [ ] Token hashing parity: confirm a real token's `hashedToken` equals our `base64(sha256(token))`. **(§14 golden fixture)**
- [ ] `loginExpirationDays` (only matters if you reject sliding-infinite). **(Q2)**

**Nextgen / identity**
- [ ] Does identity-sync already populate **admin + bot** accounts in nextgen `users`, with `roles`? **(§6.1)**
- [ ] Is the nextgen `users._id` the **same** value as the legacy Meteor `_id`, or different? If different, `LegacyUserID` is mandatory. **(Q9)**
- [ ] `model.IsBotAccount` covers every real bot naming pattern in production.

**Infra / platform**
- [ ] Valkey cluster reachable from auth-service **and** the gateway in prod. **(Q6)**
- [ ] The HTTP↔NATS **gateway is owned/built by the internal track**, and can read the Valkey session cache (or must go through `auth.session.validate`). **(Q5)**
- [ ] Shared Istio ingress gateway + who owns the `VirtualService`/`DestinationRule`; the new namespace name; cert ownership. **(§10)**
- [ ] A NATS **service account** for the gateway with publish rights on `chat.user.*.request.>` (and that user JWTs remain scoped to self). **(§9.3)**

**Decisions to sign off (§12)**
- [ ] Q2 sliding-infinite · [ ] Q3 freeze-legacy · [ ] Q4 throttle window · [ ] Q5 ownership · [ ] Q6 Valkey day-one · [ ] Q7 `idleWindow`=180d
