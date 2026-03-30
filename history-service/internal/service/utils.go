package service

import (
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, natsrouter.ErrWithCode("bad_request", "invalid timestamp format")
	}
	return t, nil
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
