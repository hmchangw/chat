package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// getAccessSince verifies the user is subscribed to the room and returns the
// historySharedSince lower bound (nil = full access).
func (s *HistoryService) getAccessSince(ctx context.Context, username, roomID string) (*time.Time, error) {
	accessSince, subscribed, err := s.subscriptions.GetHistorySharedSince(ctx, username, roomID)
	if err != nil {
		return nil, fmt.Errorf("checking subscription: %w", err)
	}
	if !subscribed {
		return nil, natsrouter.ErrWithCode("forbidden", "not subscribed to room")
	}
	return accessSince, nil
}

// resolveRoomID returns the authoritative roomID by reconciling the subject
// param with the body value. Returns an error if both are present and differ.
func resolveRoomID(p natsrouter.Params, bodyRoomID string) (string, error) {
	subjectRoomID := p.Get("roomID")
	switch {
	case subjectRoomID != "" && bodyRoomID != "" && subjectRoomID != bodyRoomID:
		return "", natsrouter.ErrWithCode("bad_request", "roomId in body does not match subject")
	case subjectRoomID != "":
		return subjectRoomID, nil
	default:
		return bodyRoomID, nil
	}
}

// millisToTime converts a UTC millisecond timestamp to time.Time.
// Returns zero time if millis is 0 (meaning "not provided").
func millisToTime(millis int64) time.Time {
	if millis == 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

func parsePageRequest(cursor string, limit int) (cassrepo.PageRequest, error) {
	q, err := cassrepo.ParsePageRequest(cursor, limit)
	if err != nil {
		return cassrepo.PageRequest{}, natsrouter.ErrWithCode("bad_request", "invalid pagination cursor")
	}
	return q, nil
}

// derefTime safely dereferences a *time.Time, returning zero time if nil.
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// timeMax returns the later of two timestamps. Zero values are ignored.
func timeMax(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.After(b) {
		return a
	}
	return b
}
