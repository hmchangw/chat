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

func (s *HistoryService) LoadHistory(ctx context.Context, p natsrouter.Params, req models.LoadHistoryRequest) (*models.LoadHistoryResponse, error) {
	roomID, err := resolveRoomID(p, req.RoomID)
	if err != nil {
		return nil, err
	}

	accessSince, err := s.checkAccess(ctx, p.Get("username"), roomID)
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
	pageReq, err := parsePageRequest(req.Cursor, limit)
	if err != nil {
		return nil, err
	}

	// Fetch history page. With accessSince set, enforce it as the lower bound.
	var page cassrepo.Page[model.Message]
	if accessSince == nil {
		page, err = s.messages.GetMessagesBefore(ctx, roomID, before, pageReq)
	} else {
		page, err = s.messages.GetMessagesBetweenDesc(ctx, roomID, *accessSince, before, pageReq)
	}
	if err != nil {
		return nil, fmt.Errorf("loading history: %w", err)
	}

	resp := &models.LoadHistoryResponse{
		Messages: page.Data,
	}

	// Find the first unread message via a separate query when:
	// - the page has messages
	// - the client provided a lastSeen timestamp
	// - lastSeen is before the oldest message in the page (meaning unread messages may exist before it)
	if len(page.Data) > 0 && !lastSeen.IsZero() {
		oldestInPage := page.Data[len(page.Data)-1]

		if lastSeen.Before(oldestInPage.CreatedAt) {
			// Lower bound for the unread query: max(accessSince, lastSeen).
			// If accessSince is nil (no restriction), just use lastSeen.
			unreadAfter := lastSeen
			if accessSince != nil && accessSince.After(lastSeen) {
				unreadAfter = *accessSince
			}

			unreadPage, err := s.messages.GetMessagesBetweenAsc(ctx, roomID, unreadAfter, oldestInPage.CreatedAt, true, cassrepo.PageRequest{
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
	roomID, err := resolveRoomID(p, req.RoomID)
	if err != nil {
		return nil, err
	}

	accessSince, err := s.checkAccess(ctx, p.Get("username"), roomID)
	if err != nil {
		return nil, err
	}

	after, err := parseTimestamp(req.After)
	if err != nil {
		return nil, err
	}

	// Lower bound = max(after, accessSince). Zero means no lower bound.
	lowerBound := timeMax(after, derefTime(accessSince))

	pageReq, err := parsePageRequest(req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}

	var page cassrepo.Page[model.Message]
	if lowerBound.IsZero() {
		page, err = s.messages.GetAllMessagesAsc(ctx, roomID, pageReq)
	} else {
		page, err = s.messages.GetMessagesAfter(ctx, roomID, lowerBound, pageReq)
	}
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
	roomID, err := resolveRoomID(p, req.RoomID)
	if err != nil {
		return nil, err
	}

	accessSince, err := s.checkAccess(ctx, p.Get("username"), roomID)
	if err != nil {
		return nil, err
	}

	centralMsg, err := s.messages.GetMessageByID(ctx, roomID, req.MessageID)
	if err != nil {
		return nil, fmt.Errorf("finding central message: %w", err)
	}
	if centralMsg == nil {
		return nil, natsrouter.ErrWithCode("not_found", "message not found")
	}
	if accessSince != nil && centralMsg.CreatedAt.Before(*accessSince) {
		return nil, natsrouter.ErrWithCode("forbidden", "message is outside access window")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = surroundingPageSize
	}
	half := limit / 2
	if half == 0 {
		return &models.LoadSurroundingMessagesResponse{
			Messages: []model.Message{*centralMsg},
		}, nil
	}

	halfPageReq := cassrepo.PageRequest{Cursor: &cassrepo.Cursor{}, PageSize: half}

	// Before-page: messages older than central, newest-first.
	var beforePage cassrepo.Page[model.Message]
	if accessSince == nil {
		beforePage, err = s.messages.GetMessagesBefore(ctx, roomID, centralMsg.CreatedAt, halfPageReq)
	} else {
		beforePage, err = s.messages.GetMessagesBetweenDesc(ctx, roomID, *accessSince, centralMsg.CreatedAt, halfPageReq)
	}
	if err != nil {
		return nil, fmt.Errorf("loading surrounding before: %w", err)
	}

	// After-page: messages newer than central, oldest-first.
	afterPage, err := s.messages.GetMessagesAfter(ctx, roomID, centralMsg.CreatedAt, halfPageReq)
	if err != nil {
		return nil, fmt.Errorf("loading surrounding after: %w", err)
	}

	// Assemble: reverse before-page (DESC→ASC) + central + after-page (already ASC).
	messages := make([]model.Message, 0, len(beforePage.Data)+1+len(afterPage.Data))
	for i := len(beforePage.Data) - 1; i >= 0; i-- {
		messages = append(messages, beforePage.Data[i])
	}
	messages = append(messages, *centralMsg)
	messages = append(messages, afterPage.Data...)

	return &models.LoadSurroundingMessagesResponse{
		Messages:   messages,
		MoreBefore: beforePage.HasNext,
		MoreAfter:  afterPage.HasNext,
	}, nil
}

func (s *HistoryService) GetMessageByID(ctx context.Context, p natsrouter.Params, req models.GetMessageByIDRequest) (*model.Message, error) {
	roomID, err := resolveRoomID(p, req.RoomID)
	if err != nil {
		return nil, err
	}

	accessSince, err := s.checkAccess(ctx, p.Get("username"), roomID)
	if err != nil {
		return nil, err
	}

	msg, err := s.messages.GetMessageByID(ctx, roomID, req.MessageID)
	if err != nil {
		return nil, fmt.Errorf("loading message: %w", err)
	}
	if msg == nil {
		return nil, natsrouter.ErrWithCode("not_found", "message not found")
	}

	if accessSince != nil && msg.CreatedAt.Before(*accessSince) {
		return nil, natsrouter.ErrWithCode("forbidden", "message is outside access window")
	}

	return msg, nil
}
