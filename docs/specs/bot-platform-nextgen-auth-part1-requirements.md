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
- **Part 3 — Bot Platform Components Guide** *(planned; to be provided)* — the bot-platform components, their responsibilities, and how they fit the auth design above.

---

## 3. Architecture decision

Where do bot auth + the REST edge live? Two options (full breakdown in **Part 2 §7**):

- **Option A — Extend `auth-service` (recommended).** Add password login, the `credentials`/`sessions` stores, header-validation middleware, and admin RPCs to the existing auth service.
- **Option B — New `bot-gateway` service.** A dedicated service for bot password auth + REST→NATS translation + admin ops; `auth-service` stays pure-SSO.

| Criterion | Option A | Option B |
|---|---|---|
| Time to implement | **faster** | slower |
| Operational complexity | **lower** | higher |
| Separation of concerns | poor | **good** |
| Risk to human auth | higher | **lower** |
| Long-term maintainability | moderate | **better** |
| Team ownership | single owner | split ownership |

**Recommendation: Option A** — fastest path, reuses the existing JWT-minting code, single-team ownership; the human-auth-regression risk is contained by isolating bot code in its own files and a strong test gate. **Decision pending sign-off.**

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

---

## 7. Security
- **Never log** tokens or passwords (or their hashes).
- **Timing-safe credential comparison** (run bcrypt even on unknown accounts; uniform error/timing — no account enumeration).
- **Rate limiting:** 5 failed attempts → **15-minute lockout**.
- **HTTPS only.**

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
