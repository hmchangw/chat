package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/msgraph"
)

// fakeGraphClient is a hand-rolled msgraph.Client double. It records calls and
// returns a canned meeting or error. callCount lets idempotency tests assert
// that a second meetings call does NOT reach Graph.
type fakeGraphClient struct {
	meeting   *msgraph.OnlineMeeting
	err       error
	callCount int
	lastReq   msgraph.CreateOnlineMeetingRequest
}

func (f *fakeGraphClient) CreateOnlineMeeting(_ context.Context, req msgraph.CreateOnlineMeetingRequest) (*msgraph.OnlineMeeting, error) {
	f.callCount++
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.meeting, nil
}

// stubMeetMarkerReader is a hand-rolled MeetMarkerReader double.
type stubMeetMarkerReader struct {
	marker *model.TeamsMeetStartedSysData
	found  bool
	err    error
}

func (s *stubMeetMarkerReader) GetLastTeamsMeetStarted(_ context.Context, _ string) (*model.TeamsMeetStartedSysData, bool, error) {
	return s.marker, s.found, s.err
}

func indMember(account string) model.RoomMember {
	return model.RoomMember{
		Member: model.RoomMemberEntry{Type: model.RoomMemberIndividual, Account: account},
	}
}

func orgMember(id string) model.RoomMember {
	return model.RoomMember{Member: model.RoomMemberEntry{Type: model.RoomMemberOrg, ID: id}}
}

// --- calls/room (teamsRoomCall) ---

func TestTeamsRoomCall_Success_ExcludesSelf(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeChannel}, nil)
	store.EXPECT().ListRoomMembers(gomock.Any(), "r1", nil, nil, false).
		Return([]model.RoomMember{indMember("alice"), indMember("bob"), orgMember("orgX"), indMember("carol")}, nil)

	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersCallLimit: 20}

	resp, err := h.teamsRoomCall(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsRoomCallRequest{})
	require.NoError(t, err)

	// alice (self) and the org entry are excluded; bob + carol remain, order preserved.
	users := parseUsersParam(t, resp.JoinURL)
	assert.Equal(t, []string{"bob@corp.com", "carol@corp.com"}, users)
	assert.True(t, strings.HasPrefix(resp.JoinURL, "https://teams.microsoft.com/l/call/0/0?"))
}

func TestTeamsRoomCall_RequesterMissing(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersCallLimit: 20}
	_, err := h.teamsRoomCall(ctxParams(map[string]string{"account": "", "roomID": "r1"}), model.TeamsRoomCallRequest{})
	require.ErrorIs(t, err, errTeamsRequesterMissing)
}

func TestTeamsRoomCall_RoomIDMissing(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersCallLimit: 20}
	_, err := h.teamsRoomCall(ctxParams(map[string]string{"account": "alice", "roomID": ""}), model.TeamsRoomCallRequest{})
	require.ErrorIs(t, err, errTeamsRoomIDRequired)
}

func TestTeamsRoomCall_NotMember(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
		Return(nil, model.ErrSubscriptionNotFound)
	store.EXPECT().GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)

	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersCallLimit: 20}
	_, err := h.teamsRoomCall(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsRoomCallRequest{})
	require.ErrorIs(t, err, errNotRoomMember)
}

func TestTeamsRoomCall_NoOtherMembers(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)
	store.EXPECT().ListRoomMembers(gomock.Any(), "r1", nil, nil, false).
		Return([]model.RoomMember{indMember("alice")}, nil)

	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersCallLimit: 20}
	_, err := h.teamsRoomCall(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsRoomCallRequest{})
	require.ErrorIs(t, err, errTeamsNoCallableMembers)
	assert.Equal(t, errcode.RoomTargetNotMember, errcode.ReasonOf(err))
}

func TestTeamsRoomCall_TooManyMembers(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)

	members := []model.RoomMember{indMember("alice")}
	for i := 0; i < 3; i++ {
		members = append(members, indMember(string(rune('a'+i))+"x"))
	}
	store.EXPECT().ListRoomMembers(gomock.Any(), "r1", nil, nil, false).Return(members, nil)

	// CallLimit=2, but 3 other members → over the limit.
	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersCallLimit: 2}
	_, err := h.teamsRoomCall(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsRoomCallRequest{})
	require.ErrorIs(t, err, errTeamsCallTooManyMembers)
	assert.Equal(t, errcode.RoomMaxSizeReached, errcode.ReasonOf(err))
}

