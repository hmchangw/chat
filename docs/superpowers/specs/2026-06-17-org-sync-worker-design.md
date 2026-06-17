# org-sync-worker — Design

**Date:** 2026-06-17
**Status:** Approved (design locked)
**Service:** `org-sync-worker`
**Branch:** `claude/blissful-carson-kihdek`
**Source spec:** `spec_from_me_to_claude.md` (Organization Membership Sync Worker Specification)

## Summary

`org-sync-worker` is a site-local JetStream consumer that synchronizes
organization-membership changes from a central HR event stream into local room
memberships. Each site runs its own instance and only mutates rooms it hosts
(`siteId == SITE_ID`). The worker consumes zstd-compressed batches, decompresses
them, and applies idempotent MongoDB writes (create/remove subscriptions +
membership-source records, adjust member counts).

This document adapts the external `spec_from_me_to_claude.md` (written for a
standalone `cmd/`+`internal/` external repo) onto this monorepo: flat-root
`main.go` (no `cmd/`), the `internal/` layout of `history-service`, the root
`go.mod`, and reuse of the existing `pkg/` libraries. **The stream/subject/
consumer-filter design is copied verbatim from the source spec and is not
redesigned** — it matches the already-shipped sibling routing pattern
(`chat.{hr|jos}.{central}.to.{site}.>`).

## 1. Package structure

`main.go` lives at the service root (no `cmd/`). The `internal/` tree mirrors
`history-service`: interfaces are defined in `service.go`, the Mongo layer is
one `mongoutil.Collection[T]` per collection file (no composing "repository"
abstraction — `mongoutil` already is the repository), and integration tests are
split per collection.

```
org-sync-worker/
├── main.go                                   # package main — wiring at root (no cmd/)
├── internal/
│   ├── config/
│   │   ├── config.go                         # config.Load() + Config struct (caarlos0/env)
│   │   └── config_test.go
│   ├── codec/
│   │   ├── codec.go                          # Decoder struct + Decompressor interface (zstd, bomb-defended)
│   │   └── codec_test.go
│   ├── nats/
│   │   └── consumer.go                        # pull-iterator + wg + Stop/Wait; maps result → Ack/Nak/Term
│   ├── service/
│   │   ├── service.go                         # interfaces + OrgSyncService + New() + //go:generate + compile-time checks
│   │   ├── process.go                         # ProcessBatch: decompress(codec) → unmarshal → per-change dispatch
│   │   ├── process_test.go
│   │   ├── membership.go                      # handleMemberAdded / handleMemberRemoved
│   │   ├── membership_test.go                 # unit tests, mocked repos
│   │   ├── disband.go                         # handleOrgDisbanded
│   │   ├── disband_test.go
│   │   └── mocks/
│   │       └── mock_repository.go             # generated (mockgen)
│   └── mongorepo/                             # one collection → one file, thin over mongoutil.Collection[T]
│       ├── subscription.go                    # SubscriptionRepo  (subscriptions)
│       ├── room_member.go                     # RoomMemberRepo    (room_members)
│       ├── room.go                            # RoomRepo          (rooms — UserCount $inc, RoomIDsWithOrg)
│       ├── user.go                            # UserRepo          (users — GetUserByAccount)
│       ├── main_test.go                       # TestMain → testutil.RunTests
│       ├── setup_test.go                      # setupMongo(t) → testutil.MongoDB(t,"org_sync_test")
│       ├── subscription_integration_test.go
│       ├── room_member_integration_test.go
│       ├── room_integration_test.go
│       └── user_integration_test.go
└── deploy/
    ├── Dockerfile
    ├── docker-compose.yml
    └── azure-pipelines.yml
```

Import path: `github.com/hmchangw/chat/org-sync-worker/internal/…`.
Reused root packages: `natsutil`, `mongoutil`, `stream`, `subject`, `idgen`,
`shutdown`, `errcode`, `model`, `health`, `otelutil`, `logctx`, `jobguard`,
`pipelines` (partial — see §14), `testutil` (tests). See §14 for the full
package-reuse audit, including what was deliberately NOT reused.

