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
}
