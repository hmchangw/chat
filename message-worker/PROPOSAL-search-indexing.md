# Proposal: Search Indexing via MESSAGES_CANONICAL

## Problem

The system needs full-text search over chat messages (ES or OpenSearch). The question is: who publishes to the search pipeline?

## Options Considered

### Option A: message-worker publishes to a SEARCH_INDEX stream (rejected)

```
MESSAGES_CANONICAL
    ├──► message-worker ──► Cassandra
    │         └──► SEARCH_INDEX ──► search-indexer ──► ES/OpenSearch
    ├──► broadcast-worker
    └──► notification-worker
```

**Problems:**
- **Unnecessary coupling** — message-worker becomes a bottleneck for search. If message-worker is down or slow, search indexing stops even though the data is already available on the canonical stream.
- **Breaks the fan-out pattern** — every other downstream consumer (broadcast, notification) reads directly from MESSAGES_CANONICAL with its own durable consumer. Search would be the odd one out, depending on an intermediary.
- **No enrichment justification** — message-worker doesn't add or transform data before publishing. It would just forward what it received.
- **Larger blast radius** — a bug in message-worker's publish logic could break search without any relation to persistence.

### Option B: search-indexer consumes MESSAGES_CANONICAL directly (chosen)

```
MESSAGES_CANONICAL_{siteID}
    ├──► message-worker       (durable: "message-worker")       ──► Cassandra
    ├──► broadcast-worker     (durable: "broadcast-worker")     ──► room delivery
    ├──► notification-worker  (durable: "notification-worker")  ──► push notifications
    └──► search-indexer       (durable: "search-indexer")       ──► ES / OpenSearch
```

**Why this is correct:**
- **Consistent pattern** — same architecture as every other consumer. No special plumbing.
- **Independent lifecycle** — search-indexer can be deployed, scaled, restarted, or fall behind without affecting persistence, delivery, or notifications.
- **Independent backpressure** — if ES is slow, only search-indexer's consumer lag grows. Other workers are unaffected.
- **Simpler message-worker** — stays focused on one job: Cassandra persistence.
- **No new streams needed** — MESSAGES_CANONICAL already carries the full `MessageEvent`. No `SEARCH_INDEX` stream to define, monitor, or retain.

## Search-Indexer Service Design (Future)

The search-indexer would be a new service following the standard worker pattern:

### Consumer Setup
```go
// Consumes from MESSAGES_CANONICAL with its own durable consumer
cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
    Durable:   "search-indexer",
    AckPolicy: jetstream.AckExplicitPolicy,
})
```

### Store Interface
```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store

// Store defines the indexing operations for the search service.
type Store interface {
    IndexMessage(ctx context.Context, msg model.Message) error
    DeleteMessage(ctx context.Context, roomID string, messageID string) error
}
```

### Index Mapping (ES/OpenSearch)
The index schema maps the fields that are searchable. This is the search-indexer's concern — it selects which fields from `model.Message` to index:

```json
{
  "mappings": {
    "properties": {
      "messageId":  { "type": "keyword" },
      "roomId":     { "type": "keyword" },
      "content":    { "type": "text", "analyzer": "standard" },
      "senderId":   { "type": "keyword" },
      "senderName": { "type": "text" },
      "mentions":   { "type": "keyword" },
      "createdAt":  { "type": "date" },
      "editedAt":   { "type": "date" },
      "deleted":    { "type": "boolean" },
      "siteId":     { "type": "keyword" },
      "sysMsgType": { "type": "keyword" }
    }
  }
}
```

### Subject Handling
As MESSAGES_CANONICAL adds subject tokens beyond `created` (e.g., `edited`, `deleted`), the search-indexer routes by subject:

| Subject token | Action |
|---|---|
| `*.created` | Index full document |
| `*.edited` | Partial update (content, editedAt, updatedAt) |
| `*.deleted` | Set `deleted: true` or remove from index |

### File Layout
```
search-indexer/
    main.go
    handler.go
    store.go
    store_es.go              # or store_opensearch.go
    handler_test.go
    integration_test.go
    deploy/
        Dockerfile
        docker-compose.yml
        azure-pipelines.yml
```

## Impact on message-worker

**None.** Message-worker stays exactly as scoped: consume from MESSAGES_CANONICAL, persist to Cassandra. No publish responsibility, no search concern.

## Decision

Search indexing is a **peer consumer** of MESSAGES_CANONICAL, not a downstream of message-worker. The search-indexer service will be implemented separately when the search solution (ES/OpenSearch) is chosen.
