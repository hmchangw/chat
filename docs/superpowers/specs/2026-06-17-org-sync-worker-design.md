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
│   ├── consumer/
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
│       ├── room.go                            # RoomRepo          (rooms — ReconcileUserCount recompute-and-$set)
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
- **Consumer filters** (source spec §Consumer Filters). The two filter strings
  are these (broadcast-to-all + site-specific):
  ```
  chat.hr.{central}.>          // broadcast to all sites
  chat.hr.{central}.to.{site}.> // site-specific
  ```
  Example: site-a → `["chat.hr.hr-site.>", "chat.hr.hr-site.to.site-a.>"]`.
  The consumer MUST build these via the `pkg/subject` builder
  (`subject.HRConsumerFilters(centralSiteID, siteID)`), **never** raw
  `fmt.Sprintf` (CLAUDE.md §3 NATS Subject Naming). The source spec shows
  `fmt.Sprintf` only as illustration; the emitted strings are identical.
- **Consumer:** durable `org-sync-worker-{site-id}`, `AckPolicy=Explicit`,
  `DeliverPolicy=All`, `MaxDeliver=5`, `AckWait=30m`, via
  `stream.ConsumerSettings` (env-tunable `CONSUMER_*`).
- **`pkg/subject` additions** (emit the exact spec strings — no routing change):
  - `HROrgMembershipBroadcast(central) = chat.hr.{central}.org.membership.changed`
  - `HROrgMembershipForSite(central, dest) = chat.hr.{central}.to.{dest}.org.membership.changed`
  - `HRConsumerFilters(central, site) = []string{ "chat.hr."+central+".>", "chat.hr."+central+".to."+site+".>" }`
- **`pkg/stream` addition:** `HR(siteID, centralSiteID) Config` → `Name: "HR_"+siteID`,
  `Subjects: []string{"chat.hr."+centralSiteID+".>"}` umbrella so the stream
  carries both filter shapes. **Signature takes both** the local `siteID` (stream
  name) and `centralSiteID` (subject token); `bootstrap.go` passes both from
  config. In dev `central == local`.

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
    // UpsertSubscription is $setOnInsert; a duplicate-key (E11000) on the
    // (roomId, u.account) unique index under concurrency is treated as success
    // (the doc already exists), NOT a poison error.
    UpsertSubscription(ctx context.Context, sub model.Subscription) error
    DeleteSubscription(ctx context.Context, roomID, account string) error
}

type RoomMemberRepository interface {
    // AccountsViaOrg returns the accounts of users in orgID (sectId|deptId==orgID)
    // that currently hold a subscription in roomID — the org_disbanded /
    // member_removed candidate set. See §7 for the enumeration limitation.
    AccountsViaOrg(ctx context.Context, roomID, orgID, siteID string) ([]string, error)
    HasIndividualRecord(ctx context.Context, roomID, account string) (bool, error)
    // HasOrgRecordExcept reports whether roomID has an org member-record whose
    // member.id ∈ orgIDs but ≠ excludeOrgID. Implemented as
    // {member.id: {$in: orgIDs, $ne: excludeOrgID}} so sectId==deptId==orgID
    // correctly resolves to "no other coverage".
    HasOrgRecordExcept(ctx context.Context, roomID string, orgIDs []string, excludeOrgID string) (bool, error)
    // UpsertIndividualRecord upserts a model.RoomMember (ID via idgen.GenerateUUIDv7,
    // Ts set, member.type:"individual"); $setOnInsert.
    UpsertIndividualRecord(ctx context.Context, rec model.RoomMember) error
    DeleteIndividualRecord(ctx context.Context, roomID, account string) error
    DeleteOrgRecord(ctx context.Context, roomID, orgID string) error
}

