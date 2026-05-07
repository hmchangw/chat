package mongoutil

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// BulkResult mirrors the relevant fields of mongo.BulkWriteResult in a typed
// shape returned by Collection[T].BulkWrite / BulkUpsert / BulkUpsertByID.
//
// Empty-input contract: BulkWrite / BulkUpsert / BulkUpsertByID all return
// (nil, nil) when called with an empty slice -- callers MUST nil-check the
// result before reading fields. InsertMany is the exception: it returns
// (0, nil) on empty input, not a nil result. The asymmetry exists because
// BulkResult is a struct (nil-able pointer) while InsertMany returns a
// scalar count (no natural nil sentinel beyond zero).
//
// Field semantics (matching the driver):
//   - Matched: existing docs matched by the filter. Per the wire protocol,
//     a successful upsert that creates a new doc reports MatchedCount=0
//     (since the filter found no match) -- so Matched already excludes
//     upserted-new docs by construction. The driver also explicitly
//     subtracts UpsertedCount from MatchedCount in its bulk-write merge
//     path, which preserves the same invariant for batches.
//   - Modified: docs whose contents actually changed. Always Modified <= Matched.
//   - Upserted: new docs inserted via upsert (the matched=0 path).
//   - Inserted: pure inserts via InsertOneModel (rare; only set by BulkWrite
//     callers that include InsertOneModels).
//   - Deleted: docs deleted.
//   - UpsertedIDs: ordinal index -> _id of newly inserted docs (driver
//     populates from BulkWriteResult.UpsertedIDs); useful when callers need
//     server-assigned IDs. Keys are the original ordinal indices in the
//     input slice; under unordered execution with partial failures the keys
//     may be NON-CONTIGUOUS (e.g., for a 3-model batch where index 1 failed,
//     UpsertedIDs has keys {0, 2} only). Missing keys correspond to ops
//     that did not perform an insert: matched-existing, failed, or non-upsert.
//   - Acknowledged: false only when w:0 write concerns are in use; in that
//     case all counts are non-deterministic.
type BulkResult struct {
	Matched      int64
	Modified     int64
	Upserted     int64
	Inserted     int64
	Deleted      int64
	UpsertedIDs  map[int64]any
	Acknowledged bool
}

// UpsertModel constructs an UpdateOne write model with Upsert=true. Pure
// stateless constructor; safe to call repeatedly. Filter is typically
// bson.M{"_id": id}; update is typically bson.M{"$set": item} or
// bson.M{"$set": ..., "$setOnInsert": ...}.
func UpsertModel(filter, update any) mongo.WriteModel {
	return mongo.NewUpdateOneModel().
		SetFilter(filter).
		SetUpdate(update).
		SetUpsert(true)
}

// DeleteModel constructs a DeleteOne write model. Pure stateless constructor.
func DeleteModel(filter any) mongo.WriteModel {
	return mongo.NewDeleteOneModel().SetFilter(filter)
}

// fromDriverResult converts mongo.BulkWriteResult into the wrapper's
// BulkResult shape. Returns nil when r is nil so callers don't have to
// nil-check before mapping.
func fromDriverResult(r *mongo.BulkWriteResult) *BulkResult {
	if r == nil {
		return nil
	}
	return &BulkResult{
		Matched:      r.MatchedCount,
		Modified:     r.ModifiedCount,
		Upserted:     r.UpsertedCount,
		Inserted:     r.InsertedCount,
		Deleted:      r.DeletedCount,
		UpsertedIDs:  r.UpsertedIDs,
		Acknowledged: r.Acknowledged,
	}
}

// bsonSetWithoutID marshals item to a BSON map and drops the "_id" field.
// MongoDB rejects updates that would modify the immutable _id on existing
// documents; with pure $set + upsert, the marshaled $set MUST exclude
// _id or every match-and-update path errors. The _id is set on insert
// from the upsert filter -- see UpsertModel callers.
//
// Two marshal passes (Marshal then Unmarshal into bson.M) is acceptable
// at ~100 records per call. For higher-throughput callers a future
// BulkUpsertRaw taking pre-built bson.D payloads is the optimization
// path (see spec out-of-scope).
func bsonSetWithoutID(item any) (bson.M, error) {
	raw, err := bson.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("bson marshal: %w", err)
	}
	var m bson.M
	if err := bson.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("bson unmarshal: %w", err)
	}
	delete(m, "_id")
	return m, nil
}