## 2. Domain mapping (spec vocabulary → real model)

The source spec uses generic vocabulary. The real collections and `pkg/model`
structs:

| Spec term | Collection | `pkg/model` struct | Key fields / constraints |
|---|---|---|---|
| spaces | `rooms` | `Room` | `UserCount` (member count), `SiteID` |
| memberships | `subscriptions` | `Subscription` | unique `(roomId, u.account)` |
| member_records | `room_members` | `RoomMember` / `RoomMemberEntry` | `member.type ∈ {individual, org}`, `member.id`, unique `(rid, member.type, member.id)` |
| users lookup | `users` | `User` | org membership via `SectID` + `DeptID` (not `org1Id/org2Id`) |

- A user belongs to org `orgId` iff `user.SectID == orgId || user.DeptID == orgId`.
- "Rooms on this site that have org `Y` as a member" =
  `room_members{member.type:"org", member.id:Y}` joined to `rooms{siteId:SITE_ID}`.
- **Schema ownership:** the `rooms`, `subscriptions`, `room_members`, `users`
  collections and their indexes are owned by `room-service`/`room-worker`.
  `org-sync-worker` reuses them and MUST NOT create indexes or own schema.

## 3. Stream / subject / consumer — LOCKED (verbatim from source spec)

Copied directly from `spec_from_me_to_claude.md`; not redesigned.

- **Stream:** `HR_{siteID}` (e.g. `HR_00302000`). The dev-only `bootstrap.go`
  sets only `Name` + `Subjects` from `pkg/stream`. Federation config
  (`Sources` + `SubjectTransforms` for cross-site sourcing from the central HR
  site) is owned by ops/IaC and MUST NOT appear in `bootstrap.go` (INBOX
  ownership convention).
- **Consumer filters** (source spec §Consumer Filters):
  ```go
  filters := []string{
      fmt.Sprintf("chat.hr.%s.>",       centralSiteID),          // broadcast to all
      fmt.Sprintf("chat.hr.%s.to.%s.>", centralSiteID, siteID),  // site-specific
  }
  ```
  Example: site-a → `["chat.hr.hr-site.>", "chat.hr.hr-site.to.site-a.>"]`.
- **Consumer:** durable `org-sync-worker-{site-id}`, `AckPolicy=Explicit`,
  `DeliverPolicy=All`, `MaxDeliver=5`, `AckWait=30m`, via
  `stream.ConsumerSettings` (env-tunable `CONSUMER_*`).
- **`pkg/subject` additions** (emit the exact spec strings — no routing change):
  - `HROrgMembershipBroadcast(central) = chat.hr.{central}.org.membership.changed`
  - `HROrgMembershipForSite(central, dest) = chat.hr.{central}.to.{dest}.org.membership.changed`
  - `HRConsumerFilters(central, site) = []string{ "chat.hr."+central+".>", "chat.hr."+central+".to."+site+".>" }`
- **`pkg/stream` addition:** `HR(siteID) Config` → `Name: "HR_"+siteID`,
  `Subjects: []string{"chat.hr."+centralSiteID+".>"}` umbrella so the stream
  carries both filter shapes. (Central site id is supplied to the bootstrap
  helper; in dev `central == local`.)

## 4. Event model (`pkg/model/orgsync.go`)

Inbound DTOs produced by the external HR system. These are **shared NATS event
structs consumed by multiple services**, so they live in root `pkg/model`
(not service-local `internal/models`), per the CLAUDE.md rule that every NATS
event struct lives in `pkg/model`, carries a `Timestamp` field, and is
round-trip tested by `pkg/model/model_test.go`. JSON only.

