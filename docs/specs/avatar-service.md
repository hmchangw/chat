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

Two public endpoints:

| Endpoint | Purpose |
|----------|---------|
| `GET /avatar/v1/:accountName` | User **and** bot avatar (frontend routes dm/botDM room avatars here too) |
| `GET /avatar/v1/room/:roomID` | Room avatar — **channel / discussion only** |

Non-goals: avatar **upload** API (out of scope for v1), per-size rendering
(`_120` is fixed for the employee-photo redirect), and authn/authz (endpoints
are public; see §9 Open Questions).

## 2. Service shape

A new flat service `avatar-service/` at repo root, following the per-service
layout. **It does not use NATS** — it is a pure read HTTP service.

| File | Responsibility |
|------|----------------|
| `main.go` | Config (`caarlos0/env`), wire Mongo + MinIO clients, Gin server + timeouts, graceful shutdown (`pkg/shutdown.Wait`) |
| `routes.go` | Register `GET /avatar/v1/:accountName`, `GET /avatar/v1/room/:roomID`, `GET /healthz` |
| `handler.go` | Request handling + the resolve/redirect/stream decision tree |
| `avatar.go` | `renderDefaultSVG(seed, initial)` pure deterministic generator + object-key helpers |
| `store.go` | `avatarStore` interface (only the methods the handler needs) + `//go:generate mockgen` |
| `store_mongo.go` | Mongo implementation (`users`, `rooms`, bot lookup) |
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
| `MINIO_ENDPOINT` / `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | object storage | required |
| `AVATAR_BUCKET` | MinIO bucket for avatars | `avatars` |
| `CACHE_MAX_AGE_SECONDS` | `Cache-Control: public, max-age=` value | `21600` (6h) |

`CLUSTER_DOMAINS` maps a `siteID` to the base URL of *that cluster's*
avatar-service (e.g. `xxx-2=https://avatar-service-xxx-2`). Cross-cluster
redirects and `EMPLOYEE_PHOTO_BASE_URL` are **config**, never hardcoded.

The MinIO vars (`MINIO_*`, `AVATAR_BUCKET`) are **conditional** on the §9
decision: if v1 holds no custom images, MinIO is dropped and these vars go away.

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
- Forward-compatible: a `?v={version}` query param (version carried in room
  metadata the frontend already holds). When present, the response MAY use a
  long `max-age` + `immutable`. Not required for v1; the metadata just reserves
  the `version` field so it can be added without a breaking change.

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
    url, found := store.BotLocationURL(ctx, localPart)   # local DB
    if found: 307 → url
    else:     serveDefault()
else:                                        # ── user (synced; domain informational)
    eid, found := cache[localPart]
    if !found: eid, found = store.EmployeeID(ctx, localPart)   # MISS MUST hit DB
    if found:
        cache.put(localPart, eid)
        307 → {EMPLOYEE_PHOTO_BASE_URL}/xxxPhoto/po/{eid}_120.JPG
    else:
        serveDefault()
```

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
key := roomAvatarKey(roomID)
obj, err := mc.GetObject(ctx, bucket, key)
if found:        proxyStream(obj)          # stored custom image (§4.1)
else:            serveDefault(room)         # MinIO miss → dynamic SVG (§8), NOT stored
```

- The generated default is **never written back** to MinIO. MinIO is read-only
  here and only holds custom/uploaded images; a miss is answered on the fly.
- dm/botDM are **user-type** avatars; the frontend fetches them via Endpoint 1
  using the counterpart user / bot account. If such a roomID nonetheless lands
  here, return the dynamic default (safe, not a 4xx).
- Cross-cluster for rooms is a **defensive fallback**: normally the frontend
  resolves the owning domain first (via the existing room-location service) and
  hits the correct cluster directly. If this cluster has no record of the room,
  it serves the default image (it cannot know the owning siteID to forward).

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

### 8.4 MinIO's remaining role (custom images only)

MinIO holds **only** custom/uploaded images (a future feature — no upload API in
v1). The lazy "generate-and-store" + create-time pre-warm hook are **no longer
needed for defaults** (a deterministic default needs no warming); they become
relevant only if/when custom uploads land. Whether v1 talks to MinIO at all is
an open question — see §9. A shared `renderDefaultSVG` keeps the rendering in
one place regardless.

## 9. Open questions / deferred

- **Authz:** endpoints are public; the redirect `Location` exposes `employeeID`
  (org-info leakage / account enumeration). Accept as public, or gate later.
- **Employee-photo 404:** the external host may 404 for a user with no photo →
  broken image in the browser (inherent to the redirect approach). A default is
  only served for *our* lookups, not for the external host's 404.
- **Does v1 talk to MinIO at all?** With the default generated dynamically and
  never stored, MinIO only matters for *custom/uploaded* images. If v1 has no
  way to populate MinIO (no upload, no pre-warm), Endpoint 2 should skip the
  MinIO round-trip entirely and generate directly — dropping the MinIO client,
  bucket config, and proxy-stream path from v1. **DECISION NEEDED** before
  Phase 0; the spec currently keeps MinIO as the custom-image source.
- **`?v` cache-busting** is reserved but not wired in v1.
- **Avatar upload** API is out of scope for v1.

## 10. Testing plan (TDD)

- **Handler unit tests** (`handler_test.go`, mocked `avatarStore` + a stream
  seam): table-driven over Endpoint 1 (user local hit/miss, cache hit/miss→DB,
  bot local vs cross-cluster, `fwd=1` no-re-redirect, dynamic-default fallback)
  and Endpoint 2 (channel MinIO-hit stream, MinIO-miss→dynamic default,
  dm/botDM→default, remote→307, not-found→default, `If-None-Match`→304). Assert
  status code, `Location`, `Cache-Control`, `ETag`, and body bytes/Content-Type.
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
