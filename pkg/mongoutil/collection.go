package mongoutil

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collection is a type-safe wrapper around *mongo.Collection that normalises
// ErrNoDocuments and wraps errors consistently. *Collection[T] is goroutine-safe
// (it wraps *mongo.Collection, which is goroutine-safe per the driver docs).
type Collection[T any] struct {
	col  *mongo.Collection
	name string
}

func NewCollection[T any](col *mongo.Collection) *Collection[T] {
	return &Collection[T]{col: col, name: col.Name()}
}

// FindOne returns the first matching document, or (nil, nil) when none match.
func (c *Collection[T]) FindOne(ctx context.Context, filter any, opts ...QueryOption) (*T, error) {
	var result T
	err := c.col.FindOne(ctx, filter, apply(opts).findOneOpts()).Decode(&result)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding %s: %w", c.name, err)
	}
	return &result, nil
}

func (c *Collection[T]) FindByID(ctx context.Context, id string, opts ...QueryOption) (*T, error) {
	return c.FindOne(ctx, bson.M{"_id": id}, opts...)
}

// FindMany returns all matching documents; returns empty (not nil) when none match.
func (c *Collection[T]) FindMany(ctx context.Context, filter any, opts ...QueryOption) ([]T, error) {
	cursor, err := c.col.Find(ctx, filter, apply(opts).findOpts())
	if err != nil {
		return nil, fmt.Errorf("querying %s: %w", c.name, err)
	}
	var results []T
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decoding %s results: %w", c.name, err)
	}
	if results == nil {
		results = []T{}
	}
	return results, nil
}

// Raw returns the underlying *mongo.Collection for escape-hatch scenarios.
func (c *Collection[T]) Raw() *mongo.Collection { return c.col }

// Aggregate runs the pipeline; no QueryOption — the pipeline encodes all query logic.
func (c *Collection[T]) Aggregate(ctx context.Context, pipeline bson.A) ([]T, error) {
	cursor, err := c.col.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregating %s: %w", c.name, err)
	}
	var results []T
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decoding %s aggregate: %w", c.name, err)
	}
	if results == nil {
		results = []T{}
	}
	return results, nil
}

// AggregatePaged appends a $facet: skip+limit data branch + count branch → OffsetPage[T].
//
// Caveat: $facet emits a single BSON document containing both branches.
// Mongo's 16 MB document limit applies to that output. For typical pages
// (Limit ≤ 100, the cap NewOffsetPageRequest enforces) this is non-issue;
// callers passing OffsetPageRequest directly with large Limit + large
// documents must keep the product under 16 MB.
func (c *Collection[T]) AggregatePaged(ctx context.Context, pipeline bson.A, req OffsetPageRequest) (OffsetPage[T], error) {
	facet := bson.D{{Key: "$facet", Value: bson.M{
		"data": bson.A{
			bson.D{{Key: "$skip", Value: req.Offset}},
			bson.D{{Key: "$limit", Value: req.Limit}},
		},
		"total": bson.A{
			bson.D{{Key: "$count", Value: "count"}},
		},
	}}}
	full := make(bson.A, 0, len(pipeline)+1)
	full = append(full, pipeline...)
	full = append(full, facet)

	cursor, err := c.col.Aggregate(ctx, full)
	if err != nil {
		return OffsetPage[T]{}, fmt.Errorf("aggregating %s: %w", c.name, err)
	}
	var wrapper []facetResult[T]
	if err := cursor.All(ctx, &wrapper); err != nil {
		return OffsetPage[T]{}, fmt.Errorf("decoding %s facet: %w", c.name, err)
	}
	if len(wrapper) == 0 {
		return EmptyPage[T](), nil
	}
	data := wrapper[0].Data
	if data == nil {
		data = []T{}
	}
	var total int64
	if len(wrapper[0].Total) > 0 {
		total = wrapper[0].Total[0].Count
	}
	return OffsetPage[T]{Data: data, Total: total}, nil
}