// --- calls/user (teamsUserCall) ---

func TestTeamsUserCall_Success(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com"}
	resp, err := h.teamsUserCall(ctxParams(map[string]string{"account": "alice"}), model.TeamsUserCallRequest{AccountName: "bob"})
	require.NoError(t, err)
	users := parseUsersParam(t, resp.JoinURL)
	assert.Equal(t, []string{"bob@corp.com"}, users)
}

func TestTeamsUserCall_RequesterMissing(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com"}
	_, err := h.teamsUserCall(ctxParams(map[string]string{"account": ""}), model.TeamsUserCallRequest{AccountName: "bob"})
	require.ErrorIs(t, err, errTeamsRequesterMissing)
}

func TestTeamsUserCall_AccountNameRequired(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com"}
	_, err := h.teamsUserCall(ctxParams(map[string]string{"account": "alice"}), model.TeamsUserCallRequest{AccountName: ""})
	require.ErrorIs(t, err, errTeamsAccountRequired)
}

// --- meetings (teamsMeeting) ---

func TestTeamsMeeting_CreatesAndPublishes(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Name: "general", Type: model.RoomTypeChannel}, nil)
	store.EXPECT().ListRoomMembers(gomock.Any(), "r1", nil, nil, false).
		Return([]model.RoomMember{indMember("alice"), indMember("bob")}, nil)

	graph := &fakeGraphClient{meeting: &msgraph.OnlineMeeting{ID: "mtg-1", JoinURL: "https://teams.example/join/1"}}

	var publishedSubj string
	var publishedData []byte
	var publishedMsgID string
	h := &Handler{
		store:            store,
		siteID:           "site-a",
		teamsEmailDomain: "corp.com",
		roomMembersLimit: 500,
		graphClient:      graph,
		meetMarkerReader: &stubMeetMarkerReader{found: false},
		publishToStream: func(_ context.Context, subj string, data []byte, msgID string) error {
			publishedSubj, publishedData, publishedMsgID = subj, data, msgID
			return nil
		},
	}

	resp, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.NoError(t, err)
	assert.Equal(t, "mtg-1", resp.ID)
	assert.Equal(t, "https://teams.example/join/1", resp.JoinURL)

	// Graph called exactly once; organizer + attendees derived from accounts.
	assert.Equal(t, 1, graph.callCount)
	assert.Equal(t, "alice@corp.com", graph.lastReq.OrganizerEmail)
	assert.ElementsMatch(t, []string{"alice@corp.com", "bob@corp.com"}, graph.lastReq.AttendeeEmails)

	// teams_meet_started published through the canonical message path.
	require.NotEmpty(t, publishedData, "expected a canonical message publish")
	assert.NotEmpty(t, publishedMsgID, "expected a dedup msg ID")
	assert.Contains(t, publishedSubj, "chat.msg.canonical.site-a.created")

	var evt model.MessageEvent
	require.NoError(t, json.Unmarshal(publishedData, &evt))
	assert.Equal(t, model.MessageTypeTeamsMeetStarted, evt.Message.Type)
	var sys model.TeamsMeetStartedSysData
	require.NoError(t, json.Unmarshal(evt.Message.SysMsgData, &sys))
	assert.Equal(t, "mtg-1", sys.MeetingID)
	assert.Equal(t, "https://teams.example/join/1", sys.JoinURL)
}

