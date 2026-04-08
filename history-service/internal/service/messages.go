package service

import (
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

const (
	historyPageSize     = 20
	surroundingPageSize = 25
	maxPageSize         = 100
)

func (s *HistoryService) LoadHistory(c *natsrouter.Context, req models.LoadHistoryRequest) (*models.LoadHistoryResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	before := millisToTime(req.Before)
	if before.IsZero() {
		before = time.Now().UTC()
	}

	limit := req.Limit
	if limit <= 0 {
		limit = historyPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	pageReq, err := parsePageRequest("", limit)
	if err != nil {
		return nil, err
	}

	var page cassrepo.Page[models.Message]
	if accessSince == nil {
		page, err = s.messages.GetMessagesBefore(c, roomID, before, pageReq)
	} else {
		page, err = s.messages.GetMessagesBetweenDesc(c, roomID, *accessSince, before, pageReq)
	}
	if err != nil {
		return nil, fmt.Errorf("loading history: %w", err)
	}

	return &models.LoadHistoryResponse{Messages: page.Data}, nil
}

func (s *HistoryService) LoadNextMessages(c *natsrouter.Context, req models.LoadNextMessagesRequest) (*models.LoadNextMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	after := millisToTime(req.After)

	// Lower bound = max(after, accessSince). Zero means no lower bound.
	lowerBound := timeMax(after, derefTime(accessSince))

	pageReq, err := parsePageRequest(req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}

	var page cassrepo.Page[models.Message]
	if lowerBound.IsZero() {
		page, err = s.messages.GetAllMessagesAsc(c, roomID, pageReq)
	} else {
		page, err = s.messages.GetMessagesAfter(c, roomID, lowerBound, pageReq)
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

func (s *HistoryService) LoadSurroundingMessages(c *natsrouter.Context, req models.LoadSurroundingMessagesRequest) (*models.LoadSurroundingMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	centralMsg, err := s.findMessage(c, roomID, req.MessageID, req.CreatedAt)
	if err != nil {
		return nil, err
	}
	if accessSince != nil && centralMsg.CreatedAt.Before(*accessSince) {
		return nil, natsrouter.ErrForbidden("message is outside access window")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = surroundingPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	// Split limit-1 (excluding central message) across before and after.
	// before gets the larger half on odd splits.
	remaining := limit - 1
	if remaining <= 0 {
		return &models.LoadSurroundingMessagesResponse{
			Messages: []models.Message{*centralMsg},
		}, nil
	}
	beforeCount := (remaining + 1) / 2
	afterCount := remaining / 2

	beforePageReq, err := parsePageRequest("", beforeCount)
	if err != nil {
		return nil, err
	}
	afterPageReq, err := parsePageRequest("", afterCount)
	if err != nil {
		return nil, err
	}

	// Before-page: messages older than central, newest-first.
	var beforePage cassrepo.Page[models.Message]
	if accessSince == nil {
		beforePage, err = s.messages.GetMessagesBefore(c, roomID, centralMsg.CreatedAt, beforePageReq)
	} else {
		beforePage, err = s.messages.GetMessagesBetweenDesc(c, roomID, *accessSince, centralMsg.CreatedAt, beforePageReq)
	}
	if err != nil {
		return nil, fmt.Errorf("loading surrounding before: %w", err)
	}

	// After-page: messages newer than central, oldest-first.
	afterPage, err := s.messages.GetMessagesAfter(c, roomID, centralMsg.CreatedAt, afterPageReq)
	if err != nil {
		return nil, fmt.Errorf("loading surrounding after: %w", err)
	}

	// Assemble: reverse before-page (DESC→ASC) + central + after-page (already ASC).
	messages := make([]models.Message, 0, len(beforePage.Data)+1+len(afterPage.Data))
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

func (s *HistoryService) GetMessageByID(c *natsrouter.Context, req models.GetMessageByIDRequest) (*models.Message, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	msg, err := s.findMessage(c, roomID, req.MessageID, req.CreatedAt)
	if err != nil {
		return nil, err
	}

	if accessSince != nil && msg.CreatedAt.Before(*accessSince) {
		return nil, natsrouter.ErrForbidden("message is outside access window")
	}

	return msg, nil
}
