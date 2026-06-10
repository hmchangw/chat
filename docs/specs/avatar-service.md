# Spec: avatar-service — User / Bot / Room avatar resolver + image server

> **Status:** DESIGN — not yet implemented. This document is the agreed design
> record from the brainstorming session on branch `claude/avatar-service`.
> Forward-looking language ("the service will …", "the handler …") describes
> planned work, not shipped behaviour.

*A new Gin HTTP service that, given an account or a room id, either **307-redirects**
to where the avatar actually lives (external employee-photo service for users, or
the owning cluster) or **proxy-streams the image bytes** from MinIO — falling
back to a generated/default image so a request never dead-ends.*

---

## 1. Goal

Replace the soon-to-be-retired legacy (rocketchat) avatar endpoint with a
first-party service that serves avatars for **users**, **bots**, and **rooms**
across a multi-cluster (multi-domain) deployment, where:

- **User** data is synced to every cluster → resolvable locally.
- **Bot** and **room** data are cluster-bound (not synced) → may require a
  single cross-cluster hop to the owning cluster.

Public read endpoints + authenticated write endpoints:

| Endpoint | Purpose | Auth |
|----------|---------|------|
| `GET /avatar/v1/:accountName` | User **and** bot avatar (frontend routes dm/botDM room avatars here too) | public |
| `GET /avatar/v1/room/:roomID` | Room avatar — **channel / discussion only** | public |
| `PUT /avatar/v1/bot/:botName` | Upload a custom bot avatar | **authn + authz** |

**v1 write scope = bot uploads only.** Room and user avatars are never uploaded
through this service: users resolve to the external employee-photo service, and
room custom avatars are **read-only** — they arrive via a legacy-data migration
that writes directly into the `avatars` collection + MinIO (§4.4), not through
any endpoint. Room `PUT` and all `DELETE`/reset are out of scope for v1.

For any kind, a custom image (when present) takes priority; the dynamic SVG (§8)
is the universal fallback.

Non-goals: per-size rendering (`_120` is fixed for the employee-photo redirect);
room/user uploads; deleting/resetting a custom avatar. **Read** endpoints are
public; the bot-upload endpoint requires OIDC auth + **platform-admin** role
(§7a.4).

## 2. Service shape

A new flat service `avatar-service/` at repo root, following the per-service
layout. **It does not use NATS** — auth is OIDC-token validation (§7a.4), not a
NATS callout. Mongo + MinIO backed.

| File | Responsibility |
|------|----------------|
| `main.go` | Config (`caarlos0/env`), wire Mongo + MinIO + `pkg/oidc` validator, Gin server + timeouts, graceful shutdown (`pkg/shutdown.Wait`) |
| `routes.go` | Register GET ×2 (public), `PUT /bot/:botName` (behind auth middleware), `GET /healthz` |
| `handler.go` | Read path: resolve owning site → cross-cluster redirect → avatars-doc lookup → stream/default |
| `upload.go` | Bot-upload write path: OIDC authn + bot authz middleware, validate (type/size/decode), store to MinIO, upsert `avatars` doc |
| `avatar.go` | `renderDefaultSVG(seed, initial)` pure deterministic generator + object-key helpers |
| `store.go` | `avatarStore` interface — `EmployeeID` (user), `IsPlatformAdmin` (caller role, upload authz), `RoomSite` (siteID+type via subscriptions), `Avatar` (avatars-doc lookup), `SetBotAvatar` (upsert) + `//go:generate mockgen` |
| `store_mongo.go` | Mongo implementation (`users`, `subscriptions`, `avatars`) |
| `handler_test.go` | Unit tests with mocked store + fake MinIO/stream seam |
| `integration_test.go` | testcontainers (Mongo + MinIO via `pkg/testutil`), `//go:build integration` |
| `mock_store_test.go` | Generated mock (never hand-edited) |
| `deploy/` | `Dockerfile`, `docker-compose.yml`, `azure-pipelines.yml` |

Mandatory cross-cutting: `GET /healthz`; the auth-service middleware trio —
`requestIDMiddleware` (via `idgen.ResolveRequestID` + `natsutil.WithRequestID`),
`accessLogMiddleware` (`slog` JSON: method/path/status/latency/request_id), CORS;
`errcode`/`errhttp` for client-facing errors; server timeouts; ≥80% coverage via
TDD.

