# Organization Membership Sync Worker Specification

**Classification:** EXTERNAL - Open Source / Public  
**Date:** 2026-06-15  
**Status:** Draft  
**Service:** `org-sync-worker`  
**Protocol:** JetStream-based batch processing

## Abstract

This specification defines `org-sync-worker`, a lightweight service that synchronizes organization membership changes from an HR event stream to local collaboration space memberships.

**Key Innovation:** Using organization-level events to fan out to per-site space memberships, enabling centralized HR management of members while respecting data locality (spaces exist only on their origin site).

## Problem Statement

In a distributed collaboration system with federated sites:
- **User directories are global** - synchronized across all sites
- **Spaces are local** - each site hosts only its own spaces
- **Org membership changes** in the central HR system must propagate to all sites where spaces use those organizations

Example: Organization "Engineering" has 10 members. If Alice leaves the organization, she should be removed from all spaces on all sites where "Engineering" is a member.

## Solution Architecture

### Components

```
┌───────────────────���─────────────────────────────────────────────┐
│                    HR Event Producer (Central)                   │
│  - Generates daily diffs of org membership changes               │
│  - Publishes compressed batches to shared stream                 │
│  - Detects disbanded organizations                               │
└─────────────────────���───┬───────────────────────────────────────┘
                          │ JetStream (HR Events)
                          │ Subject: chat.hr.{central-site}.org.membership.changed
                                                    │ (zstd compressed, 100 changes/batch)
┌─────────────────────────▼──────────────────────────��────────────┐
│                    org-sync-worker (Per-Site)                   │
│                                                                  │
│  1. Consume batch from local site's filtered view          │
│  2. Decompress (zstd)                                           │
│  3. Parse org membership changes                                │
│  4. Query local spaces collection for affected spaces           │
│  5. Apply bulk updates:                                         │
│     - Create memberships for new members                        │
│     - Create membership records                                 │
│     - Remove memberships when appropriate                       │
│     - Update member counts                                      │
│  6. Ack processed messages                                      │
└──────────────────────────────���──────────────────────────────────┘
```

### Key Design Decisions

1. **Batch-based**: Processes 100 changes per batch (idempotent, recoverable)
2. **Local application**: Each site queries its own membership records
3. **Upsert semantics**: Safe to retry (memberships use `$setOnInsert`)
4. **Denormalized**: Updates member counts directly (no cascading reads)

## Data Model

### Input Events

#### OrgMembershipChange

Represents a single membership delta.

```json
{
  "type": "member_added",
  "orgId": "org-eng",
  "account": "alice",
  "timestamp": 1718400000000
}
```

**Fields:**
- `type` (string): Operation type
  - `"member_added"` - User added to organization
  - `"member_removed"` - User removed from organization  
  - `"org_disbanded"` - Organization permanently removed from HR
- `orgId` (string): Organization identifier
- `account` (string): User account identifier (omitted for `"org_disbanded"`)
- `timestamp` (int64): Unix millisecond timestamp when change was detected

#### OrgMembershipBatch

Container for multiple changes batched for efficiency.

```json
{
  "timestamp": 1718400000000,
  "changes": [
    {"type": "member_added", "orgId": "org-eng", "account": "alice", "timestamp": 1718400000000},
        {"type": "member_added", "orgId": "org-eng", "account": "bob", "timestamp": 1718400000000},
    {"type": "member_removed", "orgId": "org-ops", "account": "carol", "timestamp": 1718400000000},
    {"type": "org_disbanded", "orgId": "org-old", "timestamp": 1718400000000}
  ]
}
```

**Fields:**
- `timestamp` (int64): Batch generation timestamp
- `changes` (array): Array of OrgMembershipChange objects

### Processing Considerations

**First Run:**

 When no previous state exists, all current org members are published as `member_added` events

#### Handling "member_added" Events

When a user joins an organization:

```go
// For each space on this site that has the org as a member:
// Check if user already has membership
if hasMembership(user, space) {
    // Already member via another path (individual add or other org)
    continue
}

// Look up user metadata from users collection
user := getUserByAccount(account)

// Create membership (upsert - safe on retry)
upsertMembership(&Membership{
    SpaceID:    spaceID,
    UserID:     user.ID,
    JoinedAt:   now,
})

// Create membership record (tracks membership source)
upsertMemberRecord(&MemberRecord{
    SpaceID: spaceID,
    Member: MemberEntry{
        Type: "individual",
        ID:   user.ID,
    },
})

// Increment member count
incrementMemberCount(spaceID)
```
#### Handling "member_removed" Events

