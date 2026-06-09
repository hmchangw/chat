# Spec: avatar-service — User / Bot / Room avatar resolver + image server

> **Status:** DESIGN — not yet implemented. This document is the agreed design
> record from the brainstorming session on branch `claude/avatar-service`.
> Forward-looking language ("the service will …", "the handler …") describes
> planned work, not shipped behaviour.

*A new Gin HTTP service that, given an account or a room id, either **307-redirects**
to where the avatar actually lives (external employee-photo service, a bot's
`LocationURL`, or the owning cluster) or **proxy-streams the image bytes** from
MinIO — falling back to a generated/default image so a request never dead-ends.*

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
| `PUT /avatar/v1/room/:roomID` | Upload a custom room avatar | **authn + ownership** |
| `PUT /avatar/v1/bot/:botName` | Upload a custom bot avatar | **authn + ownership** |
| `DELETE /avatar/v1/room/:roomID` · `…/bot/:botName` | Reset to dynamic default (optional) | **authn + ownership** |

Custom uploads are allowed for **bots and rooms only** — users never upload
(their photo is the external employee-photo service). Custom image takes
priority; the dynamic SVG (§8) is the fallback.

Non-goals: per-size rendering (`_120` is fixed for the employee-photo
redirect). **Read** endpoints are public; **write** endpoints require auth — the
mechanism is the central open decision (§9).

## 2. Service shape

A new flat service `avatar-service/` at repo root, following the per-service
layout. **It does not use NATS** for its read path (a NATS dependency may enter
only via the §9 upload-authz decision). Mongo + MinIO backed.

| File | Responsibility |
|------|----------------|
| `main.go` | Config (`caarlos0/env`), wire Mongo + MinIO clients, Gin server + timeouts, graceful shutdown (`pkg/shutdown.Wait`) |
| `routes.go` | Register GET ×2, PUT ×2, DELETE ×2, `GET /healthz` |
| `handler.go` | Read path: resolve/redirect/stream decision tree |
| `upload.go` | Write path: validate (type/size/decode), store to MinIO, bump `AvatarVersion`; authz middleware |
| `avatar.go` | `renderDefaultSVG(seed, initial)` pure deterministic generator + object-key helpers |
| `store.go` | `avatarStore` interface — `EmployeeID`, `Bot`, `Room`, `SetRoomAvatar`/`SetBotAvatar` (bump version), ownership lookup + `//go:generate mockgen` |
| `store_mongo.go` | Mongo implementation (`users`, `rooms`, bots) |
| `handler_test.go` | Unit tests with mocked store + fake MinIO/stream seam |
| `integration_test.go` | testcontainers (Mongo + MinIO via `pkg/testutil`), `//go:build integration` |
| `mock_store_test.go` | Generated mock (never hand-edited) |
| `deploy/` | `Dockerfile`, `docker-compose.yml`, `azure-pipelines.yml` |

Mandatory cross-cutting (per CLAUDE.md): `GET /healthz`, request-ID middleware
+ `slog` JSON, `errcode`/`errhttp` for client-facing errors, server + Resty
timeouts, ≥80% coverage via TDD.

## 3. Configuration (env, `caarlos0/env`)

| Var | Meaning | Default / required |
|-----|---------|--------------------|
| `PORT` | HTTP port | `8080` |
| `LOG_LEVEL` | slog level | `info` |
| `SITE_ID` | this cluster's site id | required |
| `CLUSTER_DOMAINS` | `siteID=baseURL` map for cross-cluster redirects | required |
| `EMPLOYEE_PHOTO_BASE_URL` | external employee-photo host (the `xxx_domain`) | required |
| `MONGO_URI` / `MONGO_DB` | operational DB | required / `chat` |
| `MINIO_ENDPOINT` / `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | object storage (custom uploads) | required |
| `AVATAR_BUCKET` | MinIO bucket for avatars | `avatars` |
| `MAX_UPLOAD_BYTES` | reject uploads larger than this | `1048576` (1 MiB) |
| `CACHE_MAX_AGE_SECONDS` | `Cache-Control: public, max-age=` value | `21600` (6h) |

`CLUSTER_DOMAINS` maps a `siteID` to the base URL of *that cluster's*
avatar-service (e.g. `xxx-2=https://avatar-service-xxx-2`). Cross-cluster
redirects and `EMPLOYEE_PHOTO_BASE_URL` are **config**, never hardcoded.

