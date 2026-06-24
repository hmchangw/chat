package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoAccountStore struct {
	col *mongo.Collection
}

func newMongoAccountStore(col *mongo.Collection) *mongoAccountStore {
	return &mongoAccountStore{col: col}
}

// ListSiteAccounts returns the account of every user homed at siteID.
func (s *mongoAccountStore) ListSiteAccounts(ctx context.Context, siteID string) ([]string, error) {
	cursor, err := s.col.Find(ctx,
		bson.M{"siteId": siteID},
		options.Find().SetProjection(bson.M{"_id": 0, "account": 1}),
	)
	if err != nil {
		return nil, fmt.Errorf("list site accounts: %w", err)
	}
	defer cursor.Close(ctx)
	var rows []struct {
		Account string `bson:"account"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode site accounts: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Account != "" {
			out = append(out, r.Account)
		}
	}
	return out, nil
}
