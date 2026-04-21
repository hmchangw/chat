package main

import (
	"errors"
	"regexp"
	"strings"

	"github.com/hmchangw/chat/pkg/model"
)

// Sentinel errors for user-facing validation failures.
var (
	errInvalidRole      = errors.New("invalid role: must be owner or member")
	errOnlyOwners       = errors.New("only owners can update roles")
	errAlreadyOwner     = errors.New("user is already an owner")
	errNotOwner         = errors.New("user is not an owner")
	errCannotDemoteLast = errors.New("cannot demote the last owner")
	errRoomTypeGuard    = errors.New("role update is only allowed in group rooms")
	errTargetNotMember  = errors.New("target user is not a member of this room")
	errNotRoomMember    = errors.New("only room members can list members")
	errInvalidOrg       = errors.New("invalid org")
)

var botPattern = regexp.MustCompile(`\.bot$|^p_`)

func hasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

func isBot(account string) bool { return botPattern.MatchString(account) }

func filterBots(accounts []string) []string {
	var filtered []string
	for _, a := range accounts {
		if !isBot(a) {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func dedup(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	var result []string
	for _, item := range items {
		if _, ok := seen[item]; !ok {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func sanitizeError(err error) string {
	switch {
	case errors.Is(err, errInvalidRole),
		errors.Is(err, errOnlyOwners),
		errors.Is(err, errAlreadyOwner),
		errors.Is(err, errNotOwner),
		errors.Is(err, errCannotDemoteLast),
		errors.Is(err, errRoomTypeGuard),
		errors.Is(err, errTargetNotMember),
		errors.Is(err, errNotRoomMember),
		errors.Is(err, errInvalidOrg):
		return err.Error()
	default:
		msg := err.Error()
		for _, safe := range []string{"only owners can", "cannot add members", "room is at maximum capacity", "requester not in room", "invalid request"} {
			if strings.Contains(msg, safe) {
				return msg
			}
		}
		return "internal error"
	}
}
