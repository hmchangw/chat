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

const (
	historyPageSize     = 20
	surroundingPageSize = 25
)

// getHistorySharedSince fetches the HistorySharedSince timestamp and validates subscription exists.
func (s *HistoryService) getHistorySharedSince(ctx context.Context, username, roomID string) (time.Time, error) {
	since, err := s.subscriptions.GetHistorySharedSince(ctx, username, roomID)
	if err != nil {
		return time.Time{}, fmt.Errorf("checking subscription: %w", err)
	}
	if since == nil {
		return time.Time{}, natsrouter.ErrWithCode("forbidden", "not subscribed to this room")
	}
	return *since, nil
}

func (s *HistoryService) LoadHistory(ctx context.Context, p natsrouter.Params, req models.LoadHistoryRequest) (*models.LoadHistoryResponse, error) {
	username := p.Get("username")
	since, err := s.getHistorySharedSince(ctx, username, req.RoomID)
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
		limit = historyPageSize
	}
	q, err := parsePageRequest(req.Cursor, limit)
	if err != nil {
		return nil, err
	}

	page, err := s.messages.GetMessagesBefore(ctx, req.RoomID, since, before, q)
	if err != nil {
		return nil, fmt.Errorf("loading history: %w", err)
	}

	resp := &models.LoadHistoryResponse{
		Messages: page.Data,
	}

	// Find firstUnread via DB query if there are messages and lastSeen is set
	if len(page.Data) > 0 && !lastSeen.IsZero() {
		// Messages are newest-first (DESC), so the last element is the oldest in the page
		oldestInPage := page.Data[len(page.Data)-1]

		if lastSeen.Before(oldestInPage.CreatedAt) {
			// There are unread messages — query for the first one
			// after = MAX(historySharedSince, lastSeen)
			after := lastSeen
			if since.After(lastSeen) {
				after = since
			}

			unreadPage, err := s.messages.GetMessagesBetween(ctx, req.RoomID, after, oldestInPage.CreatedAt, cassrepo.PageRequest{
				Cursor: &cassrepo.Cursor{}, PageSize: 1,
			})
			if err != nil {
				return nil, fmt.Errorf("finding first unread: %w", err)
			}

			if len(unreadPage.Data) > 0 {
				resp.FirstUnread = &unreadPage.Data[0]
				resp.HasNextUnread = unreadPage.HasNext
				resp.NextUnreadCursor = unreadPage.NextCursor
			}
		}
	}

	return resp, nil
}

func (s *HistoryService) LoadNextMessages(ctx context.Context, p natsrouter.Params, req models.LoadNextMessagesRequest) (*models.LoadNextMessagesResponse, error) {
	username := p.Get("username")
	since, err := s.getHistorySharedSince(ctx, username, req.RoomID)
	if err != nil {
		return nil, err
	}

	after, err := parseTimestamp(req.After)
	if err != nil {
		return nil, err
	}

	if after.IsZero() || after.Before(since) {
		after = since
	}

	before, err := parseTimestamp(req.Before)
	if err != nil {
		return nil, err
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}

	q, err := parsePageRequest(req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}

	page, err := s.messages.GetMessagesBetween(ctx, req.RoomID, after, before, q)
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
	username := p.Get("username")
	since, err := s.getHistorySharedSince(ctx, username, req.RoomID)
	if err != nil {
		return nil, err
	}

	msg, err := s.messages.GetMessageByID(ctx, req.RoomID, req.MessageID)
	if err != nil {
		return nil, fmt.Errorf("finding central message: %w", err)
	}
	if msg == nil {
		return nil, natsrouter.ErrWithCode("not_found", "message not found")
	}
	if msg.CreatedAt.Before(since) {
		return nil, natsrouter.ErrWithCode("forbidden", "message is outside access window")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = surroundingPageSize
	}
	half := limit / 2

	// Fetch messages before the central message (newest-first within the before range)
	beforePage, err := s.messages.GetMessagesBefore(ctx, req.RoomID, since, msg.CreatedAt, cassrepo.PageRequest{
		Cursor: &cassrepo.Cursor{}, PageSize: half,
	})
	if err != nil {
		return nil, fmt.Errorf("loading surrounding before: %w", err)
	}

	// Fetch messages after the central message (oldest-first)
	afterPage, err := s.messages.GetMessagesBetween(ctx, req.RoomID, msg.CreatedAt, time.Now().UTC(), cassrepo.PageRequest{
		Cursor: &cassrepo.Cursor{}, PageSize: half,
	})
	if err != nil {
		return nil, fmt.Errorf("loading surrounding after: %w", err)
	}

	// Combine: reverse before (they come DESC) + central + after (ASC)
	messages := make([]model.Message, 0, len(beforePage.Data)+1+len(afterPage.Data))
	for i := len(beforePage.Data) - 1; i >= 0; i-- {
		messages = append(messages, beforePage.Data[i])
	}
	messages = append(messages, *msg)
	messages = append(messages, afterPage.Data...)

	return &models.LoadSurroundingMessagesResponse{
		Messages:   messages,
		MoreBefore: beforePage.HasNext,
		MoreAfter:  afterPage.HasNext,
	}, nil
}

func (s *HistoryService) GetMessageByID(ctx context.Context, p natsrouter.Params, req models.GetMessageByIDRequest) (*model.Message, error) {
	username := p.Get("username")
	since, err := s.getHistorySharedSince(ctx, username, req.RoomID)
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