func TestTeamsMeeting_Idempotent_ReturnsExisting(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)
	// ListRoomMembers must NOT be called when a marker already exists.

	graph := &fakeGraphClient{meeting: &msgraph.OnlineMeeting{ID: "should-not-be-used"}}
	h := &Handler{
		store:            store,
		siteID:           "site-a",
		teamsEmailDomain: "corp.com",
		roomMembersLimit: 500,
		graphClient:      graph,
		meetMarkerReader: &stubMeetMarkerReader{
			found:  true,
			marker: &model.TeamsMeetStartedSysData{MeetingID: "mtg-existing", JoinURL: "https://teams.example/join/existing"},
		},
		publishToStream: func(_ context.Context, _ string, _ []byte, _ string) error {
			t.Error("idempotent path must not publish a new system message")
			return nil
		},
	}

	resp, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.NoError(t, err)
	assert.Equal(t, "mtg-existing", resp.ID)
	assert.Equal(t, "https://teams.example/join/existing", resp.JoinURL)
	assert.Equal(t, 0, graph.callCount, "no duplicate Graph create on the idempotent path")
}

func TestTeamsMeeting_NotConfigured(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersLimit: 500} // graphClient + meetMarkerReader nil
	_, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.ErrorIs(t, err, errTeamsNotConfigured)
}

func TestTeamsMeeting_RequesterMissing(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersLimit: 500,
		graphClient: &fakeGraphClient{}, meetMarkerReader: &stubMeetMarkerReader{}}
	_, err := h.teamsMeeting(ctxParams(map[string]string{"account": "", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.ErrorIs(t, err, errTeamsRequesterMissing)
}

func TestTeamsMeeting_RoomIDMissing(t *testing.T) {
	h := &Handler{siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersLimit: 500,
		graphClient: &fakeGraphClient{}, meetMarkerReader: &stubMeetMarkerReader{}}
	_, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": ""}), model.TeamsMeetingRequest{})
	require.ErrorIs(t, err, errTeamsRoomIDRequired)
}

func TestTeamsMeeting_NotMember(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, model.ErrSubscriptionNotFound)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)

	graph := &fakeGraphClient{}
	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersLimit: 500,
		graphClient: graph, meetMarkerReader: &stubMeetMarkerReader{found: false}}
	_, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.ErrorIs(t, err, errNotRoomMember)
	assert.Equal(t, 0, graph.callCount)
}

func TestTeamsMeeting_TooManyMembers(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)
	store.EXPECT().ListRoomMembers(gomock.Any(), "r1", nil, nil, false).
		Return([]model.RoomMember{indMember("alice"), indMember("bob"), indMember("carol")}, nil)

	graph := &fakeGraphClient{}
	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersLimit: 2,
		graphClient: graph, meetMarkerReader: &stubMeetMarkerReader{found: false}}
	_, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.ErrorIs(t, err, errTeamsMeetingTooManyMembers)
	assert.Equal(t, errcode.RoomMaxSizeReached, errcode.ReasonOf(err))
	assert.Equal(t, 0, graph.callCount, "limit gate must short-circuit before Graph")
}

func TestTeamsMeeting_GraphCreateFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)
	store.EXPECT().ListRoomMembers(gomock.Any(), "r1", nil, nil, false).
		Return([]model.RoomMember{indMember("alice")}, nil)

	graph := &fakeGraphClient{err: errors.New("graph 500")}
	var published bool
	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersLimit: 500,
		graphClient: graph, meetMarkerReader: &stubMeetMarkerReader{found: false},
		publishToStream: func(_ context.Context, _ string, _ []byte, _ string) error { published = true; return nil },
	}
	_, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.Error(t, err)
	assert.False(t, published, "no system message on Graph failure")
}

func TestTeamsMeeting_MarkerReadFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(&model.Subscription{RoomID: "r1"}, nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Type: model.RoomTypeChannel}, nil)

	graph := &fakeGraphClient{}
	h := &Handler{store: store, siteID: "site-a", teamsEmailDomain: "corp.com", roomMembersLimit: 500,
		graphClient: graph, meetMarkerReader: &stubMeetMarkerReader{err: errors.New("cass down")}}
	_, err := h.teamsMeeting(ctxParams(map[string]string{"account": "alice", "roomID": "r1"}), model.TeamsMeetingRequest{})
	require.Error(t, err)
	assert.Equal(t, 0, graph.callCount)
}

// parseUsersParam extracts the comma-separated `users` query param from a Teams
// deep link and returns the individual entries.
func parseUsersParam(t *testing.T, link string) []string {
	t.Helper()
	u, err := url.Parse(link)
	require.NoError(t, err)
	raw := u.Query().Get("users")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}
