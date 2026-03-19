package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . MessageStore

// MessageStore defines persistence operations for the message worker.
type MessageStore interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	SaveMessage(ctx context.Context, msg model.Message) error
	UpdateRoomLastMessage(ctx context.Context, roomID string, at time.Time) error
}
