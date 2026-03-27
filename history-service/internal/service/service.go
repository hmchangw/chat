package service

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageRepository,SubscriptionRepository

// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, limit int) ([]model.Message, error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, limit int) ([]model.Message, error)
	GetSurroundingMessages(ctx context.Context, roomID, messageID string, limit int) (before []model.Message, after []model.Message, err error)
	GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error)
}

// SubscriptionRepository defines MongoDB-backed subscription lookups.
type SubscriptionRepository interface {
	GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
	GetHistorySharedSince(ctx context.Context, userID, roomID string) (*time.Time, error)
}

// HistoryService handles message history queries. Transport-agnostic.
type HistoryService struct {
	messages      MessageRepository
	subscriptions SubscriptionRepository
}

// New creates a HistoryService with the given repositories.
func New(msgs MessageRepository, subs SubscriptionRepository) *HistoryService {
	return &HistoryService{messages: msgs, subscriptions: subs}
}
