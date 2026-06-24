# org-sync-worker — Design

**Date:** 2026-06-17 (updated 2026-06-24 to the Final integration contract)
**Status:** Approved (design locked)
**Service:** `org-sync-worker`
**Branch:** `claude/blissful-carson-kihdek`
**Authoritative contract:** the HR producer integration spec ("Final — implement
from this document only"). Its facts are inlined below so this design is
self-contained; the hand-off files are removed from the branch before merge.

## Summary

`org-sync-worker` is a site-local JetStream consumer that synchronizes
organization-membership changes from the HR event stream into local room
memberships. Each site runs its own instance and only mutates rooms it hosts
(`siteId == SITE_ID`). The worker consumes one zstd-compressed message whose
decompressed payload is a JSON **array** of `OrgMembershipBatch` objects, and
applies idempotent, **order-independent** MongoDB writes (create/remove
subscriptions + membership-source records, recompute member counts).

This document adapts the contract onto this monorepo: flat-root `main.go` (no
`cmd/`), the `internal/` layout of `history-service`, the root `go.mod`, and
reuse of the existing `pkg/` libraries. **Stream/subject use the worker's own
local `SITE_ID`: HR is published to `chat.hr.{siteID}.org.membership.changed` and
consumed via the single filter `chat.hr.{siteID}.>` (see §3). There is no
`CENTRAL_SITE_ID` and no `.to.{site}` routing.**

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
│   │   ├── process.go                         # ProcessMessage: decompress → []OrgMembershipBatch → dedup → per-change dispatch
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
`shutdown`, `errcode`, `model` (incl. `model.IsBotAccount`), `health`,
`otelutil`, `logctx`, `jobguard`, `testutil` (tests). See §14 for the full
package-reuse audit, including what was deliberately NOT reused.

## 2. Domain mapping (spec vocabulary → real model)

The HR contract uses generic vocabulary. The real collections and `pkg/model`
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

## 3. Stream / subject / consumer

Keyed entirely on the worker's own local `SITE_ID`. The HR producer publishes to
`chat.hr.{siteID}.org.membership.changed` (broadcast); the worker consumes via the
single wildcard filter `chat.hr.{siteID}.>`. There is **no `CENTRAL_SITE_ID`** and
**no `.to.{site}` routing** — both were artifacts of an earlier draft and are
dropped. Build subject strings and filters with the `pkg/subject` helpers, never
raw `fmt.Sprintf` (CLAUDE.md §3 NATS Subject Naming).

- **Stream:** `HR_{siteID}`. The authoritative stream config (name, subjects,
  federation sourcing) is owned by the HR producer/ops — org-sync-worker sources
  from the actual `pkg/stream` HR config rather than redefining it. The dev-only
  `bootstrap.go` sets only `Name` + `Subjects`; `Sources`/`SubjectTransforms`
  are ops/IaC-owned (INBOX convention).
- **Consumer filter — single subject:**
  ```
  chat.hr.{siteID}.>   // catches all HR events (incl. org.membership.changed)
  ```
  This is the ONLY filter required; `{siteID}` = the worker's own `SITE_ID`. It
  matches `chat.hr.{siteID}.org.membership.changed`.
- **Consumer:** durable `org-sync-worker-{site-id}`, `AckPolicy=Explicit`,
  `DeliverPolicy=All`, `MaxDeliver=5`, `AckWait=30m`, via
  `stream.ConsumerSettings` (env-tunable `CONSUMER_*`).
- **`pkg/subject` additions:**
  - `HROrgMembershipSubject(siteID) = chat.hr.{siteID}.org.membership.changed`
    (the published subject; used by the integration-test publisher)
  - `HRConsumerFilters(siteID) = []string{ "chat.hr."+siteID+".>" }`
- **`pkg/stream`:** use the existing/owned HR config if present; otherwise
  org-sync-worker adds `HR(siteID) Config` → `Name: "HR_"+siteID`,
  `Subjects: []string{"chat.hr."+siteID+".>"}`. Verify the exact name and subjects
  against the HR producer before implementing — do not diverge from it.

## 4. Event model (`pkg/model/event.go`)

Inbound DTOs produced by the external HR system. These are **shared NATS event
structs consumed by multiple services**, so they live in root `pkg/model` —
specifically appended to the existing `event.go` (the established home for
cross-service NATS event DTOs: `MessageEvent`, `InboxMemberEvent`, `OutboxEvent`,
`NotificationEvent`, …), not a service-local `internal/models` and not a new
one-off file. Per the CLAUDE.md rule, every NATS event struct lives in
`pkg/model`, carries a `Timestamp` field, and is round-trip tested by
`pkg/model/model_test.go`. JSON only.

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
`Account` is `omitempty` because the producer omits it for `org_disbanded` (the
wire example carries no `account` for that type).

**Wire format (one NATS message):**
- Header `Nats-Encoding: zstd` (always present).
- Payload: a JSON **array** of `OrgMembershipBatch`, zstd-compressed. After
  decompressing, the worker parses `[]OrgMembershipBatch` and flattens
  `batch.changes` across all batches into one change list.
- The producer caps each message at ~100 changes / 64KB compressed and may emit
  **multiple messages** per sync run.
- **Duplicates are expected** across runs/redeliveries. The worker dedups the
  flattened changes by the key `(type, orgId, account)` before applying, and all
  writes are idempotent, so re-applying a processed change is a no-op.

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
    dec     codec.Decompressor // injected; ProcessMessage decompresses with it
    subs    SubscriptionRepository
    members RoomMemberRepository
    rooms   RoomRepository
    users   UserRepository
    siteID  string
}

