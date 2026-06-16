package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoDirectoryStore struct {
	employees *mongo.Collection
	users     *mongo.Collection
}

func newMongoDirectoryStore(db *mongo.Database) *mongoDirectoryStore {
	return &mongoDirectoryStore{
		employees: db.Collection("hr_employee"),
		users:     db.Collection("users"),
	}
}

// AccountProvisioned reports whether {account, siteId} exists in the users
// collection — the canonical user record. It reads no fields, only existence
// (projects _id), so it stays a cheap indexed point lookup per login.
func (s *mongoDirectoryStore) AccountProvisioned(ctx context.Context, account, siteID string) (bool, error) {
	err := s.users.FindOne(ctx,
		bson.M{"account": account, "siteId": siteID},
		options.FindOne().SetProjection(bson.M{"_id": 1}),
	).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query users for provisioning: %w", err)
	}
	return true, nil
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

func (s *mongoDirectoryStore) ListEmployees(ctx context.Context) ([]employee, error) {
	cur, err := s.employees.Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{
		"_id":        0,
		"account":    1,
		"employeeId": 1,
		"siteId":     1,
		"natsUrl":    1,
	}))
	if err != nil {
		return nil, fmt.Errorf("find employees: %w", err)
	}
	var emps []employee
	if err := cur.All(ctx, &emps); err != nil {
		return nil, fmt.Errorf("decode employees: %w", err)
	}
	return emps, nil
}
