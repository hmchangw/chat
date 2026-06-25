# Org Membership Sync — External Worker Alignment

## Event Signature (published by hr-syncer)

### OrgMembershipChange

| Field | Type | JSON key | Description |
|-------|------|----------|-------------|
| `type` | `string` | `"type"` | `"member_added"` or `"member_removed"` |
| `roomId` | `string` | `"roomId"` | Room the membership change applies to (always populated) |
| `orgId` | `string` | `"orgId"` | Organization identifier |
| `account` | `string` | `"account"` | User account |
| `timestamp` | `int64` | `"timestamp"` | Unix milliseconds |

### OrgMembershipBatch

| Field | Type | JSON key | Description |
|-------|------|----------|-------------|
| `timestamp` | `int64` | `"timestamp"` | Unix milliseconds |
| `changes` | `OrgMembershipChange[]` | `"changes"` | Array of change events |

### Wire format

- Single `OrgMembershipBatch` JSON object per publish
- Compressed with zstd
- Header: `Nats-Encoding: zstd`

### Subject & Stream

- Subject: `chat.hr.{centralSiteID}.org.membership.changed`
- Stream: `HR_{centralSiteID}` (create if not exists)
- Consumer name: `org-sync-worker` (durable)

## External Worker Scope

The worker receives events and updates `room_members` org rows idempotently.

**DO:**
- Update org rows in `room_members` collection
- Only manage org rows (one row per room-org with `member.type="org"`, `member.id=orgID`)

**DO NOT:**
- Query users collection
- Manage subscriptions
- Track state locally (no tracking table)

## Room Members Data Model

```
room_members collection:
  {
    "_id": ...,
    "rid": "<roomID>",          // Room ID
    "ts": <timestamp>,
    "member": {                 // Embedded document
      "type": "org" | "individual",
      "id": "<orgID or userID>",
      "account": "<account>"    // present for "individual" type
    }
  }
```

Unique index: `{ "rid": 1, "member.type": 1, "member.id": 1 }`

## Apply Logic

### member_added

```
filter: bson.M{"rid": roomId, "member.type": "org", "member.id": orgId}
action: InsertOne (unique index makes duplicates return ErrDuplicateKey → skip)
```

### member_removed

```
filter: bson.M{"rid": roomId, "member.type": "org", "member.id": orgId}
action: DeleteOne (returns nil on no match → safe)
```

Note: org-membership is ONE row per (room, org), not per user. `member_removed` removes ALL users from 
that org's coverage in that room. If both `member_removed` and `member_added` fire in the same cycle for the same (room, org), the org row is briefly removed then re-added — safe because the worker is idempotent.

## JetStream Client

```go
js, err := oteljetstream.New(nc)
```

## Consumer Config

```go
hrStream := stream.HRJOS(centralSiteID)
cons, err := js.CreateOrUpdateConsumer(ctx, hrStream.Name, oteljetstream.ConsumerConfig{
    Durable:        "org-sync-worker",
    FilterSubjects: stream.HRJOSConsumerFilters(centralSiteID, siteID),
    AckPolicy:      oteljetstream.AckExplicitPolicy,
})
```

Filter subjects are defined in `pkg/stream/stream.go` — `HRJOSConsumerFilters()` returns the correct list of filter subjects for the consumer.

## Import Paths

```go
"github.com/nats-io/nats.go/jetstream"
"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
"github.com/hmchangw/chat/pkg/mongoutil"
"github.com/hmchangw/chat/pkg/natsutil"
"github.com/hmchangw/chat/pkg/shutdown"
"github.com/hmchangw/chat/pkg/stream"
```

## Config Parsing

```go
cfg, err := env.ParseAs[config]()
```

## Shutdown Pattern

```go
shutdown.Wait(ctx, 25*time.Second,
    func(context.Context) error {
        // Stop consumer iterators (unblocks Next/Consume calls)
        svc.Stop()
        return nil
    },
    func(ctx context.Context) error {
        // Wait for in-flight workers to drain
        return svc.Wait(ctx)
    },
    func(ctx context.Context) error {
        nc.Drain()
        return nil
    },
    func(ctx context.Context) error {
        mongoutil.Disconnect(ctx, mongoClient)
        return nil
    },
)
```

## MongoDB v2 API

Use `options.*()` helpers, NOT struct literals:

```go
// FindOne — returns (nil, nil) on no match (project convention for existence checks)
doc, err := collection.FindOne(ctx, filter)

// InsertOne
_, err := collection.InsertOne(ctx, document)

// UpdateOne
_, err := collection.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))

// DeleteOne
_, err := collection.DeleteOne(ctx, filter)
```

## Acknowledgment

Decompression or unmarshal failures → `natsutil.Term(msg, reason)` (permanent, no retry).
Database errors → `natsutil.Nak(msg, reason)` (retriable, message re-queued).
Success → `natsutil.Ack(msg, reason)`.

The `msg` parameter implements `natsutil.Msg` interface (`Ack()`, `Nak()`, `Term()`). See `pkg/natsutil/ack.go`.

## Event Ordering

- Events are published sequentially in batches of ~100
- hr-syncer publishes before uploading snapshot, so first run and subsequent runs may have duplicates
- First run (no previous snapshot): all current members sent as `member_added`
