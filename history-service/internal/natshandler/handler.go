package natshandler

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// Handler wraps the HistoryService and registers NATS subscriptions.
type Handler struct {
	svc    *service.HistoryService
	siteID string
}

// New creates a new NATS Handler.
func New(svc *service.HistoryService, siteID string) *Handler {
	return &Handler{svc: svc, siteID: siteID}
}

// Register wires all 4 NATS queue subscriptions.
func (h *Handler) Register(nc *nats.Conn) error {
	queueGroup := "history-service"
	subs := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{subject.MsgHistoryWildcard(h.siteID), h.handleLoadHistory},
		{subject.MsgNextWildcard(h.siteID), h.handleLoadNextMessages},
		{subject.MsgSurroundingWildcard(h.siteID), h.handleLoadSurroundingMessages},
		{subject.MsgGetWildcard(h.siteID), h.handleGetMessageByID},
	}
	for _, s := range subs {
		if _, err := nc.QueueSubscribe(s.subject, queueGroup, s.handler); err != nil {
			return fmt.Errorf("subscribing to %s: %w", s.subject, err)
		}
	}
	return nil
}

func (h *Handler) handleLoadHistory(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req models.LoadHistoryRequest) (*models.LoadHistoryResponse, error) {
		return h.svc.LoadHistory(ctx, userID, req)
	})
}

func (h *Handler) handleLoadNextMessages(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req models.LoadNextMessagesRequest) (*models.LoadNextMessagesResponse, error) {
		return h.svc.LoadNextMessages(ctx, userID, req)
	})
}

func (h *Handler) handleLoadSurroundingMessages(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req models.LoadSurroundingMessagesRequest) (*models.LoadSurroundingMessagesResponse, error) {
		return h.svc.LoadSurroundingMessages(ctx, userID, req)
	})
}

func (h *Handler) handleGetMessageByID(msg *nats.Msg) {
	userID, _, ok := subject.ParseUserRoomSubject(msg.Subject)
	if !ok {
		natsutil.ReplyError(msg, "invalid subject")
		return
	}
	HandleRequest(msg, func(ctx context.Context, req models.GetMessageByIDRequest) (*model.Message, error) {
		return h.svc.GetMessageByID(ctx, userID, req)
	})
}