When a user leaves an organization:

```go
// For each space that has the org as a member:
// Check if user has individual membership (explicitly added)
if hasIndividualMembership(user, space) {
    // User was explicitly added - keep them
    continue
}

// Check if user still member via other orgs
if hasOtherOrgMembership(user, space, currentOrgID) {
    // Covered by another org - keep them
    continue
}

// Safe to remove
deleteMembership(user, space)
deleteMemberRecord(user, space)
decrementMemberCount(spaceID)
```

#### Handling "org_disbanded" Events

When an organization is permanently removed:

```go
// For each space that had this org:
// Get all users who were members via this org
membersViaOrg := getMembersViaOrg(spaceID, orgID)

removedCount := 0
for _, account := range membersViaOrg {
    // Check if user should stay (individual or other orgs)
    if shouldStayInSpace(account, spaceID, orgID) {
        continue
    }
    
    // Remove membership and member record
    deleteMembership(account, spaceID)
    deleteMemberRecord(account, spaceID)
      removedCount++
}

// Delete the org's member record itself
deleteOrgMemberRecord(spaceID, orgID)

// Update member count
decrementMemberCountBy(spaceID, removedCount)
```

### Data Storage

#### Required Collections

**memberships:**
```javascript
{
  "_id": "<generated>",
  "spaceId": "<space-id>",
  "user": {
    "id": "<user-id>",
    "account": "<account>"
  },
  "joinedAt": ISODate(...)
}
```

**member_records (membership source-of-truth):**
```javascript
{
  "_id": "<generated>",
  "spaceId": "<space-id>",
  "timestamp": ISODate(...),
  "member": {
    "type": "individual" | "org",
    "id": "<user-id or org-id>",
    "account": "<account>" // Only for individuals
  }
}
```

**users (lookup only):**
```javascript
{
  "_id": "<user-id>",
  "account": "<account>",
  "org1Id": "<org-identifier>",
  "org2Id": "<org-identifier>"
}
```

**spaces (count updates):**
```javascript
{
  "_id": "<space-id>",
  "siteId": "<site-id>",
  "memberCount": 42
}
```

script
// member_records: Query by org membership
{ "spaceId": 1, "member.type": 1, "member.id": 1 }  // Unique

// memberships: Prevent duplicates
{ "spaceId": 1, "user.account": 1 }  // Unique

// member_records: Query org members
{ "member.type":  1, "member.id": 1 }

// users: Look up by account
{ "account": 1 }  // Unique
```

## Communication Protocol

### JetStream Configuration

**Stream:** HR_{siteID} (e.g., HR_00302000)  
**Subjects:** 
- `"chat.hr.{central-site}.org.membership.changed"
` (org membership changes)

Replace `{central-site}` with your actual identifier (e.g., `hr`, `hcm`, or your HR system's site ID).

### Consumer Configuration

**Durable Name:** `org-sync-worker-{site-id}`  
**Filter Subjects:** Per-site filtering

Example:
```javascript
{
      "durable_name": "org-sync-worker-site-a",
  "filter_subjects": [
    "chat.hr.{central-site}.org.membership.changed"
  ],
  "ack_policy": "explicit",
  "max_deliver": 5,
  "ack_wait": "30m"
}
```

**Note:** Consumer uses **DeliverAll** policy to receive all pending batches on startup.

### Message Format

**Headers:**
- `Nats-Encoding: zstd` (indicates compression)
- `Nats-Msg-Id: <request-id>` (deduplication)
- `X-Request-ID: <uuid>` (tracing)

**Body:** zstd-compressed JSON of OrgMembershipBatch

### Compression

**Algorithm:** zstd (Zstandard)  
**Level:** Default (3)  
**Format:** Raw compressed bytes (no framing)  
**Compressed Size Limit:** 64KB default (HRSYNC_BATCH_MAX_COMPRESSED_SIZE)  
**Decompress:**

```go
// With size limit to prevent decompression bombs
const maxDecompressedSize = 16 * 1024 * 1024  // 16MB

