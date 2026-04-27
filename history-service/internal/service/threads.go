package service

import (
	"fmt"
	"log/slog"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/mongorepo"
	pkgmodel "github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// GetThreadMessages returns a paginated list of replies for a thread.
// NATS subject: chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread
func (s *HistoryService) GetThreadMessages(c *natsrouter.Context, req models.GetThreadMessagesRequest) (*models.GetThreadMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	if req.ThreadMessageID == "" {
		return nil, natsrouter.ErrBadRequest("threadMessageId is required")
	}

	// Access check runs before the message fetch so an unauthenticated caller
	// cannot probe whether arbitrary message IDs exist in rooms they don't belong to.
	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	msg, err := s.findMessage(c, roomID, req.ThreadMessageID)
	if err != nil {
		return nil, err
	}

	// If the submitted ID belongs to a reply (not a parent), resolve the true parent
	// so the access check enforces the historySharedSince gate against the correct row.
	// Replies themselves never carry a ThreadParentID, so one indirection is enough.
	parent := msg
	if msg.ThreadParentID != "" {
		parent, err = s.findMessage(c, roomID, msg.ThreadParentID)
		if err != nil {
			return nil, err
		}
		if parent.ThreadParentID != "" {
			slog.Error("data model violation: resolved parent is itself a reply",
				"messageID", msg.ThreadParentID, "parentThreadParentID", parent.ThreadParentID)
			return nil, natsrouter.ErrInternal("invalid thread structure")
		}
	}

	if accessSince != nil && parent.CreatedAt.Before(*accessSince) {
		return nil, natsrouter.ErrForbidden("thread is outside access window")
	}

	// thread_room_id is stamped onto the parent row by message-worker when the first
	// reply arrives. An empty value means no replies exist yet (or the reply event
	// lacked ThreadParentMessageCreatedAt so the stamp was skipped).
	if parent.ThreadRoomID == "" {
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

	page, err := s.messages.GetThreadMessages(c, roomID, parent.ThreadRoomID, pageReq)
	if err != nil {
		slog.Error("loading thread messages", "error", err, "roomID", roomID, "threadRoomID", parent.ThreadRoomID)
		return nil, natsrouter.ErrInternal("failed to load thread messages")
	}

	redactUnavailableQuotes(page.Data, accessSince)
	return &models.GetThreadMessagesResponse{
		Messages:   page.Data,
		NextCursor: page.NextCursor,
		HasNext:    page.HasNext,
	}, nil
}

// GetThreadParentMessages returns a paginated list of thread parent messages for a room.
// NATS subject: chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread.parent
func (s *HistoryService) GetThreadParentMessages(c *natsrouter.Context, req models.GetThreadParentMessagesRequest) (*models.GetThreadParentMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	// Treat empty filter as "all" so clients can omit the field.
	filter := req.Filter
	if filter == "" {
		filter = models.ThreadFilterAll
	}

	pageReq := mongorepo.NewOffsetPageRequest(req.Offset, req.Limit)

	var threadPage mongorepo.OffsetPage[pkgmodel.ThreadRoom]
	switch filter {
	case models.ThreadFilterAll:
		threadPage, err = s.threadRooms.GetThreadRooms(c, roomID, accessSince, pageReq)
	case models.ThreadFilterFollowing:
		threadPage, err = s.threadRooms.GetFollowingThreadRooms(c, roomID, account, accessSince, pageReq)
	case models.ThreadFilterUnread:
		threadPage, err = s.threadRooms.GetUnreadThreadRooms(c, roomID, account, accessSince, pageReq)
	default:
		return nil, natsrouter.ErrBadRequest(fmt.Sprintf("invalid thread filter: %q", filter))
	}
	if err != nil {
		slog.Error("loading thread rooms from MongoDB", "error", err, "roomID", roomID, "filter", filter)
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

	cassMessages, err := s.messages.GetMessagesByIDs(c, parentIDs)
	if err != nil {
		slog.Error("hydrating thread parent messages from Cassandra", "error", err, "roomID", roomID)
		return nil, natsrouter.ErrInternal("failed to load thread parent messages")
	}

	msgByID := make(map[string]models.Message, len(cassMessages))
	for i := range cassMessages {
		msgByID[cassMessages[i].MessageID] = cassMessages[i]
	}

	// Preserve MongoDB sort order; silently skip messages missing in Cassandra or
	// belonging to a different room (defense-in-depth against data drift).
	// accessSince is re-checked against the actual Cassandra CreatedAt because
	// MongoDB's threadParentCreatedAt can be zero when the field was absent from
	// the original event, which would bypass the initial $match filter.
	parentMessages := make([]models.Message, 0, len(threadPage.Data))
	for i := range threadPage.Data {
		msg, ok := msgByID[threadPage.Data[i].ParentMessageID]
		if !ok || msg.RoomID != roomID {
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
