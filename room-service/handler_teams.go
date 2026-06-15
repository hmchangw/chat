package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/displayfmt"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/msgraph"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// teamsDeepLinkBase is the Teams 1:1/group call deep-link prefix. The callable
// users are appended as a comma-joined `users` query param.
const teamsDeepLinkBase = "https://teams.microsoft.com/l/call/0/0"

// teamsEmail derives a member's email from their account using the configured
// corporate domain — the only identity available at the NATS layer (the OIDC
// email claim exists only at auth-service token exchange). Mirrors the dev
// auth-service derivation (account + "@dev.local").
func teamsEmail(account, domain string) string {
	return account + "@" + domain
}

// buildTeamsCallDeepLink builds a Teams call deep link for the given attendee
// emails. Order is preserved; the caller is responsible for excluding self.
func buildTeamsCallDeepLink(emails []string) string {
	v := url.Values{}
	v.Set("users", strings.Join(emails, ","))
	return teamsDeepLinkBase + "?" + v.Encode()
}

// teamsRoomCall builds a Teams deep link for a call to every other member of the
// room (self excluded). No Microsoft Graph call — pure string building from the
// member list. Enforces roomMembersCallLimit.
func (h *Handler) teamsRoomCall(c *natsrouter.Context, _ model.TeamsRoomCallRequest) (*model.TeamsCallReply, error) { //nolint:gocritic // hugeParam: req passed by value to satisfy natsrouter.Register
	var ctx context.Context = c
	requesterAccount := c.Param("account")
	roomID := c.Param("roomID")

	if requesterAccount == "" {
		return nil, errTeamsRequesterMissing
	}
	if roomID == "" {
		return nil, errTeamsRoomIDRequired
	}

	if _, err := h.requireMembershipAndGetRoom(ctx, requesterAccount, roomID); err != nil {
		return nil, err
	}

	members, err := h.store.ListRoomMembers(ctx, roomID, nil, nil, false)
	if err != nil {
		return nil, fmt.Errorf("list room members: %w", err)
	}

	emails := membersToCallEmails(members, requesterAccount, h.teamsEmailDomain)
	if len(emails) == 0 {
		return nil, errTeamsNoCallableMembers
	}
	if len(emails) > h.roomMembersCallLimit {
		return nil, errTeamsCallTooManyMembers
	}

	return &model.TeamsCallReply{JoinURL: buildTeamsCallDeepLink(emails)}, nil
}

// teamsUserCall builds a Teams 1:1 call deep link for the target account. No
// Graph call. The target email is account@domain (same derivation as everywhere
// else in this integration).
func (h *Handler) teamsUserCall(c *natsrouter.Context, req model.TeamsUserCallRequest) (*model.TeamsCallReply, error) { //nolint:gocritic // hugeParam: req passed by value to satisfy natsrouter.Register
	requesterAccount := c.Param("account")
	if requesterAccount == "" {
		return nil, errTeamsRequesterMissing
	}
	if req.AccountName == "" {
		return nil, errTeamsAccountRequired
	}

	email := teamsEmail(req.AccountName, h.teamsEmailDomain)
	return &model.TeamsCallReply{JoinURL: buildTeamsCallDeepLink([]string{email})}, nil
}