func decompress(data []byte) ([]byte, error) {
    reader := bytes.NewReader(data)
    zstdReader, err := zstd.NewReader(reader)
    if err != nil {
        return nil, err
    }
    defer zstdReader.Close()
    
    limitReader := io.LimitReader(zstdReader, maxDecompressedSize+1)
    result, err := io.ReadAll(limitReader)
    if len(result) > maxDecompressedSize {
        return nil, errors.New("decompressed size exceeds limit")
    }
    return result, err
}
```

## Implementation Guidelines

### Worker Pool Pattern

```go
// Bounded concurrency to prevent resource exhaustion
maxWorkers := 100
semaphore := make(chan struct{}, maxWorkers)

for msg := range messages {
    semaphore <- struct{}{}  // Acquire
    
    go func() {
        defer func() { <-semaphore }()  // Release
        
        processMessage(msg)
    }()
}
```

### Batch Processing

```go
func processMessage(msg jetstream.Msg) error {
    // Decompress
    data, err := decompress(msg.Data())
    if err != nil {
        msg.Term()  // Bad data - don't retry
        return err
    }
        
    // Parse batch
    var batch OrgMembershipBatch
    if err := json.Unmarshal(data, &batch); err != nil {
        msg.Term()
        return err
    }    

    // Process each change
    processed := 0
    for _, change := range batch.Changes {
        if err := processChange(change); err != nil {
            log.Error("Change failed", "error", err)
            continue  // Continue with next change
        }
        processed++
    }
        
    // Ack only if at least some changes succeeded
    if processed > 0 {
        msg.Ack()
    } else {
        // All changes failed - retry with NAK
        msg.NakWithDelay(30 * time.Second)
    }
    
    return nil
}
```

### Bulk Operations for Performance

All MongoDB operations MUST use bulk patterns for efficiency:

```go
// UPSERT semantics - safe on retry
collection.UpdateOne(
    filter,  // Match key
    bson.M{"$setOnInsert": doc},  // Only insert if not exists
    options.UpdateOne().SetUpsert(true),
)

// Bulk insert for member records (if supported)
var models []mongo.WriteModel
for _, member := range members {
    models = append(models, mongo.NewUpdateOneModel().
        SetFilter(filter).
        SetUpdate(bson.M{"$setOnInsert": member}).
        SetUpsert(true))
}
collection.BulkWrite(ctx, models)
```

### Error Handling Strategy

**Critical Errors (Terminate):**
- Decompression failure
- JSON parse failure
- Invalid message format

**Retryable Errors (NAK):**
- MongoDB connection timeout
- Lock contention
- Temporary network issues

**Partial Processing:**
When batch processing, continue on individual change failures:
- Log error with context
- Continue to next change
- If ALL changes fail, NAK for retry
- If ANY changes succeed, ACK (don't retry)

### Idempotency Guarantee

All operations must be idempotent (safe to retry):

```javascript
// Membership upsert - prevents duplicates
db.memberships.updateOne(
    { "spaceId": "s1", "user.account": "alice" },
    { "$setOnInsert": { "joinedAt": new Date() } },
    { upsert: true }
)

// Member record upsert
db.member_records.updateOne(
    { "spaceId": "s1", "member.type": "individual", "member.id": "u1" },
    { "$setOnInsert": { "timestamp": new Date() } },
    { upsert: true }
)

// Count increment (safe to run multiple times)
db.spaces.updateOne(
    { "_id": "s1" },  { "$inc": { "memberCount": 1 } }
)
```

## Configuration

### Environment Variables

```bash
# Site identification
SITE_ID=site-a                       # Required: site identifier (e.g., site-a, site-b)

# NATS JetStream
NATS_URL=nats://nats:4222            # Required: NATS server URL
NATS_CREDS_FILE=/path/to/creds       # Optional: credentials file

# MongoDB
MONGO_URI=mongodb://localhost:27017  # Required: MongoDB connection
MONGO_DB=chat                        # Optional: database name (default: chat)

# Performance tuning
MAX_WORKERS=100                      # Optional: concurrent workers (default: 100)

# Stream configuration
CENTRAL_SITE_ID=hr-site              # Central HR stream site
```

### Consumer Filters

Generated from template:

```go
filters := []string{
    fmt.Sprintf("chat.hr.%s.>",      centralSiteID),        // Broadcast
    fmt.Sprintf("chat.hr.%s.to.%s.>", centralSiteID, siteID),  // Site-specific
}
```

Example outputs:
- site-a: `["chat.hr.hr-site.>", "chat.hr.hr-site.to.site-a.>"]`
- site-b: `["chat.hr.hr-site.>", "chat.hr.hr-site.to.site-b.>"]`

## Deployment

### Container Configuration

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o org-sync-worker ./

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/org-sync-worker /org-sync-worker
ENTRYPOINT ["/org-sync-worker"]
```

