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
| `avatar.go` | `generateRoomAvatarSVG(room)` + object-key helpers (reusable internal func) |
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

## 4. Common mechanisms

### 4.1 Proxy streaming (the terminal "serve image" step)

When the avatar lives in *this* cluster's MinIO, the handler relays the bytes
rather than redirecting again:

1. `obj, err := mc.GetObject(ctx, bucket, key, opts)` — `*minio.Object` is an `io.Reader`.
2. `st, err := obj.Stat()` → `Size`, `ContentType`, `ETag`.
   - NotFound → serve the **default image** (same streaming path).
   - other error → `fmt.Errorf("stat object: %w", err)` → collapses to `internal`.
3. Set `Cache-Control: public, max-age=<cfg>` and `ETag: st.ETag`.
4. If request `If-None-Match` == `st.ETag` → `304 Not Modified`, no body.
5. `c.DataFromReader(http.StatusOK, st.Size, st.ContentType, obj, nil)` — streams.

Rationale: the cacheable URL stays stable (avatar-service's own URL), so
`Cache-Control` actually works and `ETag` enables conditional GET. Redirecting
to a MinIO presigned URL would defeat caching (expiring `Location`) and add a hop.

### 4.2 Cross-cluster loop breaker (`?fwd=1`)

A request resolves to at most **one** cross-cluster hop. When forwarding,
append `?fwd=1`. A handler that sees `fwd=1` MUST resolve locally or fall back
to the default image — it MUST NOT redirect cross-cluster again. The default
image is the universal backstop that guarantees termination.

### 4.3 Caching

- Baseline: `ETag` (from the MinIO object) + `Cache-Control: public, max-age`.
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

- `Cache-Control: public, max-age=<cfg>` on every response (incl. redirects).
- **Correctness rule:** a mapping-cache *miss* falls back to the DB; it must
  **not** skip to the default branch (that would give a real user the wrong
  avatar). The cache is an accelerator only — bounded LRU + TTL.
- `accountName`, `eid` are validated/escaped (`url.PathEscape`, allowlist
  regex) before being placed in a redirect `Location`.

## 7. Endpoint 2 — `GET /avatar/v1/room/:roomID`

```text
room, found := store.Room(ctx, roomID)
if !found:                          serveDefault()        # incl. room owned by a cluster we can't resolve
if room.Type in {dm, botDM}:        serveDefault()        # frontend should use Endpoint 1 instead
# channel / discussion:
if room.SiteID != cfg.SiteID && !fwd:
    307 → https://{clusterDomain(room.SiteID)}/avatar/v1/room/{roomID}?fwd=1
key := roomAvatarKey(roomID)
obj, err := mc.GetObject(ctx, bucket, key)
if NotFound:
    singleflight(roomID): generateRoomAvatarSVG(room) → put(key)   # lazy-on-miss
    obj = mc.GetObject(ctx, bucket, key)
proxyStream(obj)                    # any residual failure → serveDefault()
```

- dm/botDM are **user-type** avatars; the frontend fetches them via Endpoint 1
  using the counterpart user / bot account. If such a roomID nonetheless lands
  here, return the default image (safe, not a 4xx).
- Cross-cluster for rooms is a **defensive fallback**: normally the frontend
  resolves the owning domain first (via the existing room-location service) and
  hits the correct cluster directly. If this cluster has no record of the room,
  it serves the default image (it cannot know the owning siteID to forward).

## 8. Image generation & storage

- **Default / generated room image** is an **SVG** "initials" avatar: the room
  name's first glyph(s) on a background colour derived from a hash of `roomID`.
  SVG means **zero new dependencies** and CJK room names render via the
  client's system fonts (no embedded multi-MB font). `Content-Type:
  image/svg+xml`.
- `generateRoomAvatarSVG(room)` is a **standalone, reusable internal function**.
  The lazy path calls it on a MinIO miss (guarded by `singleflight` to collapse
  concurrent first-requests). A future room-creation hook can call the *same*
  function to pre-warm — generation logic stays in one place.
- A **room rename** changes name-derived initials → regenerate + bump `version`.
- **Storage:** image bytes live in **MinIO** (bucket `AVATAR_BUCKET`, key by
  convention e.g. `room/{roomID}.svg`). Mongo holds at most a minimal reference
  `{ minioKey, version }`; if the key is fully derivable from `roomID`, the DB
  reference may be omitted and existence checked directly against MinIO.
- A static **fallback default image** (when generation/lookup fails) is embedded
  via `go:embed` or stored at a fixed MinIO key.

## 9. Open questions / deferred

- **Authz:** endpoints are public; the redirect `Location` exposes `employeeID`
  (org-info leakage / account enumeration). Accept as public, or gate later.
- **Employee-photo 404:** the external host may 404 for a user with no photo →
  broken image in the browser (inherent to the redirect approach). A default is
  only served for *our* lookups, not for the external host's 404.
- **`?v` cache-busting** is reserved but not wired in v1.
- **Avatar upload** API is out of scope for v1.

## 10. Testing plan (TDD)

- **Handler unit tests** (`handler_test.go`, mocked `avatarStore` + a stream
  seam): table-driven over Endpoint 1 (user local hit/miss, cache hit/miss→DB,
  bot local vs cross-cluster, `fwd=1` no-re-redirect, default fallback) and
  Endpoint 2 (channel local stream, lazy-generate-on-miss, dm/botDM→default,
  remote→307, not-found→default, `If-None-Match`→304). Assert status code,
  `Location`, `Cache-Control`, `ETag`, and body bytes/Content-Type.
- **Generation unit tests:** `generateRoomAvatarSVG` produces deterministic,
  well-formed SVG (stable colour per roomID, correct initial, valid XML).
- **Integration** (`integration_test.go`, `//go:build integration`): Mongo +
  MinIO from `pkg/testutil`; real GetObject/Stat/stream round-trip, 304 path,
  lazy generation persists to MinIO.
- Coverage ≥80% (target 90% on handler + generation).

## 11. Docs to update on implementation

- `docs/client-api.md` is NATS/auth-HTTP-scoped; avatar-service is a new public
  HTTP surface — add a section there (or link this spec) describing the two
  endpoints, redirect semantics, cache headers, and the default-image behaviour.
- Delete any `docs/reviews/*` working notes before opening a PR.
