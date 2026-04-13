package main

import (
	"strings"

	"github.com/hmchangw/chat/pkg/model"
)

// HasRole returns true if the given role is present in the roles slice.
func HasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

// isBot returns true for system/bot accounts that should be excluded from membership.
func isBot(username string) bool {
	return strings.HasSuffix(username, ".bot") || strings.HasPrefix(username, "p_")
}

// dedup returns a new slice with duplicates removed, preserving first-occurrence order.
func dedup(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// sanitizeError returns a user-safe error message.
// If the error message starts with a known user-facing prefix, it is returned as-is.
// Otherwise a generic message is returned to avoid leaking internal details.
func sanitizeError(err error) string {
	msg := err.Error()
	userFacingPrefixes := []string{
		"invalid",
		"only owners",
		"room is at maximum",
		"cannot demote",
		"federation users",
		"inviter not found",
		"requester not found",
		"ambiguous",
		"last owner cannot",
	}
	for _, pfx := range userFacingPrefixes {
		if strings.HasPrefix(msg, pfx) {
			return msg
		}
	}
	return "internal error"
}

// filterBots returns a new slice with bot usernames removed.
func filterBots(ss []string) []string {
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !isBot(s) {
			result = append(result, s)
		}
	}
	return result
}