type RoomRepository interface {
    // RoomsWithOrg returns the local rooms (siteId==siteID) that have orgID as an
    // org member-record, projected to the fields needed to build a subscription
    // (_id, siteId, name, type). Net-new; pkg/pipelines covers only room→users.
    RoomsWithOrg(ctx context.Context, orgID, siteID string) ([]model.Room, error)
    // ReconcileUserCount recomputes userCount (non-bot subs) + appCount (bot subs)
    // and writes both with $set — idempotent under JetStream redelivery, same
    // recompute discipline as room-worker's ReconcileMemberCounts (NOT $inc).
    ReconcileUserCount(ctx context.Context, roomID string) error
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
  `AccountsViaOrg` aggregates the `users` collection — match `sectId|deptId ==
  orgID` (projection must include `sectId`/`deptId`), `$lookup` subscriptions
  filtered to `roomID`, keep accounts with a subscription — reusing the sect/dept
  org-match shape from `pkg/pipelines/member.go` for consistency with
  room-worker. `UpsertIndividualRecord` stamps `ID: idgen.GenerateUUIDv7()`,
  `Ts: now`, `Member: {type:"individual", id:user.ID, account}` and writes with
  `$setOnInsert`.
- `room.go` — `RoomRepo{ rooms *mongoutil.Collection[model.Room] }`.
  `RoomsWithOrg` aggregates `room_members{member.type:"org", member.id:orgID}`,
  `$lookup` rooms, filter `rooms.siteId == siteID`, replace-root to the room doc
  projected to `_id, siteId, name, type` (everything `newSub` needs).
  `ReconcileUserCount` recomputes via `Raw().CountDocuments` — `total =
  count{roomId}`, `appCount = count{roomId, u.isBot:true}` — then
  `UpdateOne(_id, {"$set": {userCount: total-appCount, appCount, updatedAt}})`.
  This mirrors `room-worker`'s `ReconcileMemberCounts` (idempotent under
  redelivery; a transient count error must NOT fall through to a zero-count
  `$set`).
- `user.go` — `UserRepo{ users *mongoutil.Collection[model.User] }`.
  `GetUserByAccount` → `FindOne(bson.M{"account": account})` with a projection
  that **includes `sectId` and `deptId`** (plus `_id`, `account`). We do NOT
  reuse `pkg/userstore.NewMongoStore` here: its `FindUserByAccount` projection
  (`userstore.go:26`) omits `sectId`/`deptId`, which the member_removed /
  org_disbanded "covered by another org" check requires. A hand-rolled
  `UserRepo` with the right projection is the correct call.

Pipeline shapes for the two net-new aggregations (for test authors):

```
// RoomsWithOrg(orgID, siteID) — source: room_members
[ {$match: {"member.type":"org", "member.id":orgID}},
  {$lookup: {from:"rooms", localField:"rid", foreignField:"_id", as:"r"}},
  {$unwind:"$r"}, {$match: {"r.siteId":siteID}},
  {$replaceRoot:{newRoot:"$r"}},
  {$project: {_id:1, siteId:1, name:1, type:1}} ]

// AccountsViaOrg(roomID, orgID, siteID) — source: users
[ {$match: {$or:[{sectId:orgID},{deptId:orgID}], siteId:siteID}},
  {$lookup: {from:"subscriptions", let:{a:"$account"},
     pipeline:[{$match:{$expr:{$and:[{$eq:["$roomId",roomID]},{$eq:["$u.account","$$a"]}]}}},{$limit:1},{$project:{_id:1}}],
     as:"sub"}},
  {$match: {sub:{$ne:[]}}}, {$project:{_id:0, account:1}} ]
```

## 7. Business logic

All operations are scoped to the local site (`SITE_ID`) and are idempotent.
`OrgSyncService.ProcessChange(ctx, change)` dispatches on `change.Type`.

**Member-count discipline (applies to all three handlers):** member counts are
NEVER adjusted with `$inc`. After a handler finishes mutating subscriptions in a
room, it calls `rooms.ReconcileUserCount(room)` once, which recomputes
`userCount`/`appCount` from the actual subscription state with `$set`. This is
**idempotent under JetStream redelivery** (re-processing a batch recomputes the
same value) and uses the **same recompute-and-`$set` discipline as room-worker**.
It is NOT a hard mutual-exclusion: a count-then-`$set` interleaving window
remains (two reconcilers can each count, then write, and a stale write can land)
— the same residual race room-worker's `ReconcileMemberCounts` already has. It
self-heals on the next reconcile of that room; the design accepts eventual
consistency on the counter rather than introducing cross-service locking. Bot/
pseudo accounts are identified with `model.IsBotAccount(account)` and stamped on
`sub.u.isBot` at creation so the recompute counts non-bots into `userCount`
exactly as room-worker does.

(Source-spec note: `spec_from_me_to_claude.md` shows `incrementMemberCount` /
`decrementMemberCount` pseudocode. This design intentionally substitutes
recompute-and-`$set` for `$inc` to gain redelivery idempotency and room-worker
convergence — a deliberate, documented deviation from the illustrative
pseudocode, not the routing/contract.)

**member_added** (`membership.go`):
1. `rooms := rooms.RoomsWithOrg(orgId, siteID)` (full `model.Room` docs).
2. For each room: if `subs.GetSubscription(room.ID, account) != nil` → skip
   (already a member via another path).
