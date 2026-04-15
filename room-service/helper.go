package main

import (
	"errors"

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
)

func hasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

func sanitizeError(err error) string {
	switch {
	case errors.Is(err, errInvalidRole),
		errors.Is(err, errOnlyOwners),
		errors.Is(err, errAlreadyOwner),
		errors.Is(err, errNotOwner),
		errors.Is(err, errCannotDemoteLast),
		errors.Is(err, errRoomTypeGuard),
		errors.Is(err, errTargetNotMember):
		return err.Error()
	default:
		return "internal error"
	}
}