### Log Format

Structured JSON logging (one line per event):

```json
{"level":"INFO","msg":"Processing batch","changes":100,"request_id":"abc123","timestamp":"2026-06-15T10:30:00Z"}
{"level":"ERROR","msg":"Failed to process change","type":"member_added","orgId":"org-eng","account":"alice","error":"user not found","request_id":"abc123"}
{"level":"INFO","msg":"Batch complete","processed":98,"failed":2,"request_id":"abc123","duration_ms":1234}
```

### Alerting Rules

**Critical alerts:**
- Worker down for > 5 minutes
- Error rate > 10% over 5 minutes
- Lag > 1 hour (missing batches)

**Warning alerts:**
- Error rate > 1% over 15 minutes
- Batch processing time > 30 seconds

## Testing

## Unit Tests

```go
func TestProcessChange_MemberAdded(t *testing.T) {
    store := newMockStore()
    handler := New(store, "site-a")
    
    // Setup: Space with org "org-eng" as member
    store.spaces = []Space{{ID: "s1", Orgs: []string{"org-eng"}}}
    store.users = []User{{ID: "u1", Account: "alice"}}
        
    change := OrgMembershipChange{
        Type:    "member_added",
        OrgID:   "org-eng",
        Account: "alice",
    }
    
    err := handler.processChange(change)// Verify: Membership created
    assert.NoError(t, err)
    assert.Equal(t, 1, len(store.memberships))
    assert.Equal(t, "s1", store.memberships[0].SpaceID)
}
```

### Integration Tests

```go
//go:build integration
func TestIntegration_FullFlow(t *testing.T) {
    ctx := context.Background()
    
    // Setup test containers
    mongo := testutil.MongoDB(t, "org-sync-test")
    nats := testutil.NATS(t)
    
    // Seed data
    db.Collection("member_records").InsertOne(ctx, MemberRecord{     SpaceID: "s1",
        Member: MemberEntry{Type: "org", ID: "org-eng"},
    })
    db.Collection("users").InsertOne(ctx, User{
        ID:      "u1",
        Account: "alice",
    })
        
    // Publish test batch
    batch := OrgMembershipBatch{
        Changes: []OrgMembershipChange{
            {Type: "member_added", OrgID: "org-eng", Account: "alice"},
        },
    }
        publishBatch(t, nats, "chat.hr.{central-site}.org.membership.changed", batch)
    
    // Wait for processing
    time.Sleep(100 * time.Millisecond)
    
    // Verify
    count, _ := db.Collection("memberships").CountDocuments(ctx, bson.M{
        "spaceId": "s1",
        "user.account": "alice",
    })
    assert.Equal(t, int64(1), count)
}
```

## Security Considerations

### NATS Authentication

- Use JWT-based authentication where available
- Store credentials in Kubernetes Secrets
- Rotate credentials regularly

### MongoDB Access

- Use least-privilege database users
- Enable authentication and TLS
- Restrict network access to application pods

### Message Validation

```go
// Validate orgId format (prevent injection)
if !validOrgID(change.OrgID) {
    return fmt.Errorf("invalid orgId: %s", change.OrgID)
}

// Validate account format
if !validAccount(change.Account) {
    return fmt.Errorf("invalid account: %s", change.Account)
}
```

### Rate Limiting

Implement per-space rate limiting to prevent thundering herd:

```go
limiter := rate.NewLimiter(rate.Every(100*time.Millisecond), 10)

func processSpaceChanges(spaceID string, changes []Change) error {
    if err := limiter.Wait(ctx); err != nil {
        return err
    }
    // ... process changes
}
```

## Implementation Approach

### Phase 1: Copy & Adapt Core Components (Week 1)

**From hr-event-consumer reference implementation:**
- [ ] Copy `internal/service/batch.go` → adapt imports and interfaces
- [ ] Copy `internal/nats/consumer.go` → adapt to your NATS client
- [ ] Copy `internal/mongorepo/*` → adapt bulk upsert pattern