**Observability scope = auth-service parity** (slog + request-id + access-log).
OTel tracing + Prometheus `/metrics` are **deferred to post-v1** (§9) — but v1
preserves the seams so adding them later is additive: `context.Context` is
threaded through every store/MinIO call, and the read handler returns a **typed
outcome** (`kind` ∈ user/bot/room × `outcome` ∈ `redirect`/`stream`/`default`/
`304`) that `accessLogMiddleware` records. That outcome gives v1 traffic-split
visibility in logs and becomes the future `resolution_total{kind,outcome}` metric
label — instrumented at one seam, not scattered across the decision tree.

## 3. Configuration (env, `caarlos0/env`)

| Var | Meaning | Default / required |
|-----|---------|--------------------|
| `PORT` | HTTP port | `8080` |
| `LOG_LEVEL` | slog level | `info` |
| `SITE_ID` | this cluster's site id | required |
| `CLUSTER_DOMAINS` | `siteID=baseURL` map for cross-cluster redirects | required |
| `EMPLOYEE_PHOTO_BASE_URL` | external employee-photo host (the `xxx_domain`) | required |
| `MONGO_URI` / `MONGO_DB` | operational DB | required / `chat` |
| `OIDC_ISSUER_URL` / `OIDC_AUDIENCES` | validate upload Bearer tokens (`pkg/oidc`); required unless `DEV_MODE` | required when `DEV_MODE=false` |
| `DEV_MODE` | bypass OIDC validation for local dev (mirrors auth-service) | `false` |
| `MINIO_ENDPOINT` / `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | object storage (custom uploads) | required |
| `AVATAR_BUCKET` | MinIO bucket for avatars | `avatars` |
| `MAX_UPLOAD_BYTES` | reject uploads larger than this | `1048576` (1 MiB) |
| `CACHE_MAX_AGE_SECONDS` | `Cache-Control: public, max-age=` value | `21600` (6h) |

`CLUSTER_DOMAINS` maps a `siteID` to the base URL of *that cluster's*
avatar-service (e.g. `xxx-2=https://avatar-service-xxx-2`). Cross-cluster
redirects and `EMPLOYEE_PHOTO_BASE_URL` are **config**, never hardcoded.

MinIO is **required** in v1 — it holds custom bot uploads and migrated room
images (§4.4).

## 4. Common mechanisms

### 4.1 Proxy streaming (the MinIO-hit "serve image" step)

When a **stored** image (a custom bot upload, or a migrated room image) lives in
*this* cluster's MinIO, the handler relays the bytes rather than redirecting:

1. `obj, err := mc.GetObject(ctx, bucket, key, opts)` — `*minio.Object` is an `io.Reader`.
2. `st, err := obj.Stat()` → `Size`, `ContentType`, `ETag`.
   - **NotFound → fall through to the dynamic default (§8)** — *not* a stored asset.
   - other error → `fmt.Errorf("stat object: %w", err)` → collapses to `internal`.
3. Set `Cache-Control: public, max-age=<cfg>` and `ETag: st.ETag`.
4. If request `If-None-Match` == `st.ETag` → `304 Not Modified`, no body.
5. `c.DataFromReader(http.StatusOK, st.Size, st.ContentType, obj, nil)` — streams.

