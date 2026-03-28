package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store

type Store interface {
	GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error)
}
