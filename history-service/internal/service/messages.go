package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
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

func parsePaginationQuery(cursor string, limit int) (cassrepo.Query, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	q, err := cassrepo.ParseQuery(cursor, limit)
	if err != nil {
		return cassrepo.Query{}, natsrouter.ErrWithCode("bad_request", "invalid pagination cursor")
	}
	return q, nil
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

	q, err := parsePaginationQuery(req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}

	page, err := s.messages.GetMessagesBefore(ctx, req.RoomID, since, before, q)
	if err != nil {
		return nil, fmt.Errorf("loading history: %w", err)
	}

	var firstUnread *model.Message
	if !lastSeen.IsZero() {
		for i := range page.Data {
			if page.Data[i].CreatedAt.After(lastSeen) {
				firstUnread = &page.Data[i]
			}
		}
	}

	return &models.LoadHistoryResponse{
		Messages:    page.Data,
		FirstUnread: firstUnread,
		NextCursor:  page.NextCursor,
		HasNext:     page.HasNext,
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

	q, err := parsePaginationQuery(req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}

	page, err := s.messages.GetMessagesAfter(ctx, req.RoomID, after, q)
	if err != nil {
		return nil, fmt.Errorf("loading next messages: %w", err)
	}

	return &models.LoadNextMessagesResponse{
		Messages:   page.Data,
		NextCursor: page.NextCursor,
		HasNext:    page.HasNext,
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
