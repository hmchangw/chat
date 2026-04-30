package main

import (
	"errors"
	"regexp"
	"strings"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomname"
)

// Sentinel errors for user-facing validation failures.
var (
	errInvalidRole      = errors.New("invalid role: must be owner or member")
	errOnlyOwners       = errors.New("only owners can update roles")
	errAlreadyOwner     = errors.New("user is already an owner")
	errNotOwner         = errors.New("user is not an owner")
	errCannotDemoteLast = errors.New("cannot demote the last owner")
	errRoomTypeGuard    = errors.New("role update is only allowed in channel rooms")
	errTargetNotMember  = errors.New("target user is not a member of this room")
	// Used by both list-members (requester subscription check) and add-member
	// channel-source expansion. Both contexts mean "the requester is not a
	// member of the room they are asking about".
	errNotRoomMember = errors.New("only room members can list members")
	errInvalidOrg    = errors.New("invalid org")
	// Only subscribers with an individual membership source can hold the owner
	// role. Remove-member's dual-membership path relies on this invariant:
	// stripping the owner role during an individual-leave is only sound when
	// the role can only be held alongside an individual entry.
	errPromoteRequiresIndividual = errors.New("only individual members can be promoted to owner")

	// Sentinels for create-room validation.
	errEmptyCreateRequest  = errors.New("request must include at least one of users, orgs, channels, or name")
	errSelfDM              = errors.New("cannot create a DM with yourself")
	errBotInChannel        = errors.New("bots cannot be added to a channel during creation")
	errBotNotAvailable     = errors.New("bot not available")
	errInvalidUserData     = errors.New("user is missing required name fields")
	errMissingRequestID    = errors.New("missing X-Request-ID header")
	errInvalidRequestID    = errors.New("invalid X-Request-ID format")
	errChannelNameRequired = errors.New("channel name is required")
	errUserNotFound        = errors.New("user not found")
)

var botPattern = regexp.MustCompile(`\.bot$|^p_`)

// hasRole checks if a given role is present in a slice of roles.
func hasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

// isBot returns true if an account name matches the bot naming pattern.
func isBot(account string) bool { return botPattern.MatchString(account) }

// filterBots removes bot accounts from a slice of account names.
func filterBots(accounts []string) []string {
	var filtered []string
	for _, a := range accounts {
		if !isBot(a) {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

// dedup removes duplicate strings from a slice while preserving order.
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

// determineRoomType inspects the post-strip request and decides what
// room type should be created. Empty payload is the caller's
// responsibility — this function assumes at least one of users / orgs /
// channels / name is non-empty.
func determineRoomType(req *model.CreateRoomRequest) model.RoomType {
	if req.Name == "" && len(req.Orgs) == 0 && len(req.Channels) == 0 && len(req.Users) == 1 {
		if strings.HasSuffix(req.Users[0], ".bot") {
			return model.RoomTypeBotDM
		}
		return model.RoomTypeDM
	}
	return model.RoomTypeChannel
}

// dmExistsError carries the existing DM/botDM room ID through the error
// chain so the natsCreateRoom handler can populate the special
// "dm already exists" reply with the existing roomId.
type dmExistsError struct{ existingRoomID string }

func newDMExistsError(roomID string) *dmExistsError {
	return &dmExistsError{existingRoomID: roomID}
}

func (e *dmExistsError) Error() string  { return "dm already exists" }
func (e *dmExistsError) RoomID() string { return e.existingRoomID }
func (e *dmExistsError) Is(target error) bool {
	_, ok := target.(*dmExistsError)
	return ok
}

// stripAccount and truncateRunes are thin wrappers that delegate to pkg/roomname so
// room-service and room-worker can never drift on these rules. composeAutoName was
// removed when channels became required-name (see spec §"Behaviour").
func stripAccount(slice []string, account string) []string {
	return roomname.StripAccount(slice, account)
}

func truncateRunes(s string, max int) string {
	return roomname.TruncateRunes(s, max)
}

// sanitizeError returns a user-safe error message for known error sentinels and approved patterns.
func sanitizeError(err error) string {
	switch {
	case errors.Is(err, errNotRoomMember):
		// Always return the sentinel message, even when wrapped (e.g. by
		// add-member's "expand channels: %w"), so callers get a clean
		// user-safe message without the wrapping context.
		return errNotRoomMember.Error()
	case errors.Is(err, errInvalidRole),
		errors.Is(err, errOnlyOwners),
		errors.Is(err, errAlreadyOwner),
		errors.Is(err, errNotOwner),
		errors.Is(err, errCannotDemoteLast),
		errors.Is(err, errRoomTypeGuard),
		errors.Is(err, errTargetNotMember),
		errors.Is(err, errInvalidOrg),
		errors.Is(err, errPromoteRequiresIndividual):
		return err.Error()
	case errors.Is(err, errEmptyCreateRequest),
		errors.Is(err, errSelfDM),
		errors.Is(err, errBotInChannel),
		errors.Is(err, errBotNotAvailable),
		errors.Is(err, errInvalidUserData),
		errors.Is(err, errMissingRequestID),
		errors.Is(err, errInvalidRequestID),
		errors.Is(err, errChannelNameRequired),
		errors.Is(err, errUserNotFound),
		errors.Is(err, &dmExistsError{}):
		return err.Error()
	default:
		msg := err.Error()
		for _, safe := range []string{"only owners can", "cannot add members", "room is at maximum capacity", "requester not in room", "invalid request", "remote member.list:"} {
			if strings.Contains(msg, safe) {
				return msg
			}
		}
		return "internal error"
	}
}
