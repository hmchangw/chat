# Org Membership Sync — External Worker Alignment

## Event Signature (published by hr-syncer)

### OrgMembershipChange

| Field | Type | JSON key | Description |
|-------|------|----------|-------------|
| `type` | `string` | `"type"` | `"member_added"` or `"member_removed"` |
| `roomId` | `string` | `"roomId"` | Room the membership change applies to (always populated) |
| `orgId` | `string` | `"orgId"` | Organization identifier |
| `account` | `string` | `"account"` | User account (present on wire for every change; worker never reads this field for org-row operations) |
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

Note: org-membership is ONE row per (room, org), not per user. `member_removed` is emitted **only when an org disappears entirely** (org-gone case — the org is no longer present in today's employee data). Per-user departures do NOT emit `member_removed` because removing a single user's membership would incorrectly delete the entire org's coverage from the room. If both `member_removed` and `member_added` fire in the same cycle for the same (room, org), this can only occur when a previous org is removed and a new org with the same ID is created — the worker handles this idempotently via the unique index.

## JetStream Client

```go
js, err := oteljetstream.New(nc)
```

## Consumer Config

```go
hrStream := stream.HRJOS(centralSiteID)
cons, err := js.CreateOrUpdateConsumer(ctx, hrStream.Name, oteljetstream.ConsumerConfig{Durable:        "org-sync-worker",
    FilterSubjects: stream.HRJOSConsumerFilters(centralSiteID, siteID),
    AckPolicy:      oteljetstream.AckExplicitPolicy,
})
```
Filter subjects are defined in `pkg/stream/stream.go` — `HRJOSConsumerFilters()` returns both the full HR prefix and the JOS routed prefix for workers that consume all HR/JOS traffic.
> If the worker consumes **only** org membership events (not all HR/JOS traffic), use a subject-specific filter instead:
>
> ```go
> FilterSubjects: []string{
>     "chat.hr." + centralSiteID + ".org.membership.changed",
> }> ```

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
        // Stop consumer iterator (unblocks Next/Consume calls)
        iter.Stop()
        return nil
    },
    func(ctx context.Context) error {
        // Wait for in-flight goroutines to drain
        done := make(chan struct{})
        go func() { wg.Wait(); close(done) }()
        select {
        case <-done:
            return nil
        case <-ctx.Done():
            return fmt.Errorf("worker drain timed out: %w", ctx.Err())
        }
    },
    func(context.Context) error {
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
// FindOne — returns (*T, mongo.ErrNoDocuments) on no match; check with errors.Is
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
