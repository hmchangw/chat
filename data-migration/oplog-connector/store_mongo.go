package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// checkpointCollection is the collection (in CHECKPOINT_DB on the source RS)
// holding one checkpoint doc per (site, collection).
const checkpointCollection = "oplog_checkpoints"

// mongoCheckpointStore persists checkpoints to MongoDB.
type mongoCheckpointStore struct {
	col    *mongo.Collection
	siteID string
}

// NewMongoCheckpointStore returns a CheckpointStore backed by col. siteID
// scopes the checkpoint _id so multiple sites can share a collection.
func NewMongoCheckpointStore(col *mongo.Collection, siteID string) *mongoCheckpointStore {
	return &mongoCheckpointStore{col: col, siteID: siteID}
}

func checkpointID(siteID, collection string) string {
	return siteID + ":" + collection
}

func (s *mongoCheckpointStore) Load(ctx context.Context, collection string) (*Checkpoint, error) {
	var cp Checkpoint
	err := s.col.FindOne(ctx, bson.M{"_id": checkpointID(s.siteID, collection)}).Decode(&cp)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load checkpoint %q: %w", collection, err)
	}
	return &cp, nil
}

func (s *mongoCheckpointStore) Save(ctx context.Context, cp *Checkpoint) error {
	// Copy so we don't mutate the caller's struct (it sets _id from site+coll).
	doc := *cp
	doc.ID = checkpointID(doc.SiteID, doc.Collection)
	if _, err := s.col.ReplaceOne(ctx, bson.M{"_id": doc.ID}, doc, options.Replace().SetUpsert(true)); err != nil {
		return fmt.Errorf("save checkpoint %q: %w", doc.Collection, err)
	}
	return nil
}