// teamsMeeting creates (or returns the existing) Microsoft Teams onlineMeeting
// for the room. Idempotent per room: the last teams_meet_started system message
// is the source of truth, so a second call returns the prior meeting without a
// duplicate Graph create. Enforces roomMembersLimit.
func (h *Handler) teamsMeeting(c *natsrouter.Context, _ model.TeamsMeetingRequest) (*model.TeamsMeetingReply, error) { //nolint:gocritic // hugeParam: req passed by value to satisfy natsrouter.Register
	var ctx context.Context = c
	requestID := natsutil.RequestIDFromContext(c)
	requesterAccount := c.Param("account")
	roomID := c.Param("roomID")

	if requesterAccount == "" {
		return nil, errTeamsRequesterMissing
	}
	if roomID == "" {
		return nil, errTeamsRoomIDRequired
	}
	if h.graphClient == nil || h.meetMarkerReader == nil {
		return nil, errTeamsNotConfigured
	}

	room, err := h.requireMembershipAndGetRoom(ctx, requesterAccount, roomID)
	if err != nil {
		return nil, err
	}

	// Idempotency: a prior teams_meet_started marker short-circuits the Graph
	// create so retries (and most concurrent clients) reuse the same meeting.
	//
	// Known limitation (read-then-create race): the marker is written via the
	// canonical message path, which is asynchronous, so two callers racing
	// before the first marker materializes can each create a Graph meeting. This
	// mirrors the previous GetLastMeetStartedMessage behavior — there is no CAS/
	// LWT lock primitive in this layer to make the create atomic, and adding one
	// is a larger design decision left to the maintainer. The marker makes the
	// endpoint eventually-idempotent: once a marker is visible, all subsequent
	// calls reuse it. The blast radius of the race is a duplicate Teams meeting,
	// not data corruption.
	marker, found, err := h.meetMarkerReader.GetLastTeamsMeetStarted(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("read meeting marker: %w", err)
	}
	if found && marker != nil && marker.JoinURL != "" {
		return &model.TeamsMeetingReply{ID: marker.MeetingID, JoinURL: marker.JoinURL}, nil
	}

	members, err := h.store.ListRoomMembers(ctx, roomID, nil, nil, false)
	if err != nil {
		return nil, fmt.Errorf("list room members: %w", err)
	}
	if countIndividualMembers(members) > h.roomMembersLimit {
		return nil, errTeamsMeetingTooManyMembers
	}

	attendeeEmails := membersToAttendeeEmails(members, h.teamsEmailDomain)
	organizerEmail := teamsEmail(requesterAccount, h.teamsEmailDomain)

	meeting, err := h.graphClient.CreateOnlineMeeting(ctx, msgraph.CreateOnlineMeetingRequest{
		Subject:        meetingSubject(room),
		OrganizerEmail: organizerEmail,
		AttendeeEmails: attendeeEmails,
	})
	if err != nil {
		return nil, fmt.Errorf("create online meeting: %w", err)
	}

	if err := h.publishTeamsMeetStarted(ctx, requestID, roomID, requesterAccount, meeting); err != nil {
		return nil, err
	}

	return &model.TeamsMeetingReply{ID: meeting.ID, JoinURL: meeting.JoinURL}, nil
}

// publishTeamsMeetStarted writes the teams_meet_started system message through
// the canonical message path — the same flow room_restricted uses — so it is
// persisted by message-worker and fanned out to room members. The marker is the
// idempotency source for subsequent meetings calls.
func (h *Handler) publishTeamsMeetStarted(
	ctx context.Context,
	requestID, roomID, byAccount string,
	meeting *msgraph.OnlineMeeting,
) error {
	sysData, err := json.Marshal(model.TeamsMeetStartedSysData{
		MeetingID: meeting.ID,
		JoinURL:   meeting.JoinURL,
	})
	if err != nil {
		return fmt.Errorf("marshal teams_meet_started sys data: %w", err)
	}

	now := time.Now().UTC()
	sysMsg := model.Message{
		ID:          idgen.MessageIDFromRequestID(requestID, "teams_meet_started"),
		RoomID:      roomID,
		UserAccount: byAccount,
		Type:        model.MessageTypeTeamsMeetStarted,
		Content:     "started a Teams meeting",
		SysMsgData:  sysData,
		CreatedAt:   now,
	}
	msgEvt := model.MessageEvent{
		Event:     model.EventCreated,
		Message:   sysMsg,
		SiteID:    h.siteID,
		Timestamp: now.UnixMilli(),
	}
	msgEvtData, err := json.Marshal(msgEvt)
	if err != nil {
		return fmt.Errorf("marshal teams_meet_started message event: %w", err)
	}
	if err := h.publishToStream(ctx, subject.MsgCanonicalCreated(h.siteID), msgEvtData, natsutil.CanonicalDedupID(&msgEvt)); err != nil {
		return fmt.Errorf("publish teams_meet_started sys message: %w", err)
	}
	return nil
}

// membersToCallEmails returns deep-link emails for every individual member
// except self, preserving member order.
func membersToCallEmails(members []model.RoomMember, self, domain string) []string {
	out := make([]string, 0, len(members))
	for i := range members {
		entry := members[i].Member
		if entry.Type != model.RoomMemberIndividual || entry.Account == "" {
			continue
		}
		if entry.Account == self {
			continue
		}
		out = append(out, teamsEmail(entry.Account, domain))
	}
	return out
}

// membersToAttendeeEmails returns attendee emails for every individual member
// (organizer included is harmless; Graph dedups the organizer).
func membersToAttendeeEmails(members []model.RoomMember, domain string) []string {
	out := make([]string, 0, len(members))
	for i := range members {
		entry := members[i].Member
		if entry.Type != model.RoomMemberIndividual || entry.Account == "" {
			continue
		}
		out = append(out, teamsEmail(entry.Account, domain))
	}
	return out
}

// countIndividualMembers counts individual (human) members for the limit gate.
func countIndividualMembers(members []model.RoomMember) int {
	n := 0
	for i := range members {
		if members[i].Member.Type == model.RoomMemberIndividual {
			n++
		}
	}
	return n
}

// meetingSubject builds a human-friendly meeting title from the room.
func meetingSubject(room *model.Room) string {
	name := displayfmt.CombineWithFallback("", "", room.Name)
	if name == "" {
		return "Chat meeting"
	}
	return name
}
