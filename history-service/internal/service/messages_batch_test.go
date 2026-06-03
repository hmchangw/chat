package service_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service"
)

// --- GetMessagesByIDs ---

func TestHistoryService_GetMessagesByIDs_Success(t *testing.T) {
	svc, msgs, subs, _, _ := newService(t)
	c := testContext()

	createdAt := joinTime.Add(1 * time.Minute)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(&joinTime, true, nil)
	repoMsgs := []models.Message{
		{MessageID: "m1", RoomID: "r1", CreatedAt: createdAt},
		{MessageID: "m2", RoomID: "r1", CreatedAt: createdAt.Add(time.Minute)},
	}
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"m1", "m2"}).Return(repoMsgs, nil)

	resp, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"m1", "m2"}})
	require.NoError(t, err)
	require.Len(t, resp.Messages, 2)
	assert.Equal(t, "m1", resp.Messages[0].MessageID)
	assert.Equal(t, "m2", resp.Messages[1].MessageID)
}

func TestHistoryService_GetMessagesByIDs_Empty(t *testing.T) {
	svc, _, _, _, _ := newService(t)
	c := testContext()

	_, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: nil})
	require.Error(t, err)
	assertBadRequestErr(t, err, "messageIds must not be empty")
}

func TestHistoryService_GetMessagesByIDs_TooMany(t *testing.T) {
	svc, _, _, _, _ := newService(t)
	c := testContext()

	ids := make([]string, 201)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%d", i)
	}

	_, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: ids})
	require.Error(t, err)
	assertBadRequestErr(t, err, "too many messageIds requested")
}

func TestHistoryService_GetMessagesByIDs_MissingOmitted(t *testing.T) {
	svc, msgs, subs, _, _ := newService(t)
	c := testContext()

	createdAt := joinTime.Add(1 * time.Minute)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(&joinTime, true, nil)
	// repo omits the missing ID "m2" already; we only assert survivors pass through.
	repoMsgs := []models.Message{
		{MessageID: "m1", RoomID: "r1", CreatedAt: createdAt},
	}
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"m1", "m2"}).Return(repoMsgs, nil)

	resp, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"m1", "m2"}})
	require.NoError(t, err)
	require.Len(t, resp.Messages, 1)
	assert.Equal(t, "m1", resp.Messages[0].MessageID)
}

func TestHistoryService_GetMessagesByIDs_OutsideAccessWindowDropped(t *testing.T) {
	svc, msgs, subs, _, _ := newService(t)
	c := testContext()

	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(&joinTime, true, nil)
	repoMsgs := []models.Message{
		// before the access window -> must be dropped, not errored.
		{MessageID: "old", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Hour)},
		// inside the window -> kept.
		{MessageID: "new", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Hour)},
	}
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"old", "new"}).Return(repoMsgs, nil)

	resp, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"old", "new"}})
	require.NoError(t, err)
	require.Len(t, resp.Messages, 1)
	assert.Equal(t, "new", resp.Messages[0].MessageID)
}

func TestHistoryService_GetMessagesByIDs_NotSubscribedRoomDropped(t *testing.T) {
	svc, msgs, subs, _, _ := newService(t)
	c := testContext()

	createdAt := joinTime.Add(1 * time.Minute)
	// r1: subscribed; r2: not subscribed -> its messages dropped.
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(&joinTime, true, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r2").Return(nil, false, nil)
	repoMsgs := []models.Message{
		{MessageID: "m1", RoomID: "r1", CreatedAt: createdAt},
		{MessageID: "m2", RoomID: "r2", CreatedAt: createdAt},
	}
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"m1", "m2"}).Return(repoMsgs, nil)

	resp, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"m1", "m2"}})
	require.NoError(t, err)
	require.Len(t, resp.Messages, 1)
	assert.Equal(t, "m1", resp.Messages[0].MessageID)
}

func TestHistoryService_GetMessagesByIDs_SubscriptionCheckCachedPerRoom(t *testing.T) {
	svc, msgs, subs, _, _ := newService(t)
	c := testContext()

	createdAt := joinTime.Add(1 * time.Minute)
	// Two messages in r1: subscription must be checked exactly once.
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(&joinTime, true, nil).Times(1)
	repoMsgs := []models.Message{
		{MessageID: "m1", RoomID: "r1", CreatedAt: createdAt},
		{MessageID: "m2", RoomID: "r1", CreatedAt: createdAt.Add(time.Minute)},
	}
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"m1", "m2"}).Return(repoMsgs, nil)

	resp, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"m1", "m2"}})
	require.NoError(t, err)
	require.Len(t, resp.Messages, 2)
}

func TestHistoryService_GetMessagesByIDs_SubscriptionStoreError(t *testing.T) {
	svc, msgs, subs, _, _ := newService(t)
	c := testContext()

	createdAt := joinTime.Add(1 * time.Minute)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, false, fmt.Errorf("db error"))
	repoMsgs := []models.Message{
		{MessageID: "m1", RoomID: "r1", CreatedAt: createdAt},
	}
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"m1"}).Return(repoMsgs, nil)

	_, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"m1"}})
	require.Error(t, err)
	assertInternalErr(t, err, "verifying room access")
}

func TestHistoryService_GetMessagesByIDs_RepoError(t *testing.T) {
	svc, msgs, _, _, _ := newService(t)
	c := testContext()

	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"m1"}).Return(nil, fmt.Errorf("db error"))

	_, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"m1"}})
	require.Error(t, err)
	assertInternalErr(t, err, "batch fetching messages")
}

func TestHistoryService_GetMessagesByIDs_RedactsInaccessibleQuote(t *testing.T) {
	svc, msgs, subs, _, _ := newService(t)
	c := testContext()

	createdAt := joinTime.Add(1 * time.Hour)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(&joinTime, true, nil)
	// Quoted parent predates the access window -> must be redacted, message kept.
	repoMsgs := []models.Message{
		{
			MessageID: "m1", RoomID: "r1", CreatedAt: createdAt,
			QuotedParentMessage: &models.QuotedParentMessage{
				MessageID: "qp", RoomID: "r1", Msg: "secret",
				CreatedAt: joinTime.Add(-2 * time.Hour),
			},
		},
	}
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), []string{"m1"}).Return(repoMsgs, nil)

	resp, err := svc.GetMessagesByIDs(c, models.GetMessagesByIDsRequest{MessageIDs: []string{"m1"}})
	require.NoError(t, err)
	require.Len(t, resp.Messages, 1)
	require.NotNil(t, resp.Messages[0].QuotedParentMessage)
	assert.Equal(t, service.UnavailableQuoteMsg, resp.Messages[0].QuotedParentMessage.Msg)
}
