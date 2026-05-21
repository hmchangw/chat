package service

import (
	"context"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/mongorepo"
	pkgmodel "github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/subject"
)

//go:generate mockgen -destination=mocks/mock_repository.go -package=mocks . MessageReader,MessageWriter,MessageRepository,SubscriptionRepository,RoomRepository,EventPublisher,ThreadRoomRepository,UserStore,CustomEmojiStore

type MessageReader interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, floor time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, ceiling time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, floor, ceiling time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
	GetThreadMessages(ctx context.Context, roomID, threadRoomID string, before, floor time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error)
}

type MessageWriter interface {
	UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error
	// SoftDeleteMessage performs a Cassandra LWT on messages_by_id and only
	// runs the mirror-table and parent-tcount work when the LWT applies.
	// Returns the updated_at value now persisted (the deletedAt argument when
	// applied; the existing value when a concurrent delete won the race).
	SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) (actualDeletedAt time.Time, applied bool, err error)
	// ToggleReaction adds actor to msg.Reactions[shortcode] when absent and
	// removes it when present. Uses an LWT-with-retry loop on messages_by_id
	// to converge under concurrent toggles, then mirrors the new value to
	// messages_by_room / thread_messages_by_room / pinned_messages_by_room
	// as applicable. The returned action is "added" or "removed".
	ToggleReaction(ctx context.Context, msg *models.Message, shortcode string, actor models.Participant, reactedAt time.Time) (action string, err error)
}

// MessageRepository composes read and write access; satisfied by *cassrepo.Repository.
type MessageRepository interface {
	MessageReader
	MessageWriter
}

type SubscriptionRepository interface {
	GetHistorySharedSince(ctx context.Context, account, roomID string) (*time.Time, bool, error)
}

// RoomRepository reads room metadata required by history handlers:
// MinUserLastSeenAt as a per-user read-receipt floor surfaced to clients, and
// GetRoomTimes (lastMsgAt, createdAt) for bucket-walk bounds.
type RoomRepository interface {
	GetMinUserLastSeenAt(ctx context.Context, roomID string) (*time.Time, error)
	GetRoomTimes(ctx context.Context, roomID string) (lastMsgAt, createdAt time.Time, err error)
}

// EventPublisher publishes live events to a NATS subject. Implemented by a
// thin wrapper around *otelnats.Conn in main.go.
type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type ThreadRoomRepository interface {
	GetThreadRooms(ctx context.Context, roomID string, accessSince *time.Time, req mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[pkgmodel.ThreadRoom], error)
	GetFollowingThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[pkgmodel.ThreadRoom], error)
	GetUnreadThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[pkgmodel.ThreadRoom], error)
}

// UserStore resolves the calling user's full profile so the reaction handler
// can populate the cassandra Participant stored in the reactions set and the
// pkg/model.Participant carried on the canonical event.
type UserStore interface {
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]pkgmodel.User, error)
}

// CustomEmojiStore reports whether a custom emoji shortcode is registered for
// the site. Implementations typically query the custom_emojis Mongo collection.
type CustomEmojiStore interface {
	CustomEmojiExists(ctx context.Context, siteID, shortcode string) (bool, error)
}

// HistoryService handles message history queries and mutations. Transport-agnostic.
type HistoryService struct {
	msgReader     MessageReader
	msgWriter     MessageWriter
	subscriptions SubscriptionRepository
	rooms         RoomRepository
	publisher     EventPublisher
	threadRooms   ThreadRoomRepository
	users         UserStore
	customEmojis  CustomEmojiStore
	historyFloor  time.Duration // from MESSAGE_HISTORY_FLOOR_DAYS
}

func New(
	msgs MessageRepository,
	subs SubscriptionRepository,
	rooms RoomRepository,
	pub EventPublisher,
	threadRooms ThreadRoomRepository,
	users UserStore,
	customEmojis CustomEmojiStore,
	historyFloor time.Duration,
) *HistoryService {
	return &HistoryService{
		msgReader:     msgs,
		msgWriter:     msgs,
		subscriptions: subs,
		rooms:         rooms,
		publisher:     pub,
		threadRooms:   threadRooms,
		users:         users,
		customEmojis:  customEmojis,
		historyFloor:  historyFloor,
	}
}

// RegisterHandlers wires all NATS endpoints. Panics on subscription failure (fatal at startup).
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	natsrouter.Register(r, subject.MsgHistoryPattern(siteID), s.LoadHistory)
	natsrouter.Register(r, subject.MsgNextPattern(siteID), s.LoadNextMessages)
	natsrouter.Register(r, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
	natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
	natsrouter.Register(r, subject.MsgEditPattern(siteID), func(c *natsrouter.Context, req models.EditMessageRequest) (*models.EditMessageResponse, error) {
		return s.EditMessage(c, siteID, req)
	})
	natsrouter.Register(r, subject.MsgDeletePattern(siteID), func(c *natsrouter.Context, req models.DeleteMessageRequest) (*models.DeleteMessageResponse, error) {
		return s.DeleteMessage(c, siteID, req)
	})
	natsrouter.Register(r, subject.MsgReactPattern(siteID), func(c *natsrouter.Context, req models.ReactMessageRequest) (*models.ReactMessageResponse, error) {
		return s.ReactMessage(c, siteID, req)
	})
	natsrouter.Register(r, subject.MsgThreadPattern(siteID), s.GetThreadMessages)
	natsrouter.Register(r, subject.MsgThreadParentPattern(siteID), s.GetThreadParentMessages)
}

// Compile-time checks.
var _ MessageRepository = (*cassrepo.Repository)(nil)
var _ RoomRepository = (*mongorepo.RoomRepo)(nil)
