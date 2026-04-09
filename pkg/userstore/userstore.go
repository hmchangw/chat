package userstore

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

// ErrUserNotFound is returned by FindUserByID when no user matches the given ID.
var ErrUserNotFound = errors.New("user not found")

type mongoStore struct {
	col *mongo.Collection
}

// NewMongoStore returns a new MongoDB-backed user store using col as the users collection.
func NewMongoStore(col *mongo.Collection) *mongoStore {
	return &mongoStore{col: col}
}

// FindUserByID returns the user with the given ID.
// Returns ErrUserNotFound (wrapped) if no document matches.
func (s *mongoStore) FindUserByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	if err := s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&u); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("find user %s: %w", id, ErrUserNotFound)
		}
		return nil, fmt.Errorf("find user %s: %w", id, err)
	}
	return &u, nil
}

// FindUsersByAccounts returns all users whose account field is in accounts.
func (s *mongoStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	cursor, err := s.col.Find(ctx, bson.M{"account": bson.M{"$in": accounts}})
	if err != nil {
		return nil, fmt.Errorf("find users by accounts: %w", err)
	}
	defer cursor.Close(ctx)
	var users []model.User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return users, nil
}