**Create new or adapt:**
- [ ] Implement `cmd/main.go` with bootstrap and shutdown
- [ ] Implement `internal/config/config.go` with env parsing
- [ ] Implement `internal/handler/handler.go` for org change processing
- [ ] Implement `internal/store/store.go` interface
- [ ] Implement `internal/store/mongo.go` MongoDB implementation
- [ ] Implement `internal/service/service.go` orchestration layer

### Phase 2: Business Logic (Week 2)

- [ ] Implement `handleMemberAdded` with space lookup and membership creation
- [ ] Implement `handleMemberRemoved` with individual/org membership checks
- [ ] Implement `handleOrgDisbanded` with bulk removal logic
- [ ] Add member count updates
- [ ] Add system notifications (optional)

### Phase 3: Infrastructure & Testing (Week 3)

- [ ] Add structured logging (slog)- [ ] Add graceful shutdown handling
- [ ] Write unit tests (80%+ coverage)
- [ ] Write integration tests with testcontainers
- [ ] Create Dockerfile

### Key Files to Create

```
org-sync-worker/
├── cmd/
│   └── main.go                    # Entry point
├── internal/
│   ├── config/
│   │   └── config.go              # Env configuration
│   ├── handler/
│   │   └── handler.go             # Change processing logic
│   ├─��� nats/
│   │   └── consumer.go            # COPY FROM REFERENCE
│   ├── service/
│   │   ├── batch.go               # COPY FROM REFERENCE
│   │   └── service.go             # Service orchestration
│   └─��� store/
│       ├── store.go               # Interface definitions
│       └── mongo.go               # MongoDB implementation
├── pkg/
│   └── model/
│       └── event.go               # OrgMembershipChange, OrgMembershipBatch
└── deploy/
    ├── Dockerfile
    └── kubernetes.yaml
```

## Implementation Checklist

## Summary

org-sync-worker is a **stateless, site-local service** that bridges the gap between:
- **Centralized HR events** (org membership changes)
- **Distributed space data** (local to each origin site)

**Key characteristics:**
- ✅ Consumes compressed batches from JetStream
- ✅ Applies idempotent bulk updates to local MongoDB
- ✅ Each site queries only its own spaces
- ✅ Safe to restart (resumes from last ACK)
- ✅ Horizontal scaling via worker pool

## Reusable Components & Code Dependencies

Since this is an external implementation, you need to either **copy code** from the internal reference implementation or **implement your own** versions. Here's what you need:

### Option 1: Copy from Reference Implementation (Recommended)

The reference implementation (hr-event-consumer) contains battle-tested code for decompression and batch processing. Copy these files and adapt them:

#### **File 1: `internal/service/batch.go`** (Copy & Adapt)