// facetResult decodes the single document emitted by the $facet stage.
type facetResult[T any] struct {
	Data  []T           `bson:"data"`
	Total []countResult `bson:"total"`
}

type countResult struct {
	Count int64 `bson:"count"`
}

// BulkWrite executes a slice of write models as a single batched operation.
// The wrapper sets options.BulkWrite().SetOrdered(false) explicitly to
// override mongo-driver's default of ordered=true. Failed individual ops
// do not block the rest under unordered execution; partial success
// returns a non-nil *BulkResult alongside the error so callers can
// inspect WriteErrors via errors.As(err, &mongo.BulkWriteException{}).
//
// Empty input is a no-op: returns (nil, nil) without calling the driver.
// Without this short-circuit the driver returns wrapped mongo.ErrEmptySlice.
//
// Sessions/transactions: BulkWrite picks up a session-bearing context
// transparently (via mongo.NewSessionContext or sess.WithTransaction).
// For atomic-across-documents semantics, wrap callers in WithTransaction.
func (c *Collection[T]) BulkWrite(ctx context.Context, models []mongo.WriteModel) (*BulkResult, error) {
	if len(models) == 0 {
		return nil, nil
	}
	res, err := c.col.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	mapped := fromDriverResult(res)
	if err != nil {
		return mapped, fmt.Errorf("bulk write %s: %w", c.name, err)
	}
	return mapped, nil
}

// BulkUpsert is a typed convenience layer over BulkWrite for the canonical
// "upsert these items by filter" case. Each item is upserted with update
// document {"$set": item}, with the item's "_id" field stripped from the
// $set payload (see below).
//
// MERGE semantics, NOT REPLACE. $set updates the listed fields only:
//   - If a document matches the filter, $set merges the fields from item
//     onto the stored document. Fields in the stored document that are
//     absent from item are PRESERVED.
//   - If no document matches, a new document is inserted containing the
//     filter fields plus everything in item (minus _id, which the upsert
//     filter sets on insert).
//
// _id handling: MongoDB rejects updates that try to modify the immutable
// "_id". Because T typically has a `bson:"_id"` field (per CLAUDE.md
// codebase convention), the wrapper marshals each item to a BSON map and
// removes "_id" before assembling the $set payload. The "_id" is set on
// insert from the upsert filter -- so callers using a filter like
// bson.M{"_id": id} will see new docs created with that id intact, and
// existing docs updated without an _id-mutation error.
//
// Caveat: stripping is keyed on the literal BSON key "_id". The wrapper
// expects T to follow the project convention of tagging the primary-key
// field bson:"_id". A field named ID without an explicit bson tag marshals
// to the key "id" (default lowercase) and will NOT be stripped -- producing
// a $set: {id: ...} payload that creates a stray sibling field on the
// stored document.
//
// IMPORTANT: For BulkUpsert with a custom (non-_id) filter, the item's
// _id is NEVER written. On insert, the server assigns a fresh ObjectID
// (filter has no _id, $set was stripped). On update, the stored _id is
// preserved. Callers who need the item's _id honored on insert should
// either include it in the filter (bson.M{"_id": it.ID, ...}) or use
// BulkUpsertByID.
//
// Pitfalls of pure-$set upsert:
//   - createdAt-style fields are REWRITTEN on every upsert (they're in
//     $set). For "set on insert only" semantics, fall back to BulkWrite
//     with $setOnInsert + $set models.
//   - The filter field(s) MUST be indexed or each upsert triggers a
//     full collection scan. _id is always indexed; custom filter fields
//     require explicit indexes.
//
// BSON omitempty caveat: a struct field tagged `bson:"foo,omitempty"`
// whose Go zero value is empty WILL NOT be present in the marshaled
// $set payload, meaning the stored value is preserved (not cleared).
// Callers that need to clear fields must either drop omitempty, use
// pointer fields (where nil means "absent" and a non-nil pointer to
// the zero value means "set explicitly"), or fall back to BulkWrite
// with explicit models.
//
// Empty input is a no-op (returns (nil, nil)). Callers must nil-check
// the result before reading fields on the empty path.
func (c *Collection[T]) BulkUpsert(ctx context.Context, items []T, filter func(T) any) (*BulkResult, error) {
	if len(items) == 0 {
		return nil, nil
	}
	models := make([]mongo.WriteModel, 0, len(items))
	for _, it := range items {
		setDoc, err := bsonSetWithoutID(it)
		if err != nil {
			return nil, fmt.Errorf("bulk upsert %s marshal item: %w", c.name, err)
		}
		models = append(models, UpsertModel(filter(it), bson.M{"$set": setDoc}))
	}
	return c.BulkWrite(ctx, models)
}

