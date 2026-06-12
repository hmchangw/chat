package service

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/errcode"
	pkgmodel "github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// emptyThreadResponse is the canonical shape for "no replies" — keeps the
// shared response shape in one place so future fields can't drift between
// the short-circuit branches.
func emptyThreadResponse() *models.GetThreadMessagesResponse {
	return &models.GetThreadMessagesResponse{Messages: []models.Message{}, HasNext: false}
}

// NATS: chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread
func (s *HistoryService) GetThreadMessages(c *natsrouter.Context, req models.GetThreadMessagesRequest) (*models.GetThreadMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")
	c.WithLogValues("account", account, "room_id", roomID)

	if req.ThreadMessageID == "" {
		return nil, errcode.BadRequest("threadMessageId is required")
	}

	accessSince, err := s.getAccessSince(c, account, roomID)
	if err != nil {
		return nil, err
	}

	// Parent lookup (Cassandra) and room-times resolve (Mongo) have no
	// dependency on each other; fan them out so the worst-case pre-fetch
	// latency is one RTT instead of two. We capture each side's error
	// separately rather than letting errgroup return whichever-fires-first,
	// so input-validation 400s derived from the parent (reply ID, empty
	// ThreadRoomID, TCount explicitly 0) take precedence over a transient
	// Mongo error from the room-times read.
	now := time.Now().UTC()
	var (
		msg       *models.Message
		findErr   error
		createdAt time.Time
		rtErr     error
	)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		msg, findErr = s.findMessage(c, roomID, req.ThreadMessageID)
	}()
	go func() {
		defer wg.Done()
		_, createdAt, rtErr = s.resolveRoomTimesOrError(c, roomID, req.Meta, now)
	}()
	wg.Wait()

	if findErr != nil {
		return nil, findErr
	}

	if msg.ThreadParentID != "" {
		return nil, errcode.BadRequest("threadMessageId must be a top-level message, not a reply")
	}

	if accessSince != nil && msg.CreatedAt.Before(*accessSince) {
		return nil, errcode.Forbidden("thread is outside access window", errcode.WithReason(errcode.MessageOutsideAccessWindow))
	}

	// Empty ThreadRoomID means no replies yet or a silently-failed stamp in message-worker.
	if msg.ThreadRoomID == "" {
		slog.Warn("thread fetch: parent has empty thread_room_id, returning no replies",
			"request_id", natsutil.RequestIDFromContext(c),
			"room_id", roomID,
			"messageID", req.ThreadMessageID,
			"messageCreatedAt", msg.CreatedAt,
			"account", account,
		)
		return emptyThreadResponse(), nil
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

	// tcount explicitly 0 means all replies have been deleted — skip the
	// Cassandra round-trip. tcount == nil means the column was never written:
	// commonly a brand-new parent with no replies yet, but also briefly true
	// between a successful SaveThreadMessage INSERT and the follow-up
	// incrementParentTcount LWT. Fall through to Cassandra in the nil case so
	// the optimisation can't hide replies during that window.
	if msg.TCount != nil && *msg.TCount == 0 {
		return emptyThreadResponse(), nil
	}

	// Room-times error only matters once we're committed to the Cassandra
	// read — short-circuit paths above don't depend on the result.
	if rtErr != nil {
		return nil, rtErr
	}

	// Ceiling: server clock + skew tolerance, deliberately NOT the room's
	// lastMsgAt. Thread replies never bump rooms.lastMsgAt (broadcast-worker
	// skips it for thread fan-out), so a lastMsgAt-derived ceiling hides every
	// reply newer than the room's last main-channel message. The thread table
	// is one partition per thread — there is no bucket walk for a tight
	// ceiling to bound — so this only guards against future-dated rows.
	ceiling := now.Add(clockSkewTolerance)

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

	page, err := s.msgReader.GetThreadMessages(c, msg.ThreadRoomID, ceiling, floor, pageReq)
	if err != nil {
		return nil, fmt.Errorf("loading thread messages: %w", err)
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
		return "", errcode.BadRequest(fmt.Sprintf("invalid thread filter: %q", filter))
	}
}

// GetThreadParentMessages handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.thread.parent.
func (s *HistoryService) GetThreadParentMessages(c *natsrouter.Context, req models.GetThreadParentMessagesRequest) (*models.GetThreadParentMessagesResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")
	c.WithLogValues("account", account, "room_id", roomID)

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
		return nil, errcode.Internal("unhandled thread filter",
			errcode.WithCause(fmt.Errorf("unhandled thread filter: %q", filter)))
	}
	if err != nil {
		return nil, fmt.Errorf("loading thread rooms (filter %s): %w", filter, err)
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
		return nil, fmt.Errorf("hydrating thread parent messages: %w", err)
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
