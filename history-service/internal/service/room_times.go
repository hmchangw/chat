package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// clockSkewTolerance allows clients with mildly out-of-sync clocks to still
// have their LastMsgAt hint accepted. Anything further out is treated as
// suspicious and triggers a Mongo fallback.
const clockSkewTolerance = time.Hour

// minPlausibleEpoch rejects clearly-bogus millis (e.g. *ms == 0 → 1970-01-01)
// without imposing tight bounds on real-world clock skew. time.Time{}.IsZero()
// does NOT match time.UnixMilli(0) — the latter is unix epoch, a real time.
var minPlausibleEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// walkBounds derives the (ceiling, floor) bucket bounds used by ASC and
// surrounding-walk handlers from the resolved lastMsgAt/createdAt. Falls back
// to now+clockSkewTolerance for the ceiling and now-historyFloor for the floor
// when the resolved values are zero.
func (s *HistoryService) walkBounds(lastMsgAt, createdAt, now time.Time) (ceiling, floor time.Time) {
	ceiling = lastMsgAt
	if ceiling.IsZero() {
		ceiling = now.Add(clockSkewTolerance)
	}
	floor = createdAt
	if floor.IsZero() {
		floor = now.Add(-s.historyFloor)
	}
	return ceiling, floor
}

// resolveRoomTimes returns lastMsgAt and createdAt for roomID. Client-supplied
// hints are trusted after sanity checks; missing or invalid hints fall back to
// Mongo via the RoomTimeResolver. now is injected for deterministic testing.
func (s *HistoryService) resolveRoomTimes(
	ctx context.Context,
	roomID string,
	hints *models.RoomHints,
	now time.Time,
) (lastMsgAt, createdAt time.Time, err error) {
	var last, created *time.Time
	if hints != nil {
		last = sanitizeLastMsgAt(hints.LastMsgAt, now)
		created = sanitizeCreatedAt(hints.CreatedAt, now)
	}

	if last == nil || created == nil {
		l, c, gerr := s.roomTimes.GetRoomTimes(ctx, roomID)
		if gerr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("resolve room times for %s: %w", roomID, gerr)
		}
		if last == nil {
			last = &l
		}
		if created == nil {
			created = &c
		}
	}

	return *last, *created, nil
}

func sanitizeLastMsgAt(ms *int64, now time.Time) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	if t.Before(minPlausibleEpoch) {
		return nil
	}
	if t.After(now.Add(clockSkewTolerance)) {
		return nil
	}
	return &t
}

func sanitizeCreatedAt(ms *int64, now time.Time) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	if t.Before(minPlausibleEpoch) {
		return nil
	}
	if t.After(now) {
		return nil
	}
	return &t
}
