package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . MessageStore

// MessageStore defines persistence operations for the message worker.
type MessageStore interface {
	SaveMessage(ctx context.Context, msg *model.Message) error
}
