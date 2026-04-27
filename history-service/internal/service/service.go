package service

import (
	"context"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/mongorepo"
	pkgmodel "github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/subject"
)

//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageRepository,SubscriptionRepository,ThreadRoomRepository

type MessageRepository interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
	GetThreadMessages(ctx context.Context, roomID, threadRoomID string, q cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error)
}

type SubscriptionRepository interface {
	GetHistorySharedSince(ctx context.Context, account, roomID string) (*time.Time, bool, error)
}

type ThreadRoomRepository interface {
	GetThreadRooms(ctx context.Context, roomID string, accessSince *time.Time, req mongorepo.OffsetPageRequest) (mongorepo.OffsetPage[pkgmodel.ThreadRoom], error)
	GetFollowingThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req mongorepo.OffsetPageRequest) (mongorepo.OffsetPage[pkgmodel.ThreadRoom], error)
	GetUnreadThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req mongorepo.OffsetPageRequest) (mongorepo.OffsetPage[pkgmodel.ThreadRoom], error)
}

// HistoryService handles message history queries. Transport-agnostic.
type HistoryService struct {
	messages      MessageRepository
	subscriptions SubscriptionRepository
	threadRooms   ThreadRoomRepository
}

func New(msgs MessageRepository, subs SubscriptionRepository, threadRooms ThreadRoomRepository) *HistoryService {
	return &HistoryService{messages: msgs, subscriptions: subs, threadRooms: threadRooms}
}

// RegisterHandlers wires all NATS endpoints. Panics on subscription failure (fatal at startup).
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
	natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
	natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
	natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
	natsrouter.Register(r, subject.MsgThreadPattern(siteID), s.GetThreadMessages)
	natsrouter.Register(r, subject.MsgThreadParentPattern(siteID), s.GetThreadParentMessages)
}