// BulkUpsertByID is the most ergonomic layer — analogous to FindByID over
// FindOne. The id function extracts a string identifier; the filter
// bson.M{"_id": idFn(item)} is applied internally. Same MERGE semantics
// and omitempty caveat as BulkUpsert. For non-string IDs (e.g.
// bson.ObjectID) or composite keys, fall back to BulkUpsert with a
// custom filter.
//
// Performance: this is the cheapest possible bulk-upsert pattern in
// MongoDB. _id is ALWAYS indexed (the unique primary key index is
// auto-created), so each upsert is a single B-tree lookup -- never a
// collection scan. Prefer this over BulkUpsert with a custom filter
// whenever the workload is "upsert by id".
//
// Empty input is a no-op (returns (nil, nil)). Callers must nil-check
// the result before reading fields on the empty path.
func (c *Collection[T]) BulkUpsertByID(ctx context.Context, items []T, idFn func(T) string) (*BulkResult, error) {
	return c.BulkUpsert(ctx, items, func(item T) any {
		return bson.M{"_id": idFn(item)}
	})
}

// InsertMany inserts a slice of items in a single batched operation. The
// wrapper sets options.InsertMany().SetOrdered(false) explicitly: a
// duplicate-key error on one item does not abort the rest, and the
// returned error reports all collisions.
//
// Returns the count of successfully inserted documents. On partial
// failure under unordered execution (some items collided, others
// succeeded), the returned count reflects the successes and the error
// carries the per-item write errors. The count is computed as
// len(*InsertManyResult.InsertedIDs); the InsertedIDs slice itself is
// dropped because this codebase exclusively uses caller-assigned
// application IDs (UUIDv7 hex / base62 -- see pkg/idgen).
//
// Acknowledged-write assumption: the count is reliable under acknowledged
// write concerns (the codebase default). Under w:0 the driver may return
// a nil result alongside a non-nil error even when writes succeeded; in
// that case the count returned by this wrapper will be 0 regardless of
// actual server-side success. The codebase does not currently use w:0
// for any collection.
//
// Faster than BulkUpsert when every item is known to be new -- Mongo
// skips the upsert filter lookup.
//
// Detect duplicate-key collisions with mongo.IsDuplicateKeyError(err) --
// the canonical helper used elsewhere in this codebase. Note: there is
// NO mongo.ErrDuplicateKey sentinel in mongo-driver/v2.
//
// Empty-input contract differs from the bulk methods: InsertMany returns
// (0, nil) on empty input rather than (nil, nil) -- callers can read
// the count unconditionally without a result-pointer nil-check.
func (c *Collection[T]) InsertMany(ctx context.Context, items []T) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	docs := make([]any, 0, len(items))
	for _, it := range items {
		docs = append(docs, it)
	}
	res, err := c.col.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	// On partial failure under unordered, the driver returns BOTH a non-nil
	// result (with InsertedIDs for the successes) AND a BulkWriteException.
	// Preserve the success count for the caller.
	var inserted int64
	if res != nil {
		inserted = int64(len(res.InsertedIDs))
	}
	if err != nil {
		return inserted, fmt.Errorf("insert many %s: %w", c.name, err)
	}
	return inserted, nil
}
