package main

import (
	"context"
	"errors"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store

// errNotSubscribed is returned when the user is not subscribed to the room.
var errNotSubscribed = errors.New("not subscribed")

type Store interface {
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
}