func New(dec codec.Decompressor, subs SubscriptionRepository, members RoomMemberRepository,
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
  `HasIndividualRecord` / `HasOrgRecordExcept` / `DeleteIndividualRecord` /
  `DeleteOrgRecord` are simple `room_members` queries/updates.
  `UpsertIndividualRecord` stamps `ID: idgen.GenerateUUIDv7()`, `Ts: now`,
  `Member: {type:"individual", id:user.ID, account}` and writes with
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
  (`userstore.go:26`) omits `sectId`/`deptId`, which the member_removed
  "covered by another org" check (`HasOrgRecordExcept`) requires. A hand-rolled
  `UserRepo` with the right projection is the correct call.

Pipeline shape for the one net-new aggregation (for test authors):

```
// RoomsWithOrg(orgID, siteID) — source: room_members
[ {$match: {"member.type":"org", "member.id":orgID}},
  {$lookup: {from:"rooms", localField:"rid", foreignField:"_id", as:"r"}},
  {$unwind:"$r"}, {$match: {"r.siteId":siteID}},
  {$replaceRoot:{newRoot:"$r"}},
  {$project: {_id:1, siteId:1, name:1, type:1}} ]
```

## 7. Business logic

All operations are scoped to the local site (`SITE_ID`), idempotent, and
**order-independent** (the contract gives no ordering guarantee within or across
batches). `ProcessMessage(ctx, raw)` decompresses, parses `[]OrgMembershipBatch`,
flattens + dedups changes by `(type, orgId, account)`, then calls
`processChange(ctx, change)` for each; `processChange` dispatches on `change.Type`.

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

(The contract explicitly calls for "recompute-and-`$set` (not `$inc`) for
redelivery idempotency" — this design matches it and reuses room-worker's
`ReconcileMemberCounts` discipline.)

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

**org_disbanded** (`disband.go`) — `account` is empty; do NOT use it. Per the
contract the **producer also emits a separate `member_removed` for each former
member in the same run**, so this handler does NOT enumerate/remove members
itself — it only drops the org's membership-source row:
1. `rooms := rooms.RoomsWithOrg(orgId, siteID)`.
2. For each room: `members.DeleteOrgRecord(room.ID, orgId)` then
   `rooms.ReconcileUserCount(room.ID)`.

The accompanying `member_removed` events do the per-member cleanup through the
normal `member_removed` path (with its individual / other-org keep-checks). This
is why there is no `AccountsViaOrg` enumeration and no org→member back-link
problem: the producer is the source of truth for who leaves.

**Validation (security):** a service-level
`validateChange(change)` rejects malformed input before any DB work — `type` ∈
{member_added, member_removed, org_disbanded}; `orgId` non-empty and matching
`^[A-Za-z0-9._-]{1,128}$`; `account` (required except for org_disbanded) matching
the same allowlist. A validation failure is a per-change skip (logged with
`type`/`orgId`), not a batch failure. The allowlist blocks operator/regex
injection into the Mongo filters built from these values.

**Ordering & dedup:** there is no ordering guarantee, so handlers never depend on
order. The flattened change list is deduped by `(type, orgId, account)` before
applying (collapses producer-emitted duplicates within a run); idempotent writes
+ recompute-and-`$set` counts make redelivery a no-op. (Sequential consume — §9 —
is a throughput/simplicity choice per the contract, not an ordering crutch.)

**Partial-message semantics:** `ProcessMessage` continues on per-change errors,
counting successes. Ack if ≥1 change succeeded; `NakWithDelay(30s)` if every
change failed (transient Mongo errors); `Term` if the message itself is
undecodable (see §9).

**First-run / oversized-message contract:** on first run the producer emits all
current org members as `member_added` (no `member_removed`/`org_disbanded`) — a
large burst, but split by the producer into multiple messages each ≤~100 changes
/ ≤64KB compressed. A single message whose total flattened change count exceeds
`MAX_BATCH_SIZE` (default 1000) is `Term`-ed as poison (§9), logged `batch too
large` (`count`/`max`); this is a producer-contract violation, not a worker bug.
Raise `MAX_BATCH_SIZE` only in lockstep with the producer's chunk size.

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
// decoding (the 64KB compressed cap from the contract), then
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
calls `service.ProcessMessage(ctx, msg.Data()) (processed int, err error)`.

- **Sequential consume** (`cons.Consume()`), per the contract's "sequential
  processing (not parallel worker pool)" directive. HR is daily/low-volume; the
  design is order-independent regardless, so this is a simplicity choice, not an
  ordering crutch. `MAX_WORKERS` is retained in config for forward-compat but
  unused on the default path.
- **Encoding check:** the consumer verifies the `Nats-Encoding: zstd` header
  (always present per the contract) before handing the body to `codec`; a
  missing/unknown encoding is poison (`Term`).
- **Term** (no retry, poison): missing/unknown `Nats-Encoding`, decompress
  failure, compressed input over `HRSYNC_BATCH_MAX_COMPRESSED_SIZE`, JSON parse
  failure (payload not a valid `[]OrgMembershipBatch`), total change count >
  `MAX_BATCH_SIZE`. The service returns these as `errcode.Permanent`; the
  consumer calls `msg.Term()` (reject-not-processed; `oteljetstream` supports
  `Term`).
- **Nak / NakWithDelay(30s)** (retry): Mongo timeout/contention and other
  transient infra errors (`errcode.IsPermanent` == false), and the
  all-changes-failed message case.
- **E11000 on `UpsertSubscription`** (concurrent insert race with room-worker)
  is treated as success, not poison — the document already exists.
- Graceful shutdown via `shutdown.Wait(25s)`, CLAUDE.md worker order:
  `iter.Stop()` → `wg.Wait()` (timeout) → `nc.Drain()` → tracer shutdown →
  `mongoutil.Disconnect` → health stop.

## 10. Configuration (`internal/config`, caarlos0/env)

```
SITE_ID                          required   site identifier (also the HR subject token)
NATS_URL                         required
NATS_CREDS_FILE                  optional
MONGO_URI                        required
MONGO_DB                         default chat
MAX_WORKERS                      default 100   (retained; sequential consume is default)
HRSYNC_BATCH_MAX_COMPRESSED_SIZE default 65536 (64KB compressed cap)
MAX_BATCH_SIZE                   default 1000  (max total changes per message → Term if exceeded)
HEALTH_ADDR                      default :8080
CONSUMER_*                       stream.ConsumerSettings (ACK_WAIT, MAX_DELIVER, …)
BOOTSTRAP_STREAMS                default false (dev sets true)
```

Fail fast on missing required vars and non-positive numeric knobs (history
-service `checkConfig` pattern). `MONGO_DB` default `chat`.

## 11. Testing (TDD, 80%+ floor, 90%+ on service)

- **Unit** (mocked repos via `internal/service/mocks`): `membership_test.go`
  (member_added/removed: user-not-found, already-subscribed, bot account,
  covered-by-other-org keep), `disband_test.go` (org_disbanded drops only the org
  row + reconciles, does NOT remove members), `process_test.go` (decompress fake,
  `[]OrgMembershipBatch` parse, `(type,orgId,account)` dedup, empty/multi-batch
  message, per-change-error continue). Plus `config_test.go` and `codec_test.go`
  (valid, empty, oversized/bomb). The event DTO round-trip is covered by
  `pkg/model/model_test.go`.
- **Integration** (`//go:build integration`): `mongorepo/main_test.go` →
  `testutil.RunTests(m)`, `setup_test.go` → `testutil.MongoDB(t,
  "org_sync_test")`; split per collection: `subscription_integration_test.go`,
  `room_member_integration_test.go`, `room_integration_test.go`,
  `user_integration_test.go`. An end-to-end JetStream flow test (publish a
  zstd-compressed `[]OrgMembershipBatch` message → assert subscription/count)
  under `internal/consumer` or `internal/service` using `testutil.NATS(t)`.
  Count-reconciliation and redelivery-idempotency (re-deliver the same message,
  assert `userCount` unchanged) get explicit integration cases.
- `make generate` regenerates `internal/service/mocks/mock_repository.go` before
  testing. All tests run with `-race` (`make test` / `make test-integration`
  pass the flag).

## 12. Deployment

- **Dockerfile** (`deploy/Dockerfile`): multi-stage `golang:1.25.11-alpine`
  builder → `alpine:3.21` runtime, build context = repo root (copies `go.mod`,
  `go.sum`, `pkg/`, `org-sync-worker/`), `go build -o /org-sync-worker
  ./org-sync-worker`, non-root user.
- **docker-compose.yml**: NATS (`--jetstream --http_port 8222`), MongoDB,
  worker; env includes `BOOTSTRAP_STREAMS=true`, `SITE_ID=site-local`.
- **azure-pipelines.yml**: path-filtered to `org-sync-worker/` + `pkg/`;
  validate (vet/test/build) + Docker build on main.

## 13. Locked decisions

1. Flat-root `main.go` (no `cmd/`); `internal/` layout per history-service.
2. Subject/stream keyed on the worker's own `SITE_ID`: subject
   `chat.hr.{siteID}.org.membership.changed`, single consumer filter
   `chat.hr.{siteID}.>`. No `CENTRAL_SITE_ID`, no `.to.{site}` routing. Stream
   `HR_{siteID}` sourced from the HR producer's owned config, not redesigned.
3. Decompressed payload is `[]OrgMembershipBatch`; the worker flattens + dedups
   changes by `(type, orgId, account)` and processes them order-independently.
4. `org_disbanded` only drops the org membership-source row + reconciles; the
   producer emits a separate `member_removed` per former member (handled by the
   normal member_removed path). No `AccountsViaOrg` enumeration.
5. One `mongoutil.Collection[T]` per collection file; no composing repository
   struct; interfaces defined in `service.go` with compile-time checks.
6. zstd decompression isolated in `internal/codec` (`Decoder` struct +
   `Decompressor` interface), 64KB compressed + 16MB decompressed caps enforced;
   orchestration stays in `service/process.go`.
7. Event DTOs appended to root `pkg/model/event.go` (round-trip tested by
   `pkg/model/model_test.go`).
8. Sequential consume per the contract; `MAX_WORKERS` retained but unused by
   default.
9. `$setOnInsert` upserts via `mongoutil.UpsertModel` + `BulkWrite` (not the
   `$set` `BulkUpsert` shortcut); reuses existing collections/indexes, owns no
   schema.
10. Member counts use recompute-and-`$set` (`ReconcileUserCount`, mirroring
    room-worker), NEVER `$inc` — idempotent under redelivery; eventually
    consistent under concurrent room-worker writes (same residual count-then-set
    window room-worker already has; self-heals on next reconcile). Bots flagged
    via `model.IsBotAccount`.
11. Poison messages use `msg.Term()`; the `Nats-Encoding: zstd` header is
    verified before decompression.
12. `member_removed` keeps a user covered by an individual record OR by a
    different member-org (`HasOrgRecordExcept`), per the original spec's
    other-org check (user-confirmed 2026-06-24).
13. Per-space rate limiting is deliberately omitted (YAGNI at daily-batch
    volume). Consumer package is `internal/consumer` (not `internal/nats`, which
    would shadow the `nats.go` import).

## 14. Package-reuse audit

Full sweep of `pkg/` for reusable logic beyond the obvious infra packages.

**Reused:**
- `pkg/model.IsBotAccount` — the canonical bot/pseudo-account test, used to stamp
  `sub.u.isBot` at subscription creation so `ReconcileUserCount` counts non-bots
  into `userCount` exactly as room-worker does. (Avoids duplicating
  `pipelines`' unexported `botOrPseudoAccountRegex`.)
- Infra (already planned): `natsutil`, `mongoutil`, `stream`, `subject`,
  `idgen`, `shutdown`, `errcode`, `model`, `health`, `otelutil`, `logctx`,
  `jobguard`, `testutil`.

**Deliberately NOT reused:**
- `pkg/pipelines` — its sect/dept org-match shape was only needed for the
  `AccountsViaOrg` enumeration, which the Final contract removed (the producer
  emits per-member `member_removed` for disbands). The remaining queries
  (`RoomsWithOrg`, `HasOrgRecordExcept`) hit `room_members` directly, so
  `pipelines` is no longer pulled in.
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
6. `dec := codec.New(cfg.MaxCompressed)`; `svc := service.New(dec, subs, members, rooms, users, cfg.SiteID)` (decoder injected into the `service.ProcessMessage` path).
7. `bootstrapStreams(ctx, js, cfg.SiteID, cfg.Bootstrap.Enabled)` (dev sets `Name`+`Subjects` only).
8. `cons := consumer.New(js, stream.HR(cfg.SiteID).Name, subject.HRConsumerFilters(cfg.SiteID), svc.ProcessMessage)`; `cons.Run(ctx)` (sequential `Consume`).
9. `healthStop := health.Serve(cfg.HealthAddr, 5*time.Second, natsutil.HealthCheck(nc))`.
10. `shutdown.Wait(ctx, 25*time.Second, …)` in CLAUDE.md worker order: `cons.Stop()` (iterator) → drain in-flight (wg, timeout) → `nc.Drain()` → `tracerShutdown` → `mongoutil.Disconnect` → `healthStop`.

## 17. Observability

- **Logging:** `log/slog` JSON (via `logctx`). Per-message lifecycle lines —
  `processing message` (`changes`, `request_id`), per-change errors (`type`,
  `orgId`, `error`, `request_id`), `message complete` (`processed`, `failed`,
  `duration_ms`, `request_id`). Never log full message bodies or accounts beyond
  the `account` identifier. Request-id via `natsutil.StampRequestID(ctx,
  msg.Headers(), msg.Subject())` on every message, propagated through `ctx`.
- **Tracing:** OTel spans on the consume path via `oteljetstream` (consumer) +
  `otelutil.InitTracer`.
- **Metrics:** the contract's alerting expectations (error-rate, processing-time,
  lag) are served for the MVP by structured-log aggregation; native Prometheus
  counters/histograms (batch latency, ack/nak/term counts, per-type change
  failures, reconcile latency) are a documented **post-launch** follow-up — not
  a launch blocker at daily-batch volume, but the hooks live on the
  `ProcessMessage` boundary when added.
