package service

import (
	"context"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/natshandler"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
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

// RegisterHandlers wires all NATS endpoints for the history service.
func (s *HistoryService) RegisterHandlers(nh *natshandler.Handler, siteID string) error {
	if err := natshandler.Register[models.LoadHistoryRequest, models.LoadHistoryResponse](nh, subject.MsgHistoryWildcard(siteID), s.LoadHistory); err != nil {
		return err
	}
	if err := natshandler.Register[models.LoadNextMessagesRequest, models.LoadNextMessagesResponse](nh, subject.MsgNextWildcard(siteID), s.LoadNextMessages); err != nil {
		return err
	}
	if err := natshandler.Register[models.LoadSurroundingMessagesRequest, models.LoadSurroundingMessagesResponse](nh, subject.MsgSurroundingWildcard(siteID), s.LoadSurroundingMessages); err != nil {
		return err
	}
	if err := natshandler.Register[models.GetMessageByIDRequest, model.Message](nh, subject.MsgGetWildcard(siteID), s.GetMessageByID); err != nil {
		return err
	}
	return nil
}
