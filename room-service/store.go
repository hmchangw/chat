package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	GetRoomMembers(ctx context.Context, roomID string) ([]model.RoomMember, error)
	CreateRoomMember(ctx context.Context, member *model.RoomMember) error
	BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error
	GetOrgData(ctx context.Context, orgID string) (name, locationURL string, err error)
	GetUserID(ctx context.Context, username string) (string, error)
	GetUserSite(ctx context.Context, username string) (string, error)
	CountSubscriptions(ctx context.Context, roomID string) (int, error)
	ListSubscriptionsByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
}
