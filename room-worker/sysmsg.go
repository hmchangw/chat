package main

import (
	"strings"

	"github.com/hmchangw/chat/pkg/model"
)

func displayName(u *model.User) string {
	if u.EngName == u.ChineseName {
		return strings.TrimSpace(u.EngName)
	}
	return strings.TrimSpace(u.EngName + " " + u.ChineseName)
}

// validateUserNames returns a permanent error when u lacks the EngName/
// ChineseName fields required to render system-message Content. role labels
// the caller's relationship to u ("user", "requester") in the error message.
func validateUserNames(u *model.User, role, roomID string) error {
	if u.EngName == "" || u.ChineseName == "" {
		return newPermanent("%s %s missing required name fields (room %s)", role, u.Account, roomID)
	}
	return nil
}

func formatAddedSingle(requester, added *model.User) string {
	return displayName(requester) + " added " + displayName(added) + " to the channel"
}

func formatAddedMulti(requester *model.User) string {
	return displayName(requester) + " added members to the channel"
}

func formatRemovedUser(user *model.User) string {
	return displayName(user) + " has been removed from the channel"
}

func formatRemovedOrg(sectName string) string {
	return sectName + " has been removed from the channel"
}

func formatLeft(user *model.User) string {
	return displayName(user) + " left the channel"
}
