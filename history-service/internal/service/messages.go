package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

const defaultLimit = 50

func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, natsrouter.ErrWithCode("bad_request", "invalid timestamp format")
	}
	return t, nil
}

// getHistorySharedSince fetches the HistorySharedSince timestamp and validates subscription exists.
func (s *HistoryService) getHistorySharedSince(ctx context.Context, userID, roomID string) (time.Time, error) {
	since, err := s.subscriptions.GetHistorySharedSince(ctx, userID, roomID)
	if err != nil {
		return time.Time{}, fmt.Errorf("checking subscription: %w", err)
	}
	if since == nil {
		return time.Time{}, natsrouter.ErrWithCode("forbidden", "not subscribed to this room")
	}
	return *since, nil
}

func (s *HistoryService) LoadHistory(ctx context.Context, p natsrouter.Params, req models.LoadHistoryRequest) (*models.LoadHistoryResponse, error) {
	userID := p.Get("userID")
	since, err := s.getHistorySharedSince(ctx, userID, req.RoomID)
	if err != nil {
		return nil, err
	}

	before, err := parseTimestamp(req.Before)
	if err != nil {
		return nil, err
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}

	lastSeen, err := parseTimestamp(req.LastSeen)
	if err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	msgs, err := s.messages.GetMessagesBefore(ctx, req.RoomID, since, before, limit+1)
	if err != nil {
		return nil, fmt.Errorf("loading history: %w", err)
	}

	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}

	var firstUnread *model.Message
	if !lastSeen.IsZero() {
		for i := range msgs {
			if msgs[i].CreatedAt.After(lastSeen) {
				firstUnread = &msgs[i]
			}
		}
	}

	return &models.LoadHistoryResponse{
		Messages:    msgs,
		FirstUnread: firstUnread,
		HasMore:     hasMore,
	}, nil
}

func (s *HistoryService) LoadNextMessages(ctx context.Context, p natsrouter.Params, req models.LoadNextMessagesRequest) (*models.LoadNextMessagesResponse, error) {
	userID := p.Get("userID")
	since, err := s.getHistorySharedSince(ctx, userID, req.RoomID)
	if err != nil {
		return nil, err
	}

	after, err := parseTimestamp(req.After)
	if err != nil {
		return nil, err
	}

	if !after.IsZero() && after.Before(since) {
		after = since
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	msgs, err := s.messages.GetMessagesAfter(ctx, req.RoomID, after, limit+1)
	if err != nil {
		return nil, fmt.Errorf("loading next messages: %w", err)
	}

	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}

	return &models.LoadNextMessagesResponse{
		Messages: msgs,
		HasMore:  hasMore,
	}, nil
}

func (s *HistoryService) LoadSurroundingMessages(ctx context.Context, p natsrouter.Params, req models.LoadSurroundingMessagesRequest) (*models.LoadSurroundingMessagesResponse, error) {
	userID := p.Get("userID")
	since, err := s.getHistorySharedSince(ctx, userID, req.RoomID)
	if err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	before, after, err := s.messages.GetSurroundingMessages(ctx, req.RoomID, req.MessageID, limit)
	if err != nil {
		return nil, fmt.Errorf("loading surrounding messages: %w", err)
	}

	if len(after) > 0 && after[0].CreatedAt.Before(since) {
		return nil, natsrouter.ErrWithCode("forbidden", "message is outside access window")
	}

	filteredBefore := before[:0]
	for _, m := range before {
		if !m.CreatedAt.Before(since) {
			filteredBefore = append(filteredBefore, m)
		}
	}
	filteredAfter := after[:0]
	for _, m := range after {
		if !m.CreatedAt.Before(since) {
			filteredAfter = append(filteredAfter, m)
		}
	}

	return &models.LoadSurroundingMessagesResponse{
		Before: filteredBefore,
		After:  filteredAfter,
	}, nil
}

func (s *HistoryService) GetMessageByID(ctx context.Context, p natsrouter.Params, req models.GetMessageByIDRequest) (*model.Message, error) {
	userID := p.Get("userID")
	since, err := s.getHistorySharedSince(ctx, userID, req.RoomID)
	if err != nil {
		return nil, err
	}

	msg, err := s.messages.GetMessageByID(ctx, req.RoomID, req.MessageID)
	if err != nil {
		return nil, fmt.Errorf("loading message: %w", err)
	}
	if msg == nil {
		return nil, natsrouter.ErrWithCode("not_found", "message not found")
	}

	if msg.CreatedAt.Before(since) {
		return nil, natsrouter.ErrWithCode("forbidden", "message is outside access window")
	}

	return msg, nil
}
