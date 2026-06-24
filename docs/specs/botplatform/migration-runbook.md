# Bot Platform NextGen — Schema & Migration Runbook

> **Operational companion** to the design spec:
> - [Bot Platform NextGen Auth Migration](./auth.md) — combined Parts I (requirements) + II (technical design) + III (components/integration)
> - [Bot Traffic Isolation](./traffic-isolation.md) — combined Parts I (requirements) + II (technical design)
>
> **Audience:** SRE/ops running the migration; engineers writing the migration jobs. Designed in AS-IS vs TO-BE format so the delta and the mechanic are explicit at every step.
>
> **Status:** DRAFT — pending verification against the legacy v2 Mongo dump and the current nextgen `chat` DB.

---

## 1. Scope

Three Mongo collections are touched by this migration:

| Collection | AS-IS location | TO-BE location | Status |
|---|---|---|---|
| **`users`** | Legacy RC DB (Meteor) + nextgen `chat` DB | Nextgen `chat` DB (siteA, siteB, …) | **Modified** — provisioning fields confirmed/added |
| **`credentials`** | (none — embedded inside legacy `users`) | Nextgen `chat` DB, owned by `botplatform-service` | **NEW** |
| **`sessions`** | (none — embedded as `loginTokens[]` array inside legacy `users`) | Nextgen `chat` DB, owned by `botplatform-service` | **NEW** |

No other collections are added or changed by this design.

---

## 2. Collection: `users` (modified, shared)

### AS-IS — legacy RC Mongo (source of migration)

Meteor/RC schema, single-site, monolithic. Auth and identity entangled in one doc.

```js
// Database: meteor (legacy RC)
// Collection: users
{
  _id:        "<17-char Meteor base62>",          // preserved into nextgen verbatim
  username:   "alice"  | "xxx.bot",
  name:       "Alice Smith",
  emails:     [{ address: "...", verified: true }],
  active:     true,
  roles:      ["bot"] | ["admin"] | ["user"],
  createdAt:  ISODate(...),
  // --- the parts we will extract ---
  services: {
    password: {
      bcrypt: "$2b$10$..."                         // → migrates to credentials.passwordHash
    },
    resume: {
      loginTokens: [
        { when: ISODate(...),
          hashedToken: "b64sha256...",             // → migrates to sessions._id
          type: undefined                          // regular login token → import
        },
        { ..., type: "personalAccessToken"         // PAT → SKIP (humans only)
        },
        ...                                        // capped at 50 in RC
      ]
    }
  },
  requirePasswordChange: true | false              // → migrates to credentials.RequirePasswordChange
}
```

**Indexes (legacy):** `_id` (primary), `username` (unique), `emails.address` (sparse unique).

**Properties:** single-site, monolithic, no `siteId` field anywhere.

### AS-IS — nextgen `chat` DB (current state pre-bot-platform)

After PR #295 (portal-service + provisioning gate), nextgen `users` has the provisioning-related fields. Per-site DB (one `chat` DB per site).

```js
// Database: chat (nextgen, per site)
// Collection: users
{
  _id:       "<17-char Meteor base62>",            // imported verbatim from legacy
  account:   "alice"  | "xxx.bot",                 // = legacy username
  siteId:    "siteA",                              // (PR #295) home site of this account
  roles:     ["bot"] | ["admin"] | ["user"],
  name:      "Alice Smith",
  active:    true,
  createdAt: 1718500000000                         // ms since epoch
  // NO password fields, NO loginTokens — those move to credentials + sessions
}
```

**Indexes (existing):** `_id` (primary), `account` (unique).

**Open against PR #295 (to verify):** does it add a compound index `{account: 1, siteId: 1}`? The provisioning gate needs it to hit an index, not a collscan.

### TO-BE — nextgen `chat` DB (post-rollout)

```js
{
  _id:           "<17-char Meteor base62>",
  account:       "alice" | "xxx.bot",
  siteId:        "siteA",
  roles:         ["bot"],
  name:          "Alice Smith",
  active:        true,
  createdAt:     1718500000000,
  schemaVersion: "v1"                              // NEW — forward-compat hook
}
```

**Indexes (TO-BE):**
- `_id` (primary, unchanged)
- `account` (unique, unchanged)
- **`{ account: 1, siteId: 1 }`** (NEW unique compound, gate the provisioning lookup) — confirm vs PR #295

**Owner:** shared. Read by botplatform-service (provisioning gate), auth-service (provisioning gate), portal-service (lookup). Written by the legacy → nextgen users-sync only.

