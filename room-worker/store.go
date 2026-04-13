package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . SubscriptionStore

type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error
	DeleteSubscription(ctx context.Context, account, roomID string) error
	UpdateSubscriptionRole(ctx context.Context, account, roomID string, role model.Role) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string, count int) error
	DecrementUserCount(ctx context.Context, roomID string, count int) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	CreateRoomMember(ctx context.Context, member *model.RoomMember) error
	DeleteRoomMember(ctx context.Context, account, roomID string) error
	DeleteOrgRoomMember(ctx context.Context, orgID, roomID string) error
	GetUser(ctx context.Context, account string) (*model.User, error)
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
	GetOrgAccounts(ctx context.Context, orgID string) ([]string, error)
	GetRoomMembers(ctx context.Context, roomID string) ([]model.RoomMember, error)
	EnsureIndexes(ctx context.Context) error
}