```go
type OrgMembershipChange struct {
    Type      string `json:"type"`              // member_added | member_removed | org_disbanded
    OrgID     string `json:"orgId"`
    Account   string `json:"account,omitempty"` // omitted for org_disbanded
    Timestamp int64  `json:"timestamp"`         // unix ms when change was detected
}

type OrgMembershipBatch struct {
    Timestamp int64                 `json:"timestamp"` // batch generation time (unix ms)
    Changes   []OrgMembershipChange `json:"changes"`
}
```

Change-type constants live alongside in `pkg/model`. The orgId/account format
validation (see §7) is business logic and stays in `service` (a small
`validateChange` helper), keeping `pkg/model` to data + constants only.

## 5. `service.go` — interfaces + wiring (history-service idiom)

Interfaces are defined in the consumer (`service`) and satisfied by the
concrete `mongorepo` structs via compile-time checks.

```go
//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . SubscriptionRepository,RoomMemberRepository,RoomRepository,UserRepository

type SubscriptionRepository interface {
    GetSubscription(ctx context.Context, roomID, account string) (*model.Subscription, error)
    UpsertSubscription(ctx context.Context, sub model.Subscription) error // $setOnInsert
    DeleteSubscription(ctx context.Context, roomID, account string) error
}

type RoomMemberRepository interface {
    RoomIDsWithOrg(ctx context.Context, orgID, siteID string) ([]string, error)
    HasIndividualRecord(ctx context.Context, roomID, account string) (bool, error)
    HasOrgRecordExcept(ctx context.Context, roomID string, orgIDs []string, excludeOrgID string) (bool, error)
    UpsertIndividualRecord(ctx context.Context, rec model.RoomMember) error // $setOnInsert
    DeleteIndividualRecord(ctx context.Context, roomID, account string) error
    DeleteOrgRecord(ctx context.Context, roomID, orgID string) error
}

type RoomRepository interface {
    IncUserCount(ctx context.Context, roomID string, delta int) error
}

type UserRepository interface {
    GetUserByAccount(ctx context.Context, account string) (*model.User, error)
}

type OrgSyncService struct {
    subs    SubscriptionRepository
    members RoomMemberRepository
    rooms   RoomRepository
    users   UserRepository
    siteID  string
}

func New(subs SubscriptionRepository, members RoomMemberRepository,
    rooms RoomRepository, users UserRepository, siteID string) *OrgSyncService

var _ SubscriptionRepository = (*mongorepo.SubscriptionRepo)(nil)
var _ RoomMemberRepository   = (*mongorepo.RoomMemberRepo)(nil)
var _ RoomRepository         = (*mongorepo.RoomRepo)(nil)
var _ UserRepository         = (*mongorepo.UserRepo)(nil)
```

## 6. `mongorepo` — one collection per file (thin over `mongoutil`)

Each file defines a small `XxxRepo` wrapping `mongoutil.NewCollection[T]`,
exactly like `history-service/internal/mongorepo/{subscription,room}.go`. No
shared repository struct.

- `subscription.go` — `SubscriptionRepo{ subscriptions *mongoutil.Collection[model.Subscription] }`.
  `UpsertSubscription` uses `mongoutil.UpsertModel(filter, bson.M{"$setOnInsert": ...})`
  + `BulkWrite` (the `$set`-based `BulkUpsert` shortcut is NOT used — we must
  not overwrite `joinedAt` on retry).
- `room_member.go` — `RoomMemberRepo{ members *mongoutil.Collection[model.RoomMember] }`.
  `RoomIDsWithOrg` runs an aggregation: match `room_members{member.type:"org",
  member.id:orgID}`, `$lookup` rooms, filter `rooms.siteId == siteID`, project
  `rid` (net-new; `pkg/pipelines` does not cover the orgId→rooms direction).
  Individual upserts use `$setOnInsert`. The org_disbanded "members via org"
  enumeration and the `hasSubscription`/`hasIndividualRoomMember` derivation
  reuse the conventions in `pkg/pipelines/member.go`
  (`GetAddMemberCandidatesPipeline`, the `botOrPseudoAccountRegex` exclusion and
  the sect/dept org match) so the worker stays consistent with room-worker's
  membership semantics.