```go
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/hmchangw/chat/pkg/natsutil"  // UPDATE: Use your natsutil or implement your own
)

const (
	// maxDecompressedSize limits decompressed payload to 16MB to prevent DoS via decompression bombs.
	maxDecompressedSize = 16 * 1024 * 1024
	defaultMaxBatchSize = 1000
)

var maxBatchSize = defaultMaxBatchSize

// SetMaxBatchSize allows configuration of the batch size limit (called from main.go).
func SetMaxBatchSize(size int) {
	if size > 0 {
		maxBatchSize = size
	}
}

// decompressWithLimit decompresses zstd data with a size limit to prevent DoS.
// Uses a temporary limit reader to enforce the size limit during decompression.
func decompressWithLimit(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty compressed data")
	}

	// Use streaming decompression with a byte limit to prevent decompression bombs.
	// This creates a fresh reader that respects the limit, avoiding OOM attacks.
	reader := bytes.NewReader(data)
	zstdReader, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.IgnoreChecksum(true))
	if err != nil {
		return nil, fmt.Errorf("create zstd reader: %w", err)
	}
	defer zstdReader.Close()
    
	limitReader := io.LimitReader(zstdReader, maxDecompressedSize+1)
	decompressed, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, fmt.Errorf("decompress failed: %w", err)
	}


	if len(decompressed) > maxDecompressedSize {
		return nil, fmt.Errorf("decompressed size exceeds maximum %d bytes", maxDecompressedSize)
	}

	return decompressed, nil
}

// Repository handles bulk upserts of typed documents.
type Repository[T any] interface {
	BulkUpsert(ctx context.Context, items []T, filter func(T) any) error
}

// natsUtil abstracts the NATS message operations needed for batch processing.
type natsUtil interface {
	Ack()
	Nak()
	Term()
}

// BatchProcessor handles decompression, unmarshaling, and bulk upserts for a specific entity type.
type BatchProcessor[T any] struct {
	name   string
	repo   Repository[T]
	filter func(T) any
}

// NewBatchProcessor creates a BatchProcessor for the given entity type.
func NewBatchProcessor[T any](name string, repo Repository[T], filter func(T) any) *BatchProcessor[T] {
	return &BatchProcessor[T]{
		name:   name,
		repo:   repo,
		filter: filter,
	}
}

// Process decompresses zstd data, unmarshals JSON, and bulk upserts to MongoDB.
// Terminates for decompression/unmarshal failures, NAKs for DB errors, Acks on success.
func (p *BatchProcessor[T]) Process(ctx context.Context, data []byte, msg natsUtil) {
	start := time.Now()
	reqID := "" // Extract from context if available

	decompressed, err := decompressWithLimit(data)
	if err != nil {
		slog.Error("decompress failed", "type", p.name, "error", err, "request_id", reqID, "data_len", len(data))
		msg.Term()
		return
	}
	decompressMs := time.Since(start).Milliseconds()
    
	var parsed []T
	if err := json.Unmarshal(decompressed, &parsed); err != nil {
		slog.Error("unmarshal failed", "type", p.name, "error", err, "request_id", reqID, "decompressed_len", len(decompressed))
		msg.Term()
		return
	}

	if len(parsed) == 0 {
		msg.Ack()
		return
	}

	if len(parsed) > maxBatchSize {
		slog.Error("batch too large", "type", p.name, "count", len(parsed), "max", maxBatchSize, "request_id", reqID)
		msg.Term()
		return
	}

	if err := p.repo.BulkUpsert(ctx, parsed, p.filter); err != nil {
		slog.Error("bulk upsert failed", "type", p.name, "error", err, "count", len(parsed), "request_id", reqID)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			msg.Nak()
		} else {
			msg.Nak()
		}
		return
	}

	slog.Info("batch processed", "type", p.name, "count", len(parsed), "elapsed_ms", time.Since(start).Milliseconds(), "decompress_ms", decompressMs, "request_id", reqID)
	msg.Ack()
}
```

**Note:** You'll need to adapt `natsUtil` interface to your NATS client library's message type.

#### **File 2: `internal/nats/consumer.go`** (Copy & Adapt)

