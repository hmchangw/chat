package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

const defaultLimit = 50

func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func (s *HistoryService) LoadHistory(ctx context.Context, userID string, req model.LoadHistoryRequest) (*model.LoadHistoryResponse, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
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

	since := sub.SharedHistorySince
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

	return &model.LoadHistoryResponse{
		Messages:    msgs,
		FirstUnread: firstUnread,
		HasMore:     hasMore,
	}, nil
}

func (s *HistoryService) LoadNextMessages(ctx context.Context, userID string, req model.LoadNextMessagesRequest) (*model.LoadNextMessagesResponse, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
	}

	after, err := parseTimestamp(req.After)
	if err != nil {
		return nil, fmt.Errorf("parsing after timestamp: %w", err)
	}

	if after.Before(sub.SharedHistorySince) {
		after = sub.SharedHistorySince
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

	return &model.LoadNextMessagesResponse{
		Messages: msgs,
		HasMore:  hasMore,
	}, nil
}

func (s *HistoryService) LoadSurroundingMessages(ctx context.Context, userID string, req model.LoadSurroundingMessagesRequest) (*model.LoadSurroundingMessagesResponse, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	before, after, err := s.messages.GetSurroundingMessages(ctx, req.RoomID, req.MessageID, limit)
	if err != nil {
		return nil, fmt.Errorf("loading surrounding messages: %w", err)
	}

	since := sub.SharedHistorySince
	filtered := before[:0]
	for _, m := range before {
		if !m.CreatedAt.Before(since) {
			filtered = append(filtered, m)
		}
	}

	return &model.LoadSurroundingMessagesResponse{
		Before: filtered,
		After:  after,
	}, nil
}

func (s *HistoryService) GetMessageByID(ctx context.Context, userID string, req model.GetMessageByIDRequest) (*model.Message, error) {
	sub, err := s.subscriptions.GetSubscription(ctx, userID, req.RoomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if sub == nil {
		return nil, fmt.Errorf("user %s is not subscribed to room %s", userID, req.RoomID)
	}

	msg, err := s.messages.GetMessageByID(ctx, req.RoomID, req.MessageID)
	if err != nil {
		return nil, fmt.Errorf("loading message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message %s not found", req.MessageID)
	}

	if msg.CreatedAt.Before(sub.SharedHistorySince) {
		return nil, fmt.Errorf("message %s is outside access window", req.MessageID)
	}

	return msg, nil
}