### Delta

| Change | Direction | Notes |
|---|---|---|
| `services.password.*` | **Removed** | Extracted to `credentials` collection |
| `services.resume.loginTokens[]` | **Removed** | Extracted to `sessions` collection |
| `requirePasswordChange` | **Removed** | Extracted to `credentials` collection |
| `schemaVersion: "v1"` | **Added** | Forward-compat |
| `{account, siteId}` index | **Added** (if not already by PR #295) | Provisioning-gate performance |

### Migration mechanic

`users` itself is **not** migrated in this rollout — PR #295 + the live users-sync own that. Our work just **reads** from the existing nextgen `users` collection for the provisioning gate. The only thing this design adds to `users` is the `{account, siteId}` index (confirm + add if missing).

```js
// Index creation (idempotent — safe to run on every deploy)
db.users.createIndex(
  { account: 1, siteId: 1 },
  { unique: true, name: "account_site_unique" }
)
```

### Verification

```js
// Provisioning-gate lookup uses the new index
db.users.find({ account: "xxx.bot", siteId: "siteA" }).explain("queryPlanner")
// Expect: IXSCAN on account_site_unique, not COLLSCAN
```

---

## 3. Collection: `credentials` (NEW)

### AS-IS

Does not exist. Equivalent data lives in `legacy_users.services.password` (legacy RC).

### TO-BE — nextgen `chat` DB

```js
// Database: chat (per site)
// Collection: credentials
// Owner: botplatform-service
{
  _id:                   "<17-char Meteor base62>",     // = users._id, preserved
  account:               "xxx.bot",                      // login username
  passwordHash:          "$2b$10$...",                   // bcrypt(sha256_hex(plaintext))
                                                          // imported verbatim from legacy
  passwordScheme:        "rc-sha256-bcrypt",             // marks the verify-path family
  requirePasswordChange: false,
  schemaVersion:         "v1",
  createdAt:             1718500000000,                  // ms since epoch
  updatedAt:             1718500000000                   // bumped on password change
}
```

**Indexes:**
- `_id` (primary, auto)
- `account` (unique, name: `account_unique`) — login lookup
- *(Optional, future)* JSON Schema validator: `bsonType: "object"`, required fields, type checks. Recommend yes; mechanical.

**Field tags & access rules** — see auth.md Part II §4.1; the canonical Go struct is `credentials.Credentials` (one source of truth). `passwordHash` is `json:"-"`, never serialized, never logged.

### Delta vs AS-IS

Everything is new. The legacy field-by-field origin:

| TO-BE field | AS-IS source (legacy RC) | Transformation |
|---|---|---|
| `_id` | `users._id` | Verbatim copy — 17-char Meteor ID, preserved |
| `account` | `users.username` | Verbatim copy |
| `passwordHash` | `users.services.password.bcrypt` | **Verbatim copy — never rehash** (we don't have the plaintext) |
| `passwordScheme` | n/a | Constant `"rc-sha256-bcrypt"` for imported rows |
| `requirePasswordChange` | `users.requirePasswordChange` (or absent → false) | Boolean copy |
| `createdAt` | `users.createdAt` → ms epoch | Type conversion (ISODate → int64) |
| `updatedAt` | `users.createdAt` (initial) | Same as createdAt on import |
| `schemaVersion` | n/a | Constant `"v1"` |

### Migration mechanic

```python
# Pseudocode — idempotent, resumable, runs once per site
for user in legacy_users.find(
    { "services.password.bcrypt": { "$exists": True } },
    sort=[("_id", 1)],               # stable order for resume
    cursor_batch_size=500
):
    nextgen_chat.credentials.update_one(
        { "_id": user["_id"] },
        { "$setOnInsert": {           # idempotent: only writes on first insert
            "_id":                   user["_id"],
            "account":               user["username"],
            "passwordHash":          user["services"]["password"]["bcrypt"],
            "passwordScheme":        "rc-sha256-bcrypt",
            "requirePasswordChange": user.get("requirePasswordChange", False),
            "createdAt":             to_epoch_ms(user["createdAt"]),
            "updatedAt":             to_epoch_ms(user["createdAt"]),
            "schemaVersion":         "v1",
          }
        },
        upsert=True
    )
    checkpoint(user["_id"])           # persist cursor for resume
```

**Properties:**
- **Idempotent:** `$setOnInsert` + upsert — re-running the job is safe.
- **Resumable:** cursor checkpoints to `migration_state` collection; restart picks up after last `_id` processed.
- **Bounded memory:** batched cursor, no full collection in memory.
- **Conflict policy:** live users-sync **must stay disabled until the bulk import finishes** (enforced by the §5 ordering — sync activates at step 4, after credentials+sessions import in steps 2+3). If concurrent operation is ever required, the sync path **must upsert** missing rows; a plain `$set` on an absent document is silently dropped (no-op), so the sync would lose writes for any account the bulk import hasn't reached yet.

### Verification

```js
// 1. Counts match (allowing for users without passwords — humans without bcrypt)
nextgenCount = db.credentials.countDocuments({})
legacyCount  = legacy.users.countDocuments({ "services.password.bcrypt": { "$exists": true } })
assert(nextgenCount === legacyCount, "credential count mismatch")

// 2. Spot-check a known bot
bot = db.credentials.findOne({ account: "xxx.bot" })
assert(bot.passwordHash.startsWith("$2b$10$"), "bcrypt format")
assert(bot.passwordScheme === "rc-sha256-bcrypt", "scheme")

// 3. No plaintext anywhere (sanity)
assert(db.credentials.findOne({ password: { $exists: true } }) === null, "no plaintext field")
```

### Rollback

Drop the collection. Legacy `users.services.password.bcrypt` is still intact; restart import after fixing.

```js
db.credentials.drop()
```

---

## 4. Collection: `sessions` (NEW)

### AS-IS

Does not exist as a collection. Equivalent data lives as `legacy_users.services.resume.loginTokens[]` — a capped array (50 entries) embedded on each user doc.

### TO-BE — nextgen `chat` DB

```js
// Database: chat (per site)
// Collection: sessions
// Owner: botplatform-service
{
  _id:           "b64hash...",        // token hash, function depends on scheme (§4.3 auth Part II)
                                       //   "legacy":  base64(sha256(rawToken))
                                       //   "v1":      base64(HMAC-SHA-256(server_secret, rawToken))
  userId:        "<17-char Meteor ID>",
  account:       "xxx.bot",           // denormalized — validate returns it directly
  siteId:        "siteA",             // home site — set at issue, never changes
  scheme:        "legacy" | "v1",
  issuedAt:      1718500000000,
  lastUsedAt:    1718500000000,       // throttled write (5-min window)
  expiresAt:     ISODate(...),        // sliding idle expiry; null = never expires
  schemaVersion: "v1"
}
```

**Indexes:**
- `_id` (primary, auto) — token hash lookup, the hot path
- `userId` (name: `userId_lookup`) — revoke-all / list-sessions for a bot
- **Partial TTL** on `expiresAt`: `expireAfterSeconds: 0`, `partialFilterExpression: { expiresAt: { $exists: true } }` — only non-null `expiresAt` rows are reaped; pure-infinite sessions (null `expiresAt`) survive forever
- *(Optional, future)* JSON Schema validator

### Delta vs AS-IS

Everything is new. Per-source mapping:

| TO-BE field | AS-IS source | Transformation |
|---|---|---|
| `_id` | `users.services.resume.loginTokens[].hashedToken` | **Verbatim copy** (already `base64(sha256(...))`) |
| `userId` | `users._id` (the containing user) | Copy |
| `account` | `users.username` | Copy (denormalized for fast validate response) |
| `siteId` | n/a in legacy; from migration job config | Constant = the nextgen site this import lands at |
| `scheme` | n/a | Constant `"legacy"` for imported rows |
| `issuedAt` | `users.services.resume.loginTokens[].when` → ms epoch | Type conversion |
| `lastUsedAt` | n/a (legacy didn't track) | Set to import time |
| `expiresAt` | n/a | Set to `import_time + 180d` (sliding window starts now) |
| `schemaVersion` | n/a | Constant `"v1"` |

**Skipped entries (allow-list):** any `loginTokens[]` entry where `type` is set to anything — regular login tokens have `type` absent/empty. PATs (`type:"personalAccessToken"`) are a human-user feature; any future non-empty `type` (e.g. impersonation) is also skipped until explicitly added to the allow-list. auth.md Part II §6.2 import filter.

### Migration mechanic

```python
# Pseudocode — idempotent, resumable
import_time = now_ms()
idle_window = 180 * 24 * 3600 * 1000      # 180d in ms

for user in legacy_users.find(
    { "services.resume.loginTokens.0": { "$exists": True } },
    sort=[("_id", 1)],
    cursor_batch_size=500
):
    for token in user["services"]["resume"]["loginTokens"]:
        # Allow-list: import ONLY tokens whose type is absent/empty
        # (regular login tokens). Any non-empty type (PAT or future
        # variants) is skipped — auth.md Part II §6.2 import filter.
        if token.get("type"):              # any truthy type → SKIP
            continue
        if not token.get("hashedToken"):
            continue                       # malformed entry — skip safely

        nextgen_chat.sessions.update_one(
            { "_id": token["hashedToken"] },
            { "$setOnInsert": {
                "_id":           token["hashedToken"],
                "userId":        user["_id"],
                "account":       user["username"],
                "siteId":        CONFIG.siteId,           # this site's ID
                "scheme":        "legacy",
                "issuedAt":      to_epoch_ms(token["when"]),
                "lastUsedAt":    import_time,
                "expiresAt":     iso_date(import_time + idle_window),
                "schemaVersion": "v1",
              }
            },
            upsert=True
        )
    checkpoint(user["_id"])
```

**Properties:**
- **Idempotent:** keyed on `_id` (token hash) — re-running the job won't dupe.
- **Resumable:** outer cursor on `users._id`; intra-user loop is bounded by the legacy 50-cap.
- **PAT skip:** explicit and tested; goldenfixture validates the skip predicate.

### Verification

```js
// 1. Count import vs source (PATs subtracted)
nextgenCount = db.sessions.countDocuments({ scheme: "legacy" })
legacyCount  = legacy.users.aggregate([
  { $unwind: "$services.resume.loginTokens" },
  { $match:  { "services.resume.loginTokens.type": { $in: [null, ""] } } },
  { $count:  "total" }
]).next().total
assert(nextgenCount === legacyCount, "session count mismatch")

// 2. All imported rows have valid siteId
assert(db.sessions.countDocuments({ scheme: "legacy", siteId: { $ne: CONFIG.siteId }}) === 0)

// 3. (PAT exclusion is enforced source-side by the import job — check #1 above already
//     subtracts PATs from the expected count, so no separate sessions-side assertion is
//     needed. The schema has no schemeOriginal field; importing a PAT would be invisible
//     in sessions if it ever leaked through.)

// 4. TTL index reaping confirmed
db.sessions.getIndexes().find(i => i.name.includes("expiresAt"))
// Expect: expireAfterSeconds: 0, partialFilterExpression: { expiresAt: { $exists: true } }
```

### Rollback

```js
db.sessions.drop()  // safe — token hashes are reproducible from legacy
```

---

## 5. Migration job — end-to-end ordering

Per site, in this order. Each step is reversible.

| Step | Action | Pre-check | Post-check | Rollback |
|---|---|---|---|---|
| **0** | Deploy `botplatform-service` (dark, no traffic) | nextgen `chat` DB reachable | `/healthz` returns 200 | Scale to 0 |
| **1** | Create `credentials` + `sessions` collections + indexes | `users` index `{account, siteId}` exists | `db.credentials.getIndexes()` shows `account_unique`; `db.sessions.getIndexes()` shows `userId_lookup` + partial TTL | Drop collections |
| **2** | Run **credentials bulk import** (§3) | Step 1 complete; pick approach below for write-side handling during steps 2–3 | `db.credentials.count()` matches reconciliation query | Drop `credentials`; rerun |
| **3** | Run **sessions bulk import** (§4) | Step 2 complete | `db.sessions.count({scheme:"legacy"})` matches reconciliation | Drop `sessions`; rerun |
| **3.5** | **Delta catch-up** — apply any legacy `users` writes that happened during steps 2+3 (see "Lossless cutover" below) | Steps 2+3 complete | Diff query returns 0 outstanding writes since each user's import timestamp | Re-run steps 2 → 3 → 3.5 (the bulk import is idempotent) |
| **4** | **Activate live users-sync** (legacy `users` → nextgen `credentials`) | Step 3.5 complete (no in-flight drift) | Pick a known bot; rotate password in legacy; observe `db.credentials.findOne({account:...}).updatedAt` bumps within sync interval | Disable the sync; ops bears the data-drift risk |
| **5** | Start **chat-GW canary** (1% → 10% → 50% → 100%) | Step 4 has soaked ≥ 24 h | Per-canary-step SLOs hold (auth.md Part II §10.2) | Re-weight VirtualService back to legacy |
| **6** | **Sunset legacy** — disable legacy-fallback path in `/v1/auth/validate`; remove fz2/chat backend | 100% traffic on nextgen for ≥ 1 week with zero legacy-fallback validations observed in metrics | No 401s spike post-flip | Re-enable legacy fallback; restore fz2 backend |

**Critical: steps are PER SITE.** A multi-site rollout repeats steps 0–6 per site, with the chat-GW canary (step 5) coordinated across sites via separate VirtualServices.

### Lossless cutover — handling writes during steps 2+3

Legacy `users` is still writable during the credentials + sessions bulk import. Password changes, fresh logins, and new tokens in that window would be missed by the bulk import (the cursor only sees data committed before its start) AND missed by the live users-sync (not activated until step 4). Pick one of these strategies — both are documented; ops chooses per cutover plan:

**Strategy A — Source freeze (simplest, requires downtime tolerance):**
- Before step 2: freeze legacy writes to `users.services.password.*` and `users.services.resume.loginTokens[]` (toggle the legacy app into a read-only mode for the password/session paths only; user-facing reads continue).
- During steps 2 + 3: bulk import sees a consistent snapshot.
- Step 3.5 is a no-op (nothing to catch up).
- Step 4 activates live sync, then legacy is un-frozen.
- **Cost:** brief window where users can't change passwords or get new sessions — typically minutes.
- **Use when:** ops can schedule the cutover during low-activity hours.

**Strategy B — Concurrent sync + delta reconciliation (no downtime, more complex):**
- Activate users-sync BEFORE step 2 (write to `credentials` upserts; importantly, the sync path uses `$setOnInsert` for missing rows so it cannot drop a write that arrives before bulk import — Strategy B requires this upsert mode to be live, not the plain `$set` in §3's conflict policy).
- Run bulk import (steps 2+3) concurrently — `$setOnInsert` semantics make both safe: import writes only on missing rows; sync updates by-account regardless.
- Step 3.5 verifies via a diff query: for each user whose legacy `updatedAt > import_start_time`, confirm `nextgen.credentials.updatedAt >= legacy.updatedAt`. If any user lags, run a final per-user sync pass before step 4.
- **Cost:** sync path must be upsert-capable from the start; more state to track during cutover.
- **Use when:** zero-downtime is required.

**Per-site:** pick one strategy per site; can vary across sites in a multi-site rollout if convenient.

---

## 6. Reconciliation queries (cheat sheet)

Bookmark these for the canary-phase health-checks.

```js
// Are all migratable legacy users represented in credentials?
nextgenChat.credentials.countDocuments({}) ===
  legacy.users.countDocuments({ "services.password.bcrypt": { "$exists": true } })

// Are all non-PAT legacy tokens in sessions?
nextgenChat.sessions.countDocuments({ scheme: "legacy" }) ===
  legacy.users.aggregate([
    { $unwind: "$services.resume.loginTokens" },
    { $match:  { "services.resume.loginTokens.type": { $in: [null, ""] } } },
    { $count:  "total" }
  ]).next().total

// Are all credentials in this DB pinned to the expected site?
assert(
  nextgenChat.credentials.countDocuments({ siteId: { $ne: CONFIG.siteId } }) === 0,
  "credential site mismatch — some rows landed in the wrong site's DB"
)
assert(
  nextgenChat.sessions.countDocuments({ siteId: { $ne: CONFIG.siteId } }) === 0,
  "session site mismatch"
)

// Validate-path performance — random spot check
db.sessions.find({ _id: "<sample hash>" }).explain("executionStats")
// Expect: nReturned: 1, totalDocsExamined: 1, IXSCAN on _id, executionTimeMillis: <5
```

---

## 7. Open questions / pending verification

- [ ] **`users.{account, siteId}` compound index** — confirm PR #295 added it, or add in step 1 of this runbook.
- [ ] **JSON Schema validators** on `credentials` and `sessions` — decision: include or defer?
- [ ] **Per-site DB topology** — confirm whether each nextgen site has its own `chat` DB or a shared cluster with site-prefixed collections. Affects how the migration job is parameterized.
- [ ] **Live users-sync mechanic** — owned by external infra team; confirm it carries `services.password.bcrypt` to `credentials.passwordHash` and not just identity fields.
- [ ] **Legacy DB read credentials** — ops provisions; this runbook assumes a read-only Mongo user with access to `legacy.users`.

---

## 8. References

- Auth spec **Part II** §4 (data model), §6 (migration overview), §10 (cutover), §16 (config)
- Auth spec **Part I** §5 (critical constraints), §6 (migration)
- Auth spec **Part III** §3 (data-flow), §4.3 (token compatibility)
- PR #295 — portal-service + provisioning gate (the `users.siteId` field origin)
