package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoDirectoryStore struct {
	employees *mongo.Collection
}

func newMongoDirectoryStore(db *mongo.Database) *mongoDirectoryStore {
	return &mongoDirectoryStore{employees: db.Collection("hr_employee")}
}

// EnsureIndexes enforces account uniqueness on hr_employee so a buggy HR cron
// write fails at insert time instead of publishing two home sites for one account.
func (s *mongoDirectoryStore) EnsureIndexes(ctx context.Context) error {
	if _, err := s.employees.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "account", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("ensure hr_employee (account) unique index: %w", err)
	}
	return nil
}

// ListEmployees returns the intersection of hr_employee and the users
// collection: every account in hr_employee that is also provisioned in users
// for the same site, with the canonical users._id projected as userId. The
// $lookup keyed on {account, siteId} performs the join in Mongo, so the
// in-memory cache already means "employee AND provisioned" and the portal does
// no per-request users query. users must live in the same database as
// hr_employee — $lookup cannot cross databases.
func (s *mongoDirectoryStore) ListEmployees(ctx context.Context) ([]employee, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "users"},
			{Key: "let", Value: bson.D{
				{Key: "acct", Value: "$account"},
				{Key: "site", Value: "$siteId"},
			}},
			{Key: "pipeline", Value: mongo.Pipeline{
				bson.D{{Key: "$match", Value: bson.D{{Key: "$expr", Value: bson.D{{Key: "$and", Value: bson.A{
					bson.D{{Key: "$eq", Value: bson.A{"$account", "$$acct"}}},
					bson.D{{Key: "$eq", Value: bson.A{"$siteId", "$$site"}}},
				}}}}}}},
				bson.D{{Key: "$project", Value: bson.D{{Key: "_id", Value: 1}}}},
				bson.D{{Key: "$limit", Value: 1}},
			}},
			{Key: "as", Value: "provisioned"},
		}}},
		bson.D{{Key: "$match", Value: bson.D{{Key: "provisioned.0", Value: bson.D{{Key: "$exists", Value: true}}}}}},
		bson.D{{Key: "$project", Value: bson.D{
			{Key: "_id", Value: 0},
			{Key: "account", Value: 1},
			{Key: "employeeId", Value: 1},
			{Key: "siteId", Value: 1},
			{Key: "userId", Value: bson.D{{Key: "$arrayElemAt", Value: bson.A{"$provisioned._id", 0}}}},
		}}},
	}
	cur, err := s.employees.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate hr_employee with users lookup: %w", err)
	}
	var emps []employee
	if err := cur.All(ctx, &emps); err != nil {
		return nil, fmt.Errorf("decode employees: %w", err)
	}
	return emps, nil
}
