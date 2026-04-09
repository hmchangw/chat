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

// userQuerier abstracts the MongoDB collection calls used by mongoStore.
// Unexported so unit tests can inject a fake without a real MongoDB connection.
type userQuerier interface {
	findByID(ctx context.Context, id string) (*model.User, error)
	findByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
}

type mongoStore struct {
	q userQuerier
}

// mongoQuerier is the real implementation of userQuerier backed by *mongo.Collection.
type mongoQuerier struct {
	col *mongo.Collection
}

func (mq *mongoQuerier) findByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	if err := mq.col.FindOne(ctx, bson.M{"_id": id}).Decode(&u); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("find user %s: %w", id, ErrUserNotFound)
		}
		return nil, fmt.Errorf("find user %s: %w", id, err)
	}
	return &u, nil
}

func (mq *mongoQuerier) findByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	cursor, err := mq.col.Find(ctx, bson.M{"account": bson.M{"$in": accounts}})
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

// NewMongoStore returns a new MongoDB-backed user store using col as the users collection.
func NewMongoStore(col *mongo.Collection) *mongoStore {
	return &mongoStore{q: &mongoQuerier{col: col}}
}

// FindUserByID returns the user with the given ID.
// Returns ErrUserNotFound (wrapped) if no document matches.
func (s *mongoStore) FindUserByID(ctx context.Context, id string) (*model.User, error) {
	return s.q.findByID(ctx, id)
}

// FindUsersByAccounts returns all users whose account field is in accounts.
// Returns nil without querying when accounts is empty.
func (s *mongoStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	return s.q.findByAccounts(ctx, accounts)
}