MinIO is **required** in v1 — it backs custom bot/room uploads (§6a).

## 4. Common mechanisms

### 4.1 Proxy streaming (the MinIO-hit "serve image" step)

When a **stored** image (a custom/uploaded room avatar) lives in *this*
cluster's MinIO, the handler relays the bytes rather than redirecting again:

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
- Cache-busting via `?v={AvatarVersion}`: `AvatarVersion` lives on the bot/room
  doc and is bumped on every upload/delete (§7a.2). The frontend already holds
  it (room/bot metadata) and appends it, so a new upload is reflected
  immediately even within the `max-age` window. A request carrying `?v` MAY be
  served with a long `max-age` + `immutable`.

## 5. Account format & parsing (Endpoint 1)

| Kind | Forms | Routing |
|------|-------|---------|
| user | `<name>` · `<name>@site.example.com` | synced everywhere → **always local**; domain is informational |
| bot | `<name>.bot` · `<name>.bot@site.example.com` | cluster-bound → domain identifies the **owning cluster** |

Parse: split `account` on `@` into `localPart` + optional `domain`. **Type is
decided first by whether `localPart` ends in `.bot`.** For bots, `domain`
(when present, ending `.example.com`) maps to the owning `siteID`.

## 6. Endpoint 1 — `GET /avatar/v1/:accountName`

```text
localPart, domain := splitAt(accountName)
if hasSuffix(localPart, ".bot"):            # ── bot (cluster-bound)
    owning := siteFromDomain(domain)        # "" => treat as local
    if owning != "" && owning != cfg.SiteID && !fwd:
        307 → https://{clusterDomain(owning)}/avatar/v1/{accountName}?fwd=1
    bot, found := store.Bot(ctx, localPart)              # local DB
    if found && bot.AvatarVersion > 0:                   # custom upload wins
        obj := mc.GetObject(ctx, bucket, botAvatarKey(localPart)); proxyStream(obj)
    elif found && bot.LocationURL != "":  307 → bot.LocationURL
    else:                                 serveDefault(localPart)
else:                                        # ── user (synced; domain informational)
    eid, found := cache[localPart]
    if !found: eid, found = store.EmployeeID(ctx, localPart)   # MISS MUST hit DB
    if found:
        cache.put(localPart, eid)
        307 → {EMPLOYEE_PHOTO_BASE_URL}/xxxPhoto/po/{eid}_120.JPG
    else:
        serveDefault()
```

- **Bot avatar precedence:** custom upload (MinIO) → bot `LocationURL` →
  dynamic default. (Confirm this ordering — §9.)
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
room, found := store.Room(ctx, roomID)
if !found:                          serveDefault(roomID)   # incl. room owned by a cluster we can't resolve
if room.Type in {dm, botDM}:        serveDefault(roomID)   # frontend should use Endpoint 1 instead
# channel / discussion:
if room.SiteID != cfg.SiteID && !fwd:
    307 → https://{clusterDomain(room.SiteID)}/avatar/v1/room/{roomID}?fwd=1
