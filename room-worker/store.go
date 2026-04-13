package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . SubscriptionStore

type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	GetUser(ctx context.Context, account string) (*model.User, error)
	AddRole(ctx context.Context, account, roomID string, role model.Role) error
	RemoveRole(ctx context.Context, account, roomID string, role model.Role) error
}
