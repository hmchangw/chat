package service

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	pkgmodel "github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// GetThreadMessages handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread.
func (s *HistoryService) GetThreadMessages(c *natsrouter.Context, req models.GetThreadMessagesRequest) (*models.GetThreadMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	if req.ThreadMessageID == "" {
		return nil, natsrouter.ErrBadRequest("threadMessageId is required")
	}

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	msg, err := s.findMessage(c, roomID, req.ThreadMessageID)
	if err != nil {
		return nil, err
	}

	if msg.ThreadParentID != "" {
		return nil, natsrouter.ErrBadRequest("threadMessageId must be a top-level message, not a reply")
	}

	if accessSince != nil && msg.CreatedAt.Before(*accessSince) {
		return nil, natsrouter.ErrForbidden("thread is outside access window")
	}

	// Empty ThreadRoomID means no replies yet or a silently-failed stamp in message-worker.
	if msg.ThreadRoomID == "" {
		slog.Warn("thread fetch: parent has empty thread_room_id, returning no replies",
			"request_id", natsutil.RequestIDFromContext(c),
			"roomID", roomID,
			"messageID", req.ThreadMessageID,
			"messageCreatedAt", msg.CreatedAt,
			"account", account,
		)
		return &models.GetThreadMessagesResponse{Messages: []models.Message{}, HasNext: false}, nil
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	pageReq, err := parsePageRequest(req.Cursor, limit)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	lastMsgAt, createdAt, err := s.resolveRoomTimesOrError(c, roomID, req.Meta, now)
	if err != nil {
		return nil, err
	}

	// Ceiling: lastMsgAt+1ms or now+clockSkewTolerance when unknown.
	ceiling := lastMsgAt
	if ceiling.IsZero() {
		ceiling = now.Add(clockSkewTolerance)
	} else {
		ceiling = ceiling.Add(time.Millisecond)
	}

	// Floor: max(createdAt, accessSince) clamped to historyFloor so an ancient createdAt can't exceed the configured limit.
	historyFloor := now.Add(-s.historyFloor)
	floor := createdAt
	if accessSince != nil && accessSince.After(floor) {
		floor = *accessSince
	}
	if floor.IsZero() || floor.Before(historyFloor) {
		floor = historyFloor
	}
	// Inverted range guard: collapsed thread on a room older than historyFloor.
	if ceiling.Before(floor) {
		ceiling = floor
	}

	page, err := s.msgReader.GetThreadMessages(c, roomID, msg.ThreadRoomID, ceiling, floor, pageReq)
	if err != nil {
		slog.Error("loading thread messages", "error", err, "request_id", natsutil.RequestIDFromContext(c), "roomID", roomID, "threadRoomID", msg.ThreadRoomID)
		return nil, natsrouter.ErrInternal("failed to load thread messages")
	}

	redactUnavailableQuotes(page.Data, accessSince)
	return &models.GetThreadMessagesResponse{
		Messages:   page.Data,
		NextCursor: page.NextCursor,
		HasNext:    page.HasNext,
	}, nil
}

// validateThreadFilter normalizes an empty filter to "all" so clients can omit the field.
func validateThreadFilter(filter models.ThreadFilter) (models.ThreadFilter, error) {
	switch filter {
	case "", models.ThreadFilterAll:
		return models.ThreadFilterAll, nil
	case models.ThreadFilterFollowing, models.ThreadFilterUnread:
		return filter, nil
	default:
		return "", natsrouter.ErrBadRequest(fmt.Sprintf("invalid thread filter: %q", filter))
	}
}

// GetThreadParentMessages handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread.parent.
func (s *HistoryService) GetThreadParentMessages(c *natsrouter.Context, req models.GetThreadParentMessagesRequest) (*models.GetThreadParentMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	filter, err := validateThreadFilter(req.Filter)
	if err != nil {
		return nil, err
	}

	pageReq := mongoutil.NewOffsetPageRequest(req.Offset, req.Limit)

	var threadPage mongoutil.OffsetPage[pkgmodel.ThreadRoom]
	switch filter {
	case models.ThreadFilterAll:
		threadPage, err = s.threadRooms.GetThreadRooms(c, roomID, accessSince, pageReq)
	case models.ThreadFilterFollowing:
		threadPage, err = s.threadRooms.GetFollowingThreadRooms(c, roomID, account, accessSince, pageReq)
	case models.ThreadFilterUnread:
		threadPage, err = s.threadRooms.GetUnreadThreadRooms(c, roomID, account, accessSince, pageReq)
	default:
		slog.Error("unhandled thread filter", "filter", filter)
		return nil, natsrouter.ErrInternal("unhandled thread filter")
	}
	if err != nil {
		slog.Error("loading thread rooms from MongoDB", "error", err, "request_id", natsutil.RequestIDFromContext(c), "roomID", roomID, "filter", filter)
		return nil, natsrouter.ErrInternal("failed to load thread parent messages")
	}

	if len(threadPage.Data) == 0 {
		return &models.GetThreadParentMessagesResponse{ParentMessages: []models.Message{}, Total: threadPage.Total}, nil
	}

	seenIDs := make(map[string]struct{}, len(threadPage.Data))
	parentIDs := make([]string, 0, len(threadPage.Data))
	for i := range threadPage.Data {
		id := threadPage.Data[i].ParentMessageID
		if _, dup := seenIDs[id]; dup {
			continue
		}
		seenIDs[id] = struct{}{}
		parentIDs = append(parentIDs, id)
	}

	cassMessages, err := s.msgReader.GetMessagesByIDs(c, parentIDs)
	if err != nil {
		slog.Error("hydrating thread parent messages from Cassandra", "error", err, "request_id", natsutil.RequestIDFromContext(c), "roomID", roomID)
		return nil, natsrouter.ErrInternal("failed to load thread parent messages")
	}

	msgByID := make(map[string]models.Message, len(cassMessages))
	for i := range cassMessages {
		msgByID[cassMessages[i].MessageID] = cassMessages[i]
	}

	// Iterate parentIDs (deduplicated) to avoid emitting the same parent twice for duplicate MongoDB thread rooms.
	parentMessages := make([]models.Message, 0, len(parentIDs))
	for _, id := range parentIDs {
		msg, ok := msgByID[id]
		if !ok {
			continue
		}
		if msg.RoomID != roomID {
			slog.Warn("thread parent message belongs to unexpected room", "messageID", id, "gotRoom", msg.RoomID, "wantRoom", roomID)
			continue
		}
		if accessSince != nil && msg.CreatedAt.Before(*accessSince) {
			continue
		}
		parentMessages = append(parentMessages, msg)
	}

	redactUnavailableQuotes(parentMessages, accessSince)
	return &models.GetThreadParentMessagesResponse{ParentMessages: parentMessages, Total: threadPage.Total}, nil
}
