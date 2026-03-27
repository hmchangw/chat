package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/model"
)

const defaultLimit = 50

func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// getHistorySharedSince fetches the HistorySharedSince timestamp and validates subscription exists.
func (s *HistoryService) getHistorySharedSince(ctx context.Context, userID, roomID string) (time.Time, error) {
	since, err := s.subscriptions.GetHistorySharedSince(ctx, userID, roomID)
	if err != nil {
		return time.Time{}, fmt.Errorf("checking subscription: %w", err)
	}
	if since == nil {
		return time.Time{}, fmt.Errorf("user %s is not subscribed to room %s", userID, roomID)
	}
	return *since, nil
}

func (s *HistoryService) LoadHistory(ctx context.Context, userID string, req models.LoadHistoryRequest) (*models.LoadHistoryResponse, error) {
	since, err := s.getHistorySharedSince(ctx, userID, req.RoomID)
	if err != nil {
		return nil, err
	}

	before, err := parseTimestamp(req.Before)
	if err != nil {
		return nil, fmt.Errorf("parsing before timestamp: %w", err)
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}

	lastSeen, err := parseTimestamp(req.LastSeen)
	if err != nil {
		return nil, fmt.Errorf("parsing lastSeen timestamp: %w", err)
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

func (s *HistoryService) LoadNextMessages(ctx context.Context, userID string, req models.LoadNextMessagesRequest) (*models.LoadNextMessagesResponse, error) {
	since, err := s.getHistorySharedSince(ctx, userID, req.RoomID)
	if err != nil {
		return nil, err
	}

	after, err := parseTimestamp(req.After)
	if err != nil {
		return nil, fmt.Errorf("parsing after timestamp: %w", err)
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

func (s *HistoryService) LoadSurroundingMessages(ctx context.Context, userID string, req models.LoadSurroundingMessagesRequest) (*models.LoadSurroundingMessagesResponse, error) {
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

	// Validate central message is within access window.
	// The central message is the first element in the after slice.
	if len(after) > 0 && after[0].CreatedAt.Before(since) {
		return nil, fmt.Errorf("message %s is outside access window", req.MessageID)
	}

	// Filter out messages before HistorySharedSince from both slices.
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

func (s *HistoryService) GetMessageByID(ctx context.Context, userID string, req models.GetMessageByIDRequest) (*model.Message, error) {
	since, err := s.getHistorySharedSince(ctx, userID, req.RoomID)
	if err != nil {
		return nil, err
	}

	msg, err := s.messages.GetMessageByID(ctx, req.RoomID, req.MessageID)
	if err != nil {
		return nil, fmt.Errorf("loading message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message %s not found", req.MessageID)
	}

	if msg.CreatedAt.Before(since) {
		return nil, fmt.Errorf("message %s is outside access window", req.MessageID)
	}

	return msg, nil
}