- `room.go` — `RoomRepo{ rooms *mongoutil.Collection[model.Room] }`.
  `IncUserCount` → `UpdateOne(_id, {"$inc": {"userCount": delta}})` via `Raw()`.
- `user.go` — `UserRepo{ users *mongoutil.Collection[model.User] }`.
  `GetUserByAccount` → `FindOne(bson.M{"account": account})` with a projection
  that **includes `sectId` and `deptId`** (plus `_id`, `account`). We do NOT
  reuse `pkg/userstore.NewMongoStore` here: its `FindUserByAccount` projection
  (`userstore.go:26`) omits `sectId`/`deptId`, which the member_removed /
  org_disbanded "covered by another org" check requires. A hand-rolled
  `UserRepo` with the right projection is the correct call.

## 7. Business logic

All operations are scoped to the local site (`SITE_ID`) and are idempotent.
`OrgSyncService.ProcessChange(ctx, change)` dispatches on `change.Type`.

**member_added** (`membership.go`):
1. `rooms := members.RoomIDsWithOrg(orgId, siteID)`.
2. For each room: if `subs.GetSubscription(room, account) != nil` → skip
   (already a member via another path).
3. Else `user := users.GetUserByAccount(account)`; if not found → log + skip.
4. `subs.UpsertSubscription` (`$setOnInsert`), `members.UpsertIndividualRecord`
   (`$setOnInsert`, `member.type:"individual", member.id:user.ID,
   member.account:account`), `rooms.IncUserCount(room, +1)`.

**member_removed** (`membership.go`):
1. `rooms := members.RoomIDsWithOrg(orgId, siteID)`.
2. For each room: keep (skip) if `members.HasIndividualRecord(room, account)`
   (explicitly added).
3. `user := users.GetUserByAccount(account)`; keep if
   `members.HasOrgRecordExcept(room, [user.SectID, user.DeptID], orgId)` (still
   covered by a different org in this room).
4. Else `subs.DeleteSubscription`, `members.DeleteIndividualRecord`,
   `rooms.IncUserCount(room, -1)`.

**org_disbanded** (`disband.go`):
1. `rooms := members.RoomIDsWithOrg(orgId, siteID)`.
2. For each room: enumerate members that joined via this org (users in the org
   with a subscription in the room); for each, apply the same keep-checks as
   member_removed; remove the non-covered ones (`DeleteSubscription` +
   `DeleteIndividualRecord` + `IncUserCount(-1)` each).
3. `members.DeleteOrgRecord(room, orgId)` (remove the org membership-source row).

**Validation (security, source spec §Message Validation):** a service-level
`validateChange(change)` rejects malformed `orgId`/`account` (charset/length
allowlist) before any DB work; a validation failure is a per-change skip
(logged), not a batch failure.

**Partial-batch semantics:** `ProcessBatch` continues on per-change errors,
counting successes. Ack if ≥1 change succeeded; `NakWithDelay(30s)` if every
change failed (transient) ; `Term`/Ack-poison if the batch itself is
undecodable (see §9).

## 8. `internal/codec` — zstd decompression

A descriptive, NATS-agnostic helper package (not `utils`/`helpers`). Defines a
struct + interface because the performant pattern reuses one `*zstd.Decoder`.

```go
type Decompressor interface {
    Decompress(data []byte) ([]byte, error)
}

type Decoder struct { d *zstd.Decoder }

// New builds a reusable decoder with single-thread concurrency and a 16MB
// decoded-size cap (decompression-bomb defense).
func New() (*Decoder, error) // zstd.NewReader(nil, WithDecoderConcurrency(1), WithDecoderMaxMemory(16<<20))

func (d *Decoder) Decompress(data []byte) ([]byte, error) // d.DecodeAll(data, nil); empty input → error
```