3. Else `user := users.GetUserByAccount(account)`; if not found → log + skip.
4. Build the subscription with the SAME field set as room-worker's `newSub`
   (room-worker/handler.go:1229): `ID: idgen.GenerateUUIDv7()`,
   `User: {ID:user.ID, Account:account, IsBot:model.IsBotAccount(account)}`,
   `RoomID:room.ID`, `SiteID:room.SiteID`, `Roles:[]model.Role{model.RoleMember}`,
   `Name:room.Name`, `RoomType:room.Type`, `JoinedAt: now`. `subs.UpsertSubscription`
   (`$setOnInsert`).
5. `members.UpsertIndividualRecord` (`ID: idgen.GenerateUUIDv7()`, `Ts: now`,
   `member.type:"individual", member.id:user.ID, member.account:account`).
6. `rooms.ReconcileUserCount(room.ID)`.

**member_removed** (`membership.go`):
1. `rooms := rooms.RoomsWithOrg(orgId, siteID)`.
2. For each room: keep (skip) if `members.HasIndividualRecord(room.ID, account)`
   (explicitly added).
3. `user := users.GetUserByAccount(account)`; keep if
   `members.HasOrgRecordExcept(room.ID, [user.SectID, user.DeptID], orgId)`
   (still covered by a different org — `$in/$ne` semantics, see §5).
4. Else `subs.DeleteSubscription`, `members.DeleteIndividualRecord`,
   `rooms.ReconcileUserCount(room.ID)`.

**org_disbanded** (`disband.go`):
1. `rooms := rooms.RoomsWithOrg(orgId, siteID)`.
2. For each room: `accounts := members.AccountsViaOrg(room.ID, orgId, siteID)`.
   For each account apply the same keep-checks as member_removed
   (`HasIndividualRecord`, `HasOrgRecordExcept`); `DeleteSubscription` +
   `DeleteIndividualRecord` for the non-covered ones.
3. `members.DeleteOrgRecord(room.ID, orgId)` (remove the org membership-source row).
4. `rooms.ReconcileUserCount(room.ID)` once per room (after all removals).

**Enumeration limitation (org_disbanded / member_removed):** the data model does
NOT record which org introduced an individual `room_members` row (individual
rows carry `member.id:user.ID`, no back-link to the org). So "members via org X"
is approximated by **current** users in org X (`sectId|deptId==orgID`) holding a
subscription. A user who left org X *before* the disband is no longer matched
here — they are expected to have been removed by their own earlier
`member_removed` event. This is an accepted limitation, consistent with the
source spec's reliance on the per-change event stream; it is not a silent
divergence. `DeleteOrgRecord` always removes the single org member-source row
regardless.

**Validation (security, source spec §Message Validation):** a service-level
`validateChange(change)` rejects malformed input before any DB work — `type` ∈
{member_added, member_removed, org_disbanded}; `orgId` non-empty and matching
`^[A-Za-z0-9._-]{1,128}$`; `account` (required except for org_disbanded) matching
the same allowlist. A validation failure is a per-change skip (logged with
`type`/`orgId`), not a batch failure. The allowlist blocks operator/regex
injection into the Mongo filters built from these values.

**Within-batch ordering:** changes are applied in array order; because every
write is idempotent and counts are reconciled (not incremented), a duplicated or
out-of-order pair for the same `(account, orgId)` converges to the correct state.

**Partial-batch semantics:** `ProcessBatch` continues on per-change errors,
counting successes. Ack if ≥1 change succeeded; `NakWithDelay(30s)` if every
change failed (transient); `Term` if the batch itself is undecodable (see §9).
`Nats-Msg-Id` (source §Message Format) provides JetStream-level dedup; combined
with reconcile-and-`$set` counts, redelivery is fully idempotent.

**First-run / oversized-batch contract:** the source spec's first-run behavior
publishes "all current org members as `member_added`". A batch with more than
`MAX_BATCH_SIZE` (default 1000) changes is `Term`-ed as poison (§9) — so the HR
**producer owns chunking**: first-run cohorts MUST be split into ≤`MAX_BATCH_SIZE`
batches. An over-cap batch is treated as a producer-contract violation, not a
worker bug; the worker logs `batch too large` (with `count`/`max`) so first-run
deployments are observable. Operators should raise `MAX_BATCH_SIZE` only in
lockstep with the producer's chunk size.

## 8. `internal/codec` — zstd decompression