if room.AvatarVersion == 0:         serveDefault(room)     # no custom image → dynamic SVG, no MinIO hit
obj, err := mc.GetObject(ctx, bucket, roomAvatarKey(roomID))
if found:        proxyStream(obj)          # stored custom image (§4.1)
else:            serveDefault(room)         # race/inconsistency → dynamic SVG (§8)
```

- The `AvatarVersion` flag on the room doc (bumped on upload, §6a) lets the
  common "no custom image" case skip the MinIO round-trip and answer with the
  dynamic default directly.
- The generated default is **never written back** to MinIO. MinIO is read-only
  on the GET path and only holds custom/uploaded images.
- dm/botDM are **user-type** avatars; the frontend fetches them via Endpoint 1
  using the counterpart user / bot account. If such a roomID nonetheless lands
  here, return the dynamic default (safe, not a 4xx).
- Cross-cluster for rooms is a **defensive fallback**: normally the frontend
  resolves the owning domain first (via the existing room-location service) and
  hits the correct cluster directly. If this cluster has no record of the room,
  it serves the default image (it cannot know the owning siteID to forward).

## 7a. Upload API (custom bot/room avatars)

`PUT /avatar/v1/room/:roomID` and `PUT /avatar/v1/bot/:botName` accept a custom
image (request body = raw image bytes; `Content-Type` declares the format).
Users cannot upload. On success the custom image takes priority over the
dynamic default on the GET path.

### 7a.1 Validation & security (mandatory)

- **Raster only — reject `image/svg+xml` uploads.** A user-supplied SVG served
  from our origin is **stored XSS** (SVG can carry `<script>`/`foreignObject`).
  Allowlist: `image/png`, `image/jpeg`, `image/webp`. The *default* avatar is
  SVG because **we** generate it (trusted); uploads never are.
- **Size cap**: enforce `MAX_UPLOAD_BYTES` via `http.MaxBytesReader` before
  reading the body.
- **Verify the bytes are really an image**: decode with the stdlib `image`
  package (`image/png`, `image/jpeg`; WebP decode needs `golang.org/x/image` —
  see §9) and reject on decode failure. Optionally re-encode to a normalized
  format to strip EXIF/metadata and defeat polyglot files.
- All responses (GET and the served upload) set `X-Content-Type-Options:
  nosniff` so browsers don't MIME-sniff the bytes into something executable.

### 7a.2 Storage & versioning

- Bytes stored in MinIO at the convention key (`room/{roomID}` / `bot/{botName}`),
  with the validated `Content-Type` as object metadata.
- After a successful put, **bump `AvatarVersion`** on the entity's Mongo doc.
  This drives two things: the GET existence check (§7) and cache-busting — the
  frontend appends `?v={AvatarVersion}` so a new upload is seen immediately
  (§4.3). The MinIO object's `ETag` also changes naturally.
- `DELETE` removes the object and sets `AvatarVersion = 0` → GET reverts to the
  dynamic default.

### 7a.3 Cluster locality

Bots and rooms are cluster-bound, so an upload must land on the **owning
cluster** (which owns the Mongo doc + MinIO). The frontend resolves the owning
domain first (same room-location service as GET) and `PUT`s there directly. An
upload that reaches the wrong cluster is **rejected** with an errcode pointing
at the correct domain — we do **not** proxy/`307` the body cross-cluster
(re-sending the upload body is wasteful; a 307 on PUT would resend it).

### 7a.4 Authorization

Authn + an **ownership check** (caller must own/admin the target room or bot)
gate every write. The mechanism is **undecided** — this service is the first to
authorize inbound mutating HTTP, and there is no existing middleware to reuse.
See §9 for the options to pick from.

## 8. Default image — dynamic, deterministic, not persisted

The universal fallback for **every** kind (user, bot, room) — whenever the
external photo / bot `LocationURL` / MinIO custom image is absent — is an
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
- `Content-Type: image/svg+xml`.

**Seed / initial sources:**

| Kind | `seed` (colour) | `initial` |
|------|-----------------|-----------|
| room | `roomID` | first glyph of `room.Name` |
| user | `account` | first glyph of display name if known, else `account` |
| bot | bot `account` | first glyph of bot name, else `account` |

### 8.2 Caching a generated default

Because the output is deterministic, the default is still cacheable:

- `ETag` = `"<templateVersion>-<hex(stableHash(seed+initial))>"` — identical
  across replicas and requests; `If-None-Match` → `304` works.
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

## 9. Open questions / deferred

- **🔴 Upload auth mechanism (DECISION NEEDED before Phase 0).** Write endpoints
  (§7a) need authn + ownership. This is the first HTTP service to authorize an
  inbound *mutating* request; auth-service only *issues* NATS JWTs, so there is
  no middleware to reuse. Options:
  1. **Validate the NATS user JWT** (Bearer) that `auth-service` issued —
     avatar-service holds the NATS account public key and verifies the
     signature. Caveat: the user `account` is embedded in the JWT's pub/sub
     permission subjects, not a clean claim, so extraction is awkward.
  2. **Validate the OIDC/SSO token directly** via `pkg/oidc` (same path
     auth-service uses) → clean `account` claim. Adds an OIDC dependency to this
     service.
  3. **Front the write path with NATS** instead of HTTP: a `room-service` /
     bot-owner handler does the ownership check (it already has the context) and
     either writes MinIO or calls avatar-service with a trusted internal token.
     Keeps authz where ownership data lives; image bytes over NATS are the
     downside.
  4. **API-gateway** injects a verified identity header; avatar-service trusts it.
  - Plus the **ownership check** itself: where does avatar-service learn that
    `account` owns room/bot? (Mongo `RoomMember` role lookup, or a NATS request
    to room-service.)
- **Bot avatar precedence** (§6): confirm custom upload → `LocationURL` →
  default is the intended order.
- **WebP / re-encoding** (§7a.1): stdlib decodes PNG/JPEG only. Accept WebP
  uploads (needs `golang.org/x/image` — a new dep, ask first) or restrict v1 to
  PNG/JPEG? Re-encode-to-normalize or store-as-is?
- **Read-path authz / privacy:** GET stays public; the redirect `Location`
  exposes `employeeID` (org-info leakage / account enumeration). Accept, or gate.
- **Employee-photo 404:** the external host may 404 for a user with no photo →
  broken image in the browser (inherent to the redirect approach). A default is
  only served for *our* lookups, not for the external host's 404.

## 10. Testing plan (TDD)

- **Handler unit tests** (`handler_test.go`, mocked `avatarStore` + a stream
  seam): table-driven over Endpoint 1 (user local hit/miss, cache hit/miss→DB,
  bot local vs cross-cluster, `fwd=1` no-re-redirect, dynamic-default fallback)
  and Endpoint 2 (channel MinIO-hit stream, MinIO-miss→dynamic default,
  dm/botDM→default, remote→307, not-found→default, `If-None-Match`→304). Assert
  status code, `Location`, `Cache-Control`, `ETag`, and body bytes/Content-Type.
- **Upload unit tests** (`upload.go`): accept PNG/JPEG within size; reject
  oversize (`MAX_UPLOAD_BYTES`), reject `image/svg+xml` and non-image bytes,
  reject decode failures; on success store to MinIO + bump `AvatarVersion`;
  `DELETE` removes object + zeroes version; wrong-cluster → rejected with
  guiding error; unauthorized/non-owner → 401/403; assert `nosniff` header.
- **Generation unit tests:** `renderDefaultSVG` is **deterministic** — same
  `(seed, initial)` yields byte-identical SVG *and* the same `ETag` across
  repeated calls; stable colour per seed, correct initial (incl. CJK), valid XML.
- **Integration** (`integration_test.go`, `//go:build integration`): Mongo +
  MinIO from `pkg/testutil`; real GetObject/Stat/stream round-trip for a stored
  custom image, 304 path, and MinIO-miss → dynamic default (nothing written back).
- Coverage ≥80% (target 90% on handler + generation).

## 11. Docs to update on implementation

- `docs/client-api.md` is NATS/auth-HTTP-scoped; avatar-service is a new public
  HTTP surface — add a section there (or link this spec) describing the two
  endpoints, redirect semantics, cache headers, and the default-image behaviour.
- Delete any `docs/reviews/*` working notes before opening a PR.
