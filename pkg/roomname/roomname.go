// Package roomname provides shared helpers for composing and normalizing
// room names and account lists during create-room / add-members flows.
//
// These helpers were previously duplicated verbatim between room-service and
// room-worker; centralizing them prevents the two services from drifting on
// validation rules (truncation length, bot pattern, dedup ordering, etc.).
package roomname

import (
	"strings"
	"unicode/utf8"

	"github.com/hmchangw/chat/pkg/model"
)

// MaxNameRunes is the maximum number of Unicode code points allowed in a
// persisted room name. Names longer than this are silently truncated rather
// than rejected so legacy clients keep working.
const MaxNameRunes = 100

// TruncateRunes truncates s to at most n Unicode code points.
func TruncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	out := make([]rune, 0, n)
	for _, r := range s {
		out = append(out, r)
		if len(out) == n {
			break
		}
	}
	return string(out)
}

// StripAccount returns a new slice with all occurrences of account removed.
// Order is preserved.
func StripAccount(slice []string, account string) []string {
	out := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != account {
			out = append(out, s)
		}
	}
	return out
}

// ComposeAutoName joins users, orgs, and channel room IDs with ", " and
// truncates the result to MaxNameRunes for use as an auto-generated room name.
func ComposeAutoName(users, orgs []string, channels []model.ChannelRef) string {
	parts := make([]string, 0, len(users)+len(orgs)+len(channels))
	parts = append(parts, users...)
	parts = append(parts, orgs...)
	for _, c := range channels {
		parts = append(parts, c.RoomID)
	}
	joined := strings.Join(parts, ", ")
	return TruncateRunes(joined, MaxNameRunes)
}