A descriptive, NATS-agnostic helper package (not `utils`/`helpers`). Defines a
struct + interface because the performant pattern reuses one `*zstd.Decoder`.

```go
type Decompressor interface {
    Decompress(data []byte) ([]byte, error)
}

type Decoder struct {
    d           *zstd.Decoder
    maxCompressed int // HRSYNC_BATCH_MAX_COMPRESSED_SIZE (64KB default)
}

// New builds a reusable decoder with single-thread concurrency, a 16MB
// decoded-size cap (decompression-bomb defense), checksum verification
// disabled (perf), and a compressed-input cap.
func New(maxCompressed int) (*Decoder, error)
// zstd.NewReader(nil, WithDecoderConcurrency(1), WithDecoderMaxMemory(16<<20), IgnoreChecksum(true))

// Decompress rejects empty input and input larger than maxCompressed BEFORE
// decoding (the 64KB compressed cap from source §Compression), then
// d.DecodeAll(data, nil). Over-cap / decode errors return a sentinel the
// consumer maps to Term (poison).
func (d *Decoder) Decompress(data []byte) ([]byte, error)
```

`github.com/klauspost/compress` is already a (transitive) dependency; this
promotes it to direct use — no new third-party dependency. `service` depends on
the `Decompressor` interface so `process_test.go` can inject a fake. Both the
16MB decompressed cap and the 64KB compressed cap are enforced.

## 9. Consumer + error handling (`internal/consumer/consumer.go`)

Reuses the inbox-worker pull-consumer pattern (OTel via `oteljetstream`,
`jobguard.Guard` panic recovery per message, `natsutil.StampRequestID` →
request-id in every log line). The consumer owns Ack/Nak/Term mechanics and
calls `service.ProcessBatch(ctx, msg.Data()) (acked int, err error)`.

- **Sequential consume** (`cons.Consume()`), NOT a worker pool. HR batches are
  daily/low-volume, and concurrent batch processing could reorder a later
  `member_removed` ahead of an earlier `member_added` for the same user.
  `MAX_WORKERS` is retained in config for forward-compat but the default path is
  sequential.
- **Dedup:** the consumer honors the `Nats-Msg-Id` header (source §Message
  Format). With JetStream dedup plus reconcile-and-`$set` counts, redelivery is
  idempotent at both layers.
- **Term** (no retry, poison): decompress failure, compressed input over
  `HRSYNC_BATCH_MAX_COMPRESSED_SIZE`, JSON parse failure, batch size >
  `MAX_BATCH_SIZE`. The service returns these as `errcode.Permanent`; the
  consumer calls `msg.Term()` (preserves the source spec's reject-not-processed
  semantics; `oteljetstream` supports `Term`).
- **Nak / NakWithDelay(30s)** (retry): Mongo timeout/contention and other
  transient infra errors (`errcode.IsPermanent` == false), and the all-changes
  -failed batch case.
- **E11000 on `UpsertSubscription`** (concurrent insert race with room-worker)
  is treated as success, not poison — the document already exists.
- Graceful shutdown via `shutdown.Wait(25s)`, CLAUDE.md worker order:
  `iter.Stop()` → `wg.Wait()` (timeout) → `nc.Drain()` → tracer shutdown →
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
  zstd batch → assert subscription/count) under `internal/consumer` or
  `internal/service` using `testutil.NATS(t)`. Count-reconciliation and
  redelivery-idempotency (re-deliver the same batch, assert `userCount`
  unchanged) get explicit integration cases.
- `make generate` regenerates `internal/service/mocks/mock_repository.go` before
  testing. All tests run with `-race` (`make test` / `make test-integration`
  pass the flag).

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
   `Decompressor` interface), with both the 64KB compressed and 16MB
   decompressed caps enforced; orchestration stays in `service/process.go`.
5. Event DTOs in root `pkg/model/orgsync.go` (shared by multiple services;
   round-trip tested by `pkg/model/model_test.go`).
6. Sequential consume (ordering safety); `MAX_WORKERS` retained but unused by
   default.
7. `$setOnInsert` upserts via `mongoutil.UpsertModel` + `BulkWrite` (not the
   `$set` `BulkUpsert` shortcut).
8. org-sync-worker reuses existing collections/indexes; owns no schema.
9. Member counts use recompute-and-`$set` (`ReconcileUserCount`, mirroring
   room-worker), NEVER `$inc` — idempotent under redelivery; eventually
   consistent under concurrent room-worker writes (same residual count-then-set
   window room-worker already has; self-heals on next reconcile). Bots flagged
   via `model.IsBotAccount`.
