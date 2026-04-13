package main

import (
	"strings"

	"github.com/hmchangw/chat/pkg/model"
)

func HasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

func sanitizeError(err error) string {
	msg := err.Error()
	userFacingPrefixes := []string{
		"invalid", "only owners", "cannot demote",
		"requester not found", "target user", "role update", "user already",
	}
	for _, pfx := range userFacingPrefixes {
		if strings.HasPrefix(msg, pfx) {
			return msg
		}
	}
	return "internal error"
}
