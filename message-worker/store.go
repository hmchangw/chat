package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store,ContentEncryptor

// Store defines persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg model.Message) error
}

// ContentEncryptor encrypts message content before persistence.
type ContentEncryptor interface {
	Encrypt(ctx context.Context, roomID, content string) (string, error)
}
