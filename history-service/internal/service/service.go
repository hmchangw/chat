package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageRepository,SubscriptionRepository

// MessageRepository defines Cassandra-backed message operations.
type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, since, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[model.Message], error)
	GetMessagesBetween(ctx context.Context, roomID string, after, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[model.Message], error)
	GetMessageByID(ctx context.Context, roomID, messageID string) (*model.Message, error)
}

// SubscriptionRepository defines MongoDB-backed subscription lookups.
type SubscriptionRepository interface {
	GetHistorySharedSince(ctx context.Context, username, roomID string) (*time.Time, error)
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

// RegisterHandlers wires all NATS endpoints for the history service.
// Panics if any subscription fails (startup-only, fatal if broken).
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	pattern := func(action string) string {
		return fmt.Sprintf("chat.user.{username}.request.room.{roomID}.%s.msg.%s", siteID, action)
	}
	natsrouter.Register(r, pattern("history"), s.LoadHistory)
	natsrouter.Register(r, pattern("next"), s.LoadNextMessages)
	natsrouter.Register(r, pattern("surrounding"), s.LoadSurroundingMessages)
	natsrouter.Register(r, pattern("get"), s.GetMessageByID)
}