```go
package nats

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
	
	"github.com/nats-io/nats.go/jetstream"  // UPDATE: Use your NATS client
)

const maxProcessDuration = 30 * time.Second

// messagesIterator abstracts the pull iterator returned by Messages().
type messagesIterator interface {
	Next(opts ...jetstream.NextOpt) (context.Context, jetstream.Msg, error)
	Stop()
}

// consumerCreator abstracts JetStream consumer creation.
type consumerCreator interface {
	CreateOrUpdateConsumer(ctx context.Context, streamName string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error)
}

// Config holds settings for the consumer.
type Config struct {
	StreamName     string
	FilterSubjects []string
	MaxWorkers     int
}

// ConsumerSpec describes one durable consumer: its name, subject filters, and handler.
type ConsumerSpec struct {
	Name   string       // durable consumer name
	Topics []string     // filter subjects
	Handle BatchHandler // batch processor
}

// BatchHandler is a function that processes a batch of message data using the
// same ack/nak/term primitives as jetstream.Msg.
type BatchHandler func(context.Context, []byte, Msg)

// Msg abstracts the JetStream message operations.
type Msg interface {
	Ack()
	Nak()
	Term()
	Data() []byte
	Headers() jetstream.Header
	Subject() string
}

// Consumer manages the NATS JetStream pull-based consumption lifecycle.
type Consumer struct {
	streamClient   consumerCreator
	streamName     string
	filterSubjects []string
	maxWorkers     int

	mu       sync.Mutex
	iters    []messagesIterator
	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// NewConsumer creates a new Consumer backed by the given JetStream client.
func NewConsumer(js consumerCreator, cfg Config) *Consumer {
	return &Consumer{
		streamClient:   js,
		streamName:     cfg.StreamName,
		filterSubjects: cfg.FilterSubjects,
		maxWorkers:     cfg.MaxWorkers,
		stopCh:         make(chan struct{}),
	}
}

// Run creates durable consumers for each spec, starts their pull iterators,
// and launches the worker loops. It returns immediately — the loops run in
// background goroutines. Use Stop() and Wait() for graceful shutdown.func (c *Consumer) Run(ctx context.Context, specs []ConsumerSpec) error {
	type iterSpec struct {
		name   string
		iter   messagesIterator
		handle BatchHandler
	}
	var iterSpecs []iterSpec


	for _, spec := range specs {
		filterSubjects := getFilterSubjects(c.filterSubjects, spec.Topics)
		cfg := buildConsumerConfig(spec.Name, filterSubjects)
		cons, err := c.streamClient.CreateOrUpdateConsumer(ctx, c.streamName, cfg)
		if err != nil {
			for _, is := range iterSpecs {
				is.iter.Stop()
			}
			return fmt.Errorf("create %s consumer: %w", spec.Name, err)
		}

		iter, err := cons.Messages(jetstream.PullMaxMessages(2 * c.maxWorkers))
		if err != nil {
			for _, is := range iterSpecs {
				is.iter.Stop()
			}
			return fmt.Errorf("%s messages iterator: %w", spec.Name, err)
		}

		iterSpecs = append(iterSpecs, iterSpec{name: spec.Name, iter: iter, handle: spec.Handle})
	}

	c.mu.Lock()
	for _, is := range iterSpecs {
		c.iters = append(c.iters, is.iter)
	}
	c.mu.Unlock()

	for _, is := range iterSpecs {
		c.startWorkers(is.name, is.iter, is.handle)
	}

	return nil
}

func (c *Consumer) startWorkers(name string, iter messagesIterator, processBatch BatchHandler) {
	for w := 0; w < c.maxWorkers; w++ {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			for {
				select {
				case <-c.stopCh:
					return
				default:
				}

				msgCtx, msg, err := iter.Next()
				if err != nil {
					slog.Error("consumer loop exited unexpectedly", "consumer", name, "error", err)
					c.Stop()
					return
				}

				ctx, cancel := context.WithTimeout(msgCtx, maxProcessDuration)
				processBatch(ctx, msg.Data(), msg)
				cancel()
			}
		}()
	}
}
// Stop stops all consumer iterators and signals workers to exit.
func (c *Consumer) Stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, iter := range c.iters {
		iter.Stop()
	}
}
// Wait waits for all in-flight workers to drain.
func (c *Consumer) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker drain timed out: %w", ctx.Err())
	}
}


func buildConsumerConfig(durable string, filterSubjects []string) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:        durable,
		FilterSubjects: filterSubjects,
		// Set other defaults as needed
	}
}


func getFilterSubjects(consumerFilters, specTopics []string) []string {
	if len(specTopics) > 0 {
		return specTopics
	}
	if len(consumerFilters) > 0 {
		return consumerFilters
	}
	return []string{"*"}
}
```

**Note:** Update imports and types to match your NATS client library.


### Option 2: Implement Your Own (If No Access to Reference)

If you cannot copy from hr-event-consumer, implement these key components:

1. **Zstd Decompression with Size Limit:**
   - Use `github.com/klauspost/compress/zstd` library
   - Always use `io.LimitReader` with 16MB limit
   - Set decoder concurrency to 1
   - Use `IgnoreChecksum(true)` for performance

2. **Generic Batch Processor:**
   - Create a generic struct that can handle any type `T`
   - Takes a repository interface for persistence
   - Handles Ack/NAK/Term based on processing results

3. **Consumer with Worker Pool:**
   - Pull-based iterator with semaphore-bounded workers
   - Graceful shutdown with WaitGroup
   - Context timeout per message (30s default)

### Required Dependencies

```go
// go.mod
require (
	github.com/nats-io/nats.go v1.36.0
	github.com/klauspost/compress v1.17.0
	go.mongodb.org/mongo-driver/v2 v2.0.0
	github.com/caarlos0/env/v11 v11.0.0
	// ... your other deps
)
```

### Architecture Decision

**Recommended:** Copy the code from hr-event-consumer and adapt the interfaces to your specific:
- NATS client library
- MongoDB driver version
- Logging framework
- Configuration approach

The logic (decompression, batching, error handling) is proven and should be kept identical for safety.

## References

- NATS JetStream documentation: https://docs.nats.io/nats-concepts/jetstream
- zstd compression: https://github.com/facebook/zstd
- MongoDB bulk operations: https://docs.mongodb.com/manual/reference/method/db.collection.bulkWrite/
- Reference implementation: hr-event-consumer (internal repository)