Rationale: the cacheable URL stays stable (avatar-service's own URL), so
`Cache-Control` actually works and `ETag` enables conditional GET. Redirecting
to a MinIO presigned URL would defeat caching (expiring `Location`) and add a hop.

This path only applies when MinIO actually holds a stored image. A miss does
**not** stream a stored default — the default is generated on the fly (§8) and
never written back.

### 4.2 Cross-cluster loop breaker (`?fwd=1`)

A request resolves to at most **one** cross-cluster hop. When forwarding,
append `?fwd=1`. A handler that sees `fwd=1` MUST resolve locally or fall back
to the dynamic default image — it MUST NOT redirect cross-cluster again. The
dynamic default (§8) is the universal backstop that guarantees termination.

### 4.3 Caching

- Baseline: `ETag` (from the MinIO object, or the deterministic hash for a
  generated default — §8) + `Cache-Control: public, max-age`.
- Cache-busting via `?v=`: **deferred, and not stored.** There is no `version`
  field on the `avatars` doc (§4.4), and the frontend's room/bot metadata
  (sourced from room-service / apps) carries no version it could append anyway.
  v1 relies on `ETag` revalidation; `?v`-based busting is revisited once version
  propagation is designed (§9). A request that does carry a `?v` MAY still be
  served with a long `max-age` + `immutable`.

### 4.4 The `avatars` collection (custom-image existence source)

A dedicated Mongo collection **owned by avatar-service**. **Presence of a
document = "this subject has a custom image in MinIO";** absence = serve the
dynamic default (§8). It is the authoritative existence check for both kinds, so
the common "no custom image" case is a cheap `_id` point-lookup that never
touches MinIO.

- **Writers:** avatar-service writes a doc on a bot upload (§7a); the legacy-data
  **migration** writes docs for pre-existing room (and bot) images. avatar-service
  never writes into room-service's `rooms` or the upstream `apps` collection — it
  owns only `avatars`, respecting service data boundaries.
- **Readers:** the GET path looks up the doc by `_id` to decide
  stream-from-MinIO vs dynamic default.
- **Cluster-local invariant:** every document belongs to **this** site — a
  subject owned by another cluster has its avatar data only in that cluster's
  `avatars` + MinIO. Cross-cluster routing is decided upstream (§6/§7) before the
  lookup, so the doc needs no `siteId`.

**Schema** (`pkg/model/avatar.go`; added to the `model_test` round-trip):

```go
type AvatarSubjectType string

const (
    AvatarSubjectRoom AvatarSubjectType = "room"
    AvatarSubjectBot  AvatarSubjectType = "bot"
)

// Avatar is a custom (uploaded or migrated) avatar for a room or bot. Presence
// of a document means the subject has a custom image in MinIO; absence means the
// service serves a generated default (§8). The collection is cluster-local, so
// no siteId is stored.
type Avatar struct {
    ID          string            `json:"id"          bson:"_id"`         // "<subjectType>:<subjectId>"
    SubjectType AvatarSubjectType `json:"subjectType" bson:"subjectType"` // "room" | "bot"
    // SubjectID is the id this service looks the subject up by:
    //   room → roomID;  bot → bot account (".bot").
    SubjectID   string    `json:"subjectId"   bson:"subjectId"`
    MinioKey    string    `json:"minioKey"    bson:"minioKey"`    // MinIO object key, used verbatim
    ContentType string    `json:"contentType" bson:"contentType"` // detected type (image/png|image/jpeg)
    Size        int64     `json:"size"        bson:"size"`        // object size, bytes (Content-Length)
    ETag        string    `json:"etag"        bson:"etag"`        // MinIO ETag — 304 without a MinIO hit
    CreatedAt   time.Time `json:"createdAt"   bson:"createdAt"`
    UpdatedAt   time.Time `json:"updatedAt"   bson:"updatedAt"`
}
```

`_id` is the deterministic composite `"<subjectType>:<subjectId>"` (e.g.
`room:r123`, `bot:helper.bot`) — the natural key, so "one custom image per
subject" is structural (no surrogate id, no extra unique index) and an upload
**upserts by `_id`**. No `version` field (§4.3); if `?v` is ever wired, a missing
int reads as 0.

**Migration from the legacy `avatars` collection** (one-time; writes this
cluster's docs — MinIO objects are **not** moved):

| legacy field | → `Avatar` |
|--------------|------------|
| `rid` | `subjectType=room`, `subjectId=rid` |
| `userId` (a bot) | `subjectType=bot`, `subjectId=`account (resolve `userId`→account) |
| `path` | `minioKey` (used verbatim) |
| `type` | `contentType` |
| `size` | `size` |
| `etag` | `etag` |
| `uploadedAt` | `createdAt` |
| `updatedAt` | `updatedAt` |

Migration rules:
1. **Only migrate `complete == true` records** — skip in-flight/abandoned legacy
   uploads (`uploading` / incomplete), whose MinIO object may be partial or absent.
2. **Bot records:** resolve legacy `userId` → bot `account` (join to `users`) to
   build `subjectId` / `_id`; the read path keys bots by account, not user id.
3. **Human-user avatars are not migrated** — real users resolve to the external
   employee-photo service and have no `avatars` doc.
4. Legacy `progress` / `complete` / `uploading` are **not** carried over — the
   single-PUT upload model (§7a.2) tracks no upload state.

## 5. Account format & parsing (Endpoint 1)

| Kind | Forms | Routing |
|------|-------|---------|
| user | `<name>` · `<name>@site.example.com` | synced everywhere → **always local**; domain is informational |
| bot | `<name>.bot` · `<name>.bot@site.example.com` | cluster-bound → domain identifies the **owning cluster** |

Parse: split `account` on `@` into `localPart` + optional `domain`. **Type is
decided first by whether `localPart` ends in `.bot`.** For bots, `domain`
(when present, ending `.example.com`) maps to the owning `siteID`.

Owning-site resolution is isolated behind a single resolver seam so the read
handler is agnostic to its source. **v1: a bot's owning `siteID` = the account
`domain`** (no DB call); this can later be swapped for a `users`-record lookup
(`SiteID` on the bot's user doc) without touching the redirect/stream logic.

## 6. Endpoint 1 — `GET /avatar/v1/:accountName`

```text
localPart, domain := splitAt(accountName)
if hasSuffix(localPart, ".bot"):            # ── bot (cluster-bound)
    owning := resolveBotSite(localPart, domain)   # v1: from domain; "" => local
    if owning != "" && owning != cfg.SiteID && !fwd:
        307 → https://{clusterDomain(owning)}/avatar/v1/{accountName}?fwd=1
    if av, found := store.Avatar(ctx, "bot", localPart); found:
        proxyStream(mc.GetObject(ctx, bucket, av.MinioKey))   # custom upload (§4.1)
    else:
        serveDefault(localPart)                               # dynamic SVG (§8)
else:                                        # ── user (synced; domain informational)
    eid, found := cache[localPart]
    if !found: eid, found = store.EmployeeID(ctx, localPart)   # MISS MUST hit DB
    if found:
        cache.put(localPart, eid)
        307 → {EMPLOYEE_PHOTO_BASE_URL}/xxxPhoto/po/{eid}_120.JPG
    else:
        serveDefault()
```

- **Bot read path:** resolve owning site → (cross-cluster redirect if remote) →
  `avatars` doc present ? proxy-stream the MinIO object : dynamic default. There
  is **no** redirect to an app-provided URL — every avatar is served through this
  GET endpoint, so `App.AvatarURL` is not used.
- `serveDefault()` **dynamically generates** a deterministic SVG from the
  account (§8) and returns it directly — it does not read from MinIO and does
  not store anything.
- `Cache-Control: public, max-age=<cfg>` on every response (incl. redirects).
- **Correctness rule:** a mapping-cache *miss* falls back to the DB; it must
  **not** skip to the default branch (that would give a real user the wrong
  avatar). The cache is an accelerator only — bounded LRU + TTL.
- `accountName`, `eid` are validated/escaped (`url.PathEscape`, allowlist
  regex) before being placed in a redirect `Location`.

## 7. Endpoint 2 — `GET /avatar/v1/room/:roomID`

```text
room, found := store.RoomSite(ctx, roomID)   # SiteID + RoomType, via subscriptions
if !found:                          serveDefault(roomID)   # unknown here → can't forward
if room.RoomType in {dm, botDM}:    serveDefault(roomID)   # frontend should use Endpoint 1
# channel / discussion:
if room.SiteID != cfg.SiteID && !fwd:
    307 → https://{clusterDomain(room.SiteID)}/avatar/v1/room/{roomID}?fwd=1
if av, found := store.Avatar(ctx, "room", roomID); found:
    proxyStream(mc.GetObject(ctx, bucket, av.MinioKey))   # migrated custom image (§4.1)
else:
    serveDefault(roomID)                                   # dynamic SVG (§8)
```

- Owning site + room type come from the `subscriptions` collection
  (`Subscription.SiteID` / `.RoomType`), so avatar-service does not read
  room-service's `rooms` collection. No subscription record here → it cannot know
  the owning site → serve the default (the frontend normally resolves the owning
  domain first via the room-location service and hits the right cluster directly).
- Room custom images are **read-only and migrated** — there is no room upload
  (§1). The `avatars` doc (written by the migration, §4.4) is the existence
  check; present → stream from MinIO, absent → dynamic default with no MinIO hit.
- The generated default is **never written back** to MinIO.
- dm/botDM are **user-type** avatars; the frontend fetches them via Endpoint 1
  using the counterpart user / bot account. If such a roomID nonetheless lands
  here, return the dynamic default (safe, not a 4xx).

## 7a. Upload API (custom bot/room avatars)

`PUT /avatar/v1/bot/:botName` accepts a custom bot image (request body = raw
image bytes; `Content-Type` declares the format). **Bots are the only uploadable
kind in v1** — users and rooms never upload (§1). On success the custom image
takes priority over the dynamic default on the bot's GET path.

### 7a.1 Validation & security (mandatory)

- **Raster only — reject `image/svg+xml` uploads.** A user-supplied SVG served
  from our origin is **stored XSS** (SVG can carry `<script>`/`foreignObject`).
  v1 allowlist: **`image/png`, `image/jpeg`** (WebP deferred — §9). The *default*
  avatar is SVG because **we** generate it (trusted); uploads never are.
- **Size cap**: enforce `MAX_UPLOAD_BYTES` via `http.MaxBytesReader` before
  reading the body.
- **Verify the bytes are really an image**: decode with the stdlib `image`
  package (`image/png`, `image/jpeg`) and reject on decode failure. Optionally
  re-encode to a normalized format to strip EXIF/metadata and defeat polyglots.
- All served responses set `X-Content-Type-Options: nosniff` and
  `Content-Security-Policy: default-src 'none'`, so a browser neither MIME-sniffs
  the bytes into something executable nor runs any script if a generated SVG is
  opened as a top-level document. Both are unconditional and cheap (harmless on
  PNG/JPEG, meaningful for the generated SVG, §8.1).

### 7a.2 Storage

- **Write order: MinIO object first, then the `avatars` doc.** The doc is upserted
  only after the object is durably stored, so "doc exists ⟺ a complete image
  exists" — no upload-state flags are needed (contrast the legacy
  `progress`/`complete`/`uploading` fields, dropped in §4.4).
- The object's key is chosen by avatar-service and stored **verbatim** in
  `minioKey`, used as-is on reads — never re-derived from a convention (migrated
  room objects keep their legacy paths, §4.4). The **detected** content-type
  (from decode, not the client header) is set as object metadata and stored.
- The upserted doc (`_id = bot:{botName}`, §4.4) records `minioKey`,
  `contentType`, `size`, `etag` and bumps `updatedAt`; its presence is the GET
  existence check (§6). A re-upload **overwrites** in place.
- No `DELETE`/reset in v1 (§1): a custom bot avatar can be overwritten by a new
  upload but not cleared back to the default.

### 7a.3 Cluster locality

Bots are cluster-bound, so an upload must land on the bot's **owning cluster**
(which owns the MinIO bucket + `avatars` doc). The frontend resolves the owning
site first and `PUT`s there directly. An upload that reaches the wrong cluster is
**rejected** with an errcode pointing at the correct domain — we do **not**
proxy/`307` the body cross-cluster (a 307 on PUT would resend the body).

### 7a.4 Authorization

The bot-upload endpoint is gated by an auth middleware (`upload.go`):

1. **Authn — validate the Bearer OIDC token** with `pkg/oidc.NewValidator`
   (same issuer/audience config as auth-service; `OIDC_ISSUER_URL` /
   `OIDC_AUDIENCES`). Extract `account` from claims (`PreferredUsername`, else
   `Name` — mirroring auth-service). Missing/invalid token → `401`
   (`errcode.Unauthenticated`). `DEV_MODE` bypasses validation for local dev.
2. **Authz — platform-admin only.** Bots are platform-level, upstream-provisioned
   entities with no per-bot owner field, so there is no per-bot ownership to key
   on. v1 therefore gates the upload on the **caller's** platform-admin role:
   look up the caller's `users` record by `account` and require
   `model.IsPlatformAdmin` (`Roles` contains `UserRoleAdmin`,
   `pkg/model/user.go`); non-admin → `403` (`errcode.Forbidden`). No new data
   model. (Per-bot ownership can be layered on later if a bot-owner source
   appears.)

Read endpoints (GET) remain public — no token required.

## 8. Default image — dynamic, deterministic, not persisted

The universal fallback for **every** kind (user, bot, room) — whenever the
external employee photo / MinIO custom image is absent — is an
**SVG "initials" avatar generated on the fly and returned directly to the
client**. It is **never written back to MinIO or Mongo**.

### 8.1 The generator is a pure, deterministic function

```go
// renderDefaultSVG returns the same bytes for the same (seed, initial) every
// time, on every replica. No time, no randomness, no map-iteration order.
func renderDefaultSVG(seed, initial string) []byte
```

- **Background colour** = `palette[ stableHash(seed) % len(palette) ]` using a
  fixed hash (e.g. FNV-1a). Same `seed` → same colour, forever, everywhere.
- **Initial** = the first display glyph; CJK names render via the client's
  system fonts (SVG `<text>`), so **no embedded font and zero new dependencies**.
- **Injection-safe (mandatory).** Only `initial` reaches the output; `seed` is
  hash-only and never rendered. `initial` is reduced to the **first rune**, then
  allowlisted to a letter/digit (Unicode `L*`/`Nd`, incl. CJK; uppercased when
  cased); anything else (punctuation, symbol, control, combining/bidi/zero-width,
  or empty) falls back to a single placeholder **`?`**. The chosen glyph is then
  run through `html.EscapeString` before embedding (defense-in-depth — the
  allowlist already excludes `<>&"'`). The output is served as `image/svg+xml`,
  so an unescaped `<`/`>` would be the **same stored-XSS** we reject uploads for
  (§7a.1); responses also carry `nosniff` + CSP `default-src 'none'` (§7a.1).
- `Content-Type: image/svg+xml`.

**Seed / initial sources:**

| Kind | `seed` (colour) | `initial` |
|------|-----------------|-----------|
| room | `roomID` | first glyph of `room.Name` |
| user | `account` | first glyph of display name if known, else `account` |
| bot | bot `account` | first glyph of bot name, else `account` |

### 8.2 Caching a generated default

Because the output is deterministic, the default is still cacheable:

- `ETag` = `"<templateVersion>-<hex(stableHash(seed + sanitizedInitial))>"` —
  over the **sanitized** glyph (§8.1), so names that render to the same
  colour+glyph share an ETag; identical across replicas/requests, so
  `If-None-Match` → `304` works.
- `templateVersion` is a build-time constant; bump it when the SVG template
  changes so existing caches re-fetch.
- `Cache-Control: public, max-age=<cfg>` as usual.

### 8.3 What this removes vs. a stored default

No write-back path, no `singleflight`, no embedded static asset, no
generation/storage consistency concerns. The fallback is stateless and
self-healing.

### 8.4 MinIO's role (custom uploads only)

MinIO holds **only** custom/uploaded bot/room images (§7a). Generated defaults
are never stored, so there is no lazy "generate-and-store" and no pre-warm hook
— a deterministic default needs no warming. A shared `renderDefaultSVG` keeps
the rendering in one place.

## 9. Resolved decisions & open items

**Resolved:**
- **Write scope** → **bot uploads only** in v1; no room/user upload, no
  `DELETE`/reset (§1).
- **`avatars` collection** → avatar-service-owned; **doc presence = has custom
  image**; authoritative existence source for room + bot; migration writes room
  docs, avatar-service writes bot docs (§4.4).
- **Unified read model** → resolve owning `siteID` → cross-cluster redirect →
  local `avatars`-doc lookup → MinIO stream or dynamic default (§6, §7).
- **Bot owning site** → from the account `domain` in v1, behind a resolver seam
  (swappable to a `users`-record lookup) (§5).
- **Room owning site + type** → from the `subscriptions` collection, not `rooms`
  (§7).
- **`App.AvatarURL` removed** → every avatar is served through this GET endpoint;
  no redirect to an app-provided URL (§6).
- **Upload authn** → OIDC Bearer-token validation via `pkg/oidc` (§7a.4).
- **Upload authz** → **platform-admin role** (caller's `users` record,
  `model.IsPlatformAdmin`); no per-bot owner model in v1 (§7a.4).
- **Upload formats** → PNG/JPEG only; SVG uploads rejected (§7a.1).
- **`avatars` field schema** → finalized: `_id = subjectType:subjectId`,
  `subjectType`/`subjectId`/`minioKey`/`contentType`/`size`/`etag`/`createdAt`/
  `updatedAt`; no `siteId`/`version`; migration maps the legacy collection (§4.4).

- **Default-SVG injection (S1)** → first rune + letter/digit allowlist (else
  placeholder `?`) + `html.EscapeString`, plus `nosniff` + CSP `default-src
  'none'` on responses (§8.1, §7a.1).

**Deferred / to address before implementation:**
- **`?v` cache-bust propagation (C3):** the version lives on the `avatars` doc the
  frontend can't see; v1 is ETag-only (§4.3).
- **WebP support:** deferred — needs `golang.org/x/image` (new dep, ask first).
  Also TBD: re-encode-to-normalize vs store-as-is.
- **OTel tracing + Prometheus `/metrics`:** deferred to post-v1. v1 ships
  auth-service-parity logging (slog + request-id + access-log, §2). The infra is
  ready to copy (`pkg/otelutil`; search-service's promauto + separate `/metrics`
  listener) and purely additive; v1 preserves the seams (ctx everywhere + a typed
  read-outcome in the access log → future `resolution_total{kind,outcome}`).
- **Read-path privacy:** the employee-photo redirect `Location` exposes
  `employeeID` (org-info leakage / account enumeration). Accept, or gate later.
- **Employee-photo 404:** the external host may 404 for a user with no photo →
  broken image (inherent to the redirect). A default is only served for *our*
  lookups, not for the external host's 404.

## 10. Testing plan (TDD)

- **Handler unit tests** (`handler_test.go`, mocked `avatarStore` + a stream
  seam): table-driven over Endpoint 1 (user local hit/miss, cache hit/miss→DB,
  bot local vs cross-cluster, `fwd=1` no-re-redirect, bot avatars-doc hit→stream,
  miss→dynamic default) and Endpoint 2 (room resolved via subscription: channel
  avatars-doc hit→stream, miss→dynamic default, dm/botDM→default, remote→307,
  not-found→default, `If-None-Match`→304). Assert status code, `Location`,
  `Cache-Control`, `ETag`, and body bytes/Content-Type.
- **Bot-upload unit tests** (`upload.go`): accept PNG/JPEG within size; reject
  oversize (`MAX_UPLOAD_BYTES`), reject `image/svg+xml` and non-image bytes,
  reject decode failures; on success store to MinIO + upsert the `avatars` doc;
  wrong-cluster → rejected with guiding error; missing/invalid token → 401;
  authenticated non-admin → 403; platform-admin → accepted; assert `nosniff`.
- **Generation unit tests:** `renderDefaultSVG` is **deterministic** — same
  `(seed, initial)` yields byte-identical SVG *and* the same `ETag` across
  repeated calls; stable colour per seed, correct initial (incl. CJK), valid +
  injection-safe XML (escapes hostile names).
- **Integration** (`integration_test.go`, `//go:build integration`): Mongo +
  MinIO from `pkg/testutil`; real GetObject/Stat/stream round-trip for a stored
  custom image, 304 path, and avatars-doc-miss → dynamic default (nothing written
  back).
- Coverage ≥80% (target 90% on handler + generation).

## 11. Docs to update on implementation

- `docs/client-api.md` is NATS/auth-HTTP-scoped; avatar-service is a new public
  HTTP surface — add a section there (or link this spec) describing the two
  endpoints, redirect semantics, cache headers, and the default-image behaviour.
- Delete any `docs/reviews/*` working notes before opening a PR.
