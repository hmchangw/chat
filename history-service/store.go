package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . HistoryStore

type HistoryStore interface {
	GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error)
	ListMessages(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
}