`github.com/klauspost/compress` is already a (transitive) dependency; this
promotes it to direct use — no new third-party dependency. `service` depends on
the `Decompressor` interface so `process_test.go` can inject a fake.

## 9. Consumer + error handling (`internal/nats/consumer.go`)

Reuses the inbox-worker pull-consumer pattern (OTel via `oteljetstream`,
`jobguard.Guard` panic recovery per message, `natsutil.StampRequestID` →
request-id in every log line). The consumer owns Ack/Nak/Term mechanics and
calls `service.ProcessBatch(ctx, msg.Data()) (acked int, err error)`.

- **Sequential consume** (`cons.Consume()`), NOT a worker pool. HR batches are
  daily/low-volume, and concurrent batch processing could reorder a later
  `member_removed` ahead of an earlier `member_added` for the same user.
  `MAX_WORKERS` is retained in config for forward-compat but the default path is
  sequential.
- **Term / Ack-poison** (no retry): decompress failure, JSON parse failure,
  batch size > `MAX_BATCH_SIZE`. Surfaced as `errcode.Permanent`; the consumer
  Acks (poison-drop) per the repo idiom (equivalent to the spec's `Term`).
- **Nak / NakWithDelay(30s)** (retry): Mongo timeout/contention and other
  transient infra errors (`errcode.IsPermanent` == false).
- Graceful shutdown via `shutdown.Wait(25s)`: stop iterator → drain in-flight
  (wg, with timeout) → `nc.Drain()` → tracer shutdown →
  `mongoutil.Disconnect` → health stop.

## 10. Configuration (`internal/config`, caarlos0/env)

```
SITE_ID                          required   site identifier
CENTRAL_SITE_ID                  required   central HR stream site (builds filters)
NATS_URL                         required
NATS_CREDS_FILE                  optional
MONGO_URI                        required
MONGO_DB                         default chat
MAX_WORKERS                      default 100   (retained; sequential consume is default)
HRSYNC_BATCH_MAX_COMPRESSED_SIZE default 65536 (64KB compressed cap)
MAX_BATCH_SIZE                   default 1000  (max changes per batch → Term if exceeded)
HEALTH_ADDR                      default :8080
CONSUMER_*                       stream.ConsumerSettings (ACK_WAIT, MAX_DELIVER, …)
BOOTSTRAP_STREAMS                default false (dev sets true)
```

Fail fast on missing required vars and non-positive numeric knobs (history
-service `checkConfig` pattern). `MONGO_DB` default `chat`.

## 11. Testing (TDD, 80%+ floor, 90%+ on service)

- **Unit** (mocked repos via `internal/service/mocks`): `membership_test.go`,
  `disband_test.go`, `process_test.go` (decompress fake + dispatch), table-
  driven covering happy path, store errors, edge cases (user-not-found,
  already-subscribed, covered-by-other-org, empty batch). Plus `config_test.go`
  and `codec_test.go` (valid, empty, oversized/bomb). The event DTO round-trip
  is covered by `pkg/model/model_test.go`.
- **Integration** (`//go:build integration`): `mongorepo/main_test.go` →
  `testutil.RunTests(m)`, `setup_test.go` → `testutil.MongoDB(t,
  "org_sync_test")`; split per collection: `subscription_integration_test.go`,
  `room_member_integration_test.go`, `room_integration_test.go`,
  `user_integration_test.go`. An end-to-end JetStream flow test (publish a
  zstd batch → assert subscription/count) under `internal/nats` or
  `internal/service` using `testutil.NATS(t)`.
- `make generate` regenerates `internal/service/mocks/mock_repository.go` before
  testing. `-race` always (Makefile).

## 12. Deployment

- **Dockerfile** (`deploy/Dockerfile`): multi-stage `golang:1.25.11-alpine`
  builder → `alpine:3.21` runtime, build context = repo root (copies `go.mod`,
  `go.sum`, `pkg/`, `org-sync-worker/`), `go build -o /org-sync-worker
  ./org-sync-worker`, non-root user.
- **docker-compose.yml**: NATS (`--jetstream --http_port 8222`), MongoDB,
  worker; env includes `BOOTSTRAP_STREAMS=true`, `SITE_ID=site-local`,
  `CENTRAL_SITE_ID=site-local`.
- **azure-pipelines.yml**: path-filtered to `org-sync-worker/` + `pkg/`;
  validate (vet/test/build) + Docker build on main.

## 13. Locked decisions

1. Flat-root `main.go` (no `cmd/`); `internal/` layout per history-service.
2. Stream/subject/consumer filters copied verbatim from the source spec — not
   redesigned (matches sibling `chat.{hr|jos}.{central}.to.{site}.>` routing).
3. One `mongoutil.Collection[T]` per collection file; no composing repository
   struct; interfaces defined in `service.go` with compile-time checks.
4. zstd decompression isolated in `internal/codec` (`Decoder` struct +
   `Decompressor` interface); orchestration stays in `service/process.go`.
5. Event DTOs in root `pkg/model/orgsync.go` (shared by multiple services;
   round-trip tested by `pkg/model/model_test.go`).
6. Sequential consume (ordering safety); `MAX_WORKERS` retained but unused by
   default.
7. `$setOnInsert` upserts via `mongoutil.UpsertModel` + `BulkWrite` (not the
   `$set` `BulkUpsert` shortcut).
8. org-sync-worker reuses existing collections/indexes; owns no schema.

## 14. Package-reuse audit

Full sweep of `pkg/` for reusable logic beyond the obvious infra packages.

**Reused:**
- `pkg/pipelines` — **partial.** `member.go` encodes the shared sect/dept org
  match, the `botOrPseudoAccountRegex` exclusion, and the
  `hasSubscription`/`hasIndividualRoomMember` derivation
  (`GetNewMembersPipeline`, `GetAddMemberCandidatesPipeline`). Reused for the
  org_disbanded "members via org" enumeration to stay consistent with
  room-worker. Does NOT cover the worker's primary orgId→rooms query
  (`RoomIDsWithOrg` is net-new).
- Infra (already planned): `natsutil`, `mongoutil`, `stream`, `subject`,
  `idgen`, `shutdown`, `errcode`, `model`, `health`, `otelutil`, `logctx`,
  `jobguard`, `testutil`.

**Deliberately NOT reused:**
- `pkg/userstore` — its `FindUserByAccount` projection (`userstore.go:26`) omits
  `sectId`/`deptId`, which the member_removed / org_disbanded org-coverage check
  requires. A service-local `UserRepo` with the correct projection is used
  instead. (The process-local LRU cache it offers is also unnecessary at the
  worker's daily-batch volume.)
- `pkg/roommetacache`, `pkg/roomsubcache`, `pkg/valkeyutil` — caching/L2
  invalidation only valuable for read-serving, fan-out paths; not needed for a
  low-volume write-only sync worker.
- `pkg/displayfmt` — name-formatting for read-time enrichment; the worker writes
  IDs/accounts, not display names.
- Not applicable to this worker's domain: `cassutil`, `msgbucket`, `atrest`
  (Cassandra/at-rest), `roomcrypto`, `roomkeystore`, `roomkeysender`,
  `roomkeymetrics` (room encryption), `searchengine`, `searchindex` (search),
  `natsrouter` (request-reply, not pull-consumer), `restyutil`, `drive`, `oidc`,
  `minioutil`, `mention`, `emoji`.

## 15. Client API doc

Not applicable: `org-sync-worker` registers no `chat.user.…` client-facing
handler and exposes no HTTP route (other than `/healthz`). No `docs/client-api.md`
change required.
