package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store
//go:generate mockgen -destination=mock_keystore_test.go -package=main . RoomKeyProvider

// Store defines data access operations for the broadcast worker.
type Store interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
	UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error
	SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error
	FindEmployeesByAccountNames(ctx context.Context, accountNames []string) ([]model.Employee, error)
}
