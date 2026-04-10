package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store,UserStore

// Store defines Cassandra persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
	SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
}

// UserStore defines MongoDB user lookup operations for the message worker.
type UserStore interface {
	FindUserByID(ctx context.Context, id string) (*model.User, error)
}