10. Poison messages use `msg.Term()` (not Ack-drop) to preserve the source
    spec's reject semantics; `Nats-Msg-Id` honored for JetStream dedup.
11. Per-space rate limiting from the source spec is deliberately omitted (YAGNI
    at daily-batch volume); revisit only if batch sizes/cadence grow.
12. Consumer package is `internal/consumer` (not `internal/nats`, which would
    shadow the `nats.go` import).

## 14. Package-reuse audit

Full sweep of `pkg/` for reusable logic beyond the obvious infra packages.

**Reused:**
- `pkg/pipelines` — **partial.** `member.go` encodes the shared sect/dept org
  match shape (`sectId|deptId ∈ orgIDs`), reused for `AccountsViaOrg` and the
  org_disbanded enumeration to stay consistent with room-worker. Does NOT cover
  the worker's primary orgId→rooms query (`RoomsWithOrg` is net-new).
- `pkg/model.IsBotAccount` — the canonical bot/pseudo-account test, used to stamp
  `sub.u.isBot` at subscription creation so `ReconcileUserCount` counts non-bots
  into `userCount` exactly as room-worker does. (Avoids duplicating
  `pipelines`' unexported `botOrPseudoAccountRegex`.)
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

## 16. `main.go` wiring

Flat-root `main.go` (`package main`), mirroring `history-service/cmd/main.go` but
at the service root. Sequence (fail-fast on every error → `os.Exit(1)`):

1. `logctx.SetupDefault(os.Stdout)`; `cfg := config.Load()`; `logctx.Configure(cfg.DebugLog)`; validate positive numeric knobs (`MAX_BATCH_SIZE`, `HRSYNC_BATCH_MAX_COMPRESSED_SIZE`, `CONSUMER_*`).
2. `tracerShutdown := otelutil.InitTracer(ctx, "org-sync-worker")`.
3. `nc := natsutil.Connect(cfg.NATS.URL, cfg.NATS.CredsFile)`; `js := oteljetstream.New(nc)`.
4. `mongoClient := mongoutil.Connect(ctx, cfg.Mongo.URI, …)`; `db := mongoClient.Database(cfg.Mongo.DB)`.
5. Construct repos: `mongorepo.NewSubscriptionRepo(db)`, `NewRoomMemberRepo(db)`, `NewRoomRepo(db)`, `NewUserRepo(db)`.
6. `dec := codec.New(cfg.MaxCompressed)`; `svc := service.New(subs, members, rooms, users, cfg.SiteID)` (decoder injected into the `service.ProcessBatch` path).
7. `bootstrapStreams(ctx, js, cfg.SiteID, cfg.CentralSiteID, cfg.Bootstrap.Enabled)` (dev sets `Name`+`Subjects` only).
8. `cons := consumer.New(js, stream.HR(cfg.SiteID, cfg.CentralSiteID).Name, subject.HRConsumerFilters(cfg.CentralSiteID, cfg.SiteID), svc.ProcessBatch)`; `cons.Run(ctx)` (sequential `Consume`).
9. `healthStop := health.Serve(cfg.HealthAddr, 5*time.Second, natsutil.HealthCheck(nc))`.
10. `shutdown.Wait(ctx, 25*time.Second, …)` in CLAUDE.md worker order: `cons.Stop()` (iterator) → drain in-flight (wg, timeout) → `nc.Drain()` → `tracerShutdown` → `mongoutil.Disconnect` → `healthStop`.

## 17. Observability

- **Logging:** `log/slog` JSON (via `logctx`). Per-batch lifecycle lines —
  `processing batch` (`changes`, `request_id`), per-change errors (`type`,
  `orgId`, `error`, `request_id`), `batch complete` (`processed`, `failed`,
  `duration_ms`, `request_id`). Never log full batch bodies or accounts beyond
  the `account` identifier. Request-id via `natsutil.StampRequestID(ctx,
  msg.Headers(), msg.Subject())` on every message, propagated through `ctx`.
- **Tracing:** OTel spans on the consume path via `oteljetstream` (consumer) +
  `otelutil.InitTracer`.
- **Metrics:** the source spec's §Alerting (error-rate, processing-time, lag)
  is served for the MVP by structured-log aggregation; native Prometheus
  counters/histograms (batch latency, ack/nak/term counts, per-type change
  failures, reconcile latency) are a documented **post-launch** follow-up — not
  a launch blocker at daily-batch volume, but the hooks live on the
  `ProcessBatch` boundary when added.
