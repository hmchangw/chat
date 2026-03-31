package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/history-service/internal/service/mocks"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

var (
	joinTime   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testParams = natsrouter.NewParams(map[string]string{"username": "u1", "roomID": "r1"})
)

func newService(t *testing.T) (*service.HistoryService, *mocks.MockMessageRepository, *mocks.MockSubscriptionRepository) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	return service.New(msgs, subs), msgs, subs
}

func makePage(msgs []model.Message, hasNext bool) cassrepo.Page[model.Message] {
	nextCursor := ""
	if hasNext {
		nextCursor = "fake-next-cursor"
	}
	return cassrepo.Page[model.Message]{Data: msgs, NextCursor: nextCursor, HasNext: hasNext}
}

// --- LoadHistory ---

func TestHistoryService_LoadHistory_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	messages := make([]model.Message, 4)
	for i := range messages {
		messages[i] = model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1",
			CreatedAt: joinTime.Add(time.Duration(4-i) * time.Minute),
		}
	}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(makePage(messages, false), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 4)
	assert.Nil(t, resp.FirstUnread)
	assert.False(t, resp.HasNextUnread)
}

func TestHistoryService_LoadHistory_HasNext(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	messages := make([]model.Message, 3)
	for i := range messages {
		messages[i] = model.Message{ID: fmt.Sprintf("m%d", i), RoomID: "r1", CreatedAt: joinTime.Add(time.Duration(3-i) * time.Minute)}
	}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(makePage(messages, true), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 3)
}

func TestHistoryService_LoadHistory_LastSeenAfterOldest_NoFirstUnread(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	lastSeen := joinTime.Add(2 * time.Minute)
	// Messages newest-first: m3, m2, m1 — oldest is m1 at joinTime+1min
	pageMessages := []model.Message{
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
		{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(makePage(pageMessages, false), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{
		RoomID:   "r1",
		LastSeen: lastSeen.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	// lastSeen (2min) > oldestInPage (1min) — all loaded messages are seen, no unread query
	assert.Nil(t, resp.FirstUnread)
}

func TestHistoryService_LoadHistory_FirstUnread_WithDBQuery(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	// Messages newest-first: oldest is m5 at joinTime+5min
	pageMessages := []model.Message{
		{ID: "m8", RoomID: "r1", CreatedAt: joinTime.Add(8 * time.Minute)},
		{ID: "m7", RoomID: "r1", CreatedAt: joinTime.Add(7 * time.Minute)},
		{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(makePage(pageMessages, false), nil)

	// lastSeen (2min) < oldestInPage (5min) — unread exist before the loaded page
	// Unread query lower bound: max(accessSince=0min, lastSeen=2min) = 2min
	lastSeen := joinTime.Add(2 * time.Minute)
	firstUnreadMsg := model.Message{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)}
	msgs.EXPECT().GetMessagesBetweenAsc(ctx, "r1", lastSeen, pageMessages[2].CreatedAt, true, gomock.Any()).Return(makePage([]model.Message{firstUnreadMsg}, true), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{
		RoomID:   "r1",
		LastSeen: lastSeen.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.FirstUnread)
	assert.Equal(t, "m3", resp.FirstUnread.ID)
	assert.True(t, resp.HasNextUnread)
	assert.NotEmpty(t, resp.NextUnreadCursor)
}

func TestHistoryService_LoadHistory_FirstUnread_LastSeenBeforeHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	pageMessages := []model.Message{
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
		{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(makePage(pageMessages, false), nil)

	// lastSeen is BEFORE historySharedSince — after should be clamped to HSS
	lastSeen := joinTime.Add(-10 * time.Minute)
	msgs.EXPECT().GetMessagesBetweenAsc(ctx, "r1", joinTime, pageMessages[1].CreatedAt, true, gomock.Any()).Return(makePage(nil, false), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{
		RoomID:   "r1",
		LastSeen: lastSeen.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	assert.Nil(t, resp.FirstUnread) // no unread found in range
	assert.False(t, resp.HasNextUnread)
}

func TestHistoryService_LoadHistory_StoreError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(cassrepo.Page[model.Message]{}, fmt.Errorf("db down"))

	_, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading history")
}

func TestHistoryService_LoadHistory_InvalidBefore(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	_, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1", Before: "not-a-timestamp"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timestamp")
}

func TestHistoryService_LoadHistory_SubscriptionError(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, false, fmt.Errorf("db error"))

	_, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking subscription")
}

func TestHistoryService_LoadHistory_EmptyResult(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(makePage(nil, false), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Empty(t, resp.Messages)
}

func TestHistoryService_LoadHistory_NoHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// No HSS — full history access, no lower bound
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)

	messages := make([]model.Message, 3)
	for i := range messages {
		messages[i] = model.Message{ID: fmt.Sprintf("m%d", i), RoomID: "r1", CreatedAt: time.Now().Add(time.Duration(i) * time.Minute)}
	}
	// No lower bound — must use GetMessagesBefore, not GetMessagesBetweenDesc
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", gomock.Any(), gomock.Any()).Return(makePage(messages, false), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 3)
}

func TestHistoryService_LoadHistory_NoHSS_WithUnread(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// No HSS — unread query uses lastSeen as lower bound (MAX(nil, lastSeen) = lastSeen)
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pageMessages := []model.Message{
		{ID: "m8", RoomID: "r1", CreatedAt: base.Add(8 * time.Minute)},
		{ID: "m5", RoomID: "r1", CreatedAt: base.Add(5 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", gomock.Any(), gomock.Any()).Return(makePage(pageMessages, false), nil)

	// lastSeen < oldestInPage — unread query: GetMessagesBetweenAsc(lastSeen, oldest)
	lastSeen := base.Add(2 * time.Minute)
	firstUnread := model.Message{ID: "m3", RoomID: "r1", CreatedAt: base.Add(3 * time.Minute)}
	msgs.EXPECT().GetMessagesBetweenAsc(ctx, "r1", lastSeen, pageMessages[1].CreatedAt, true, gomock.Any()).Return(makePage([]model.Message{firstUnread}, false), nil)

	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{
		RoomID:   "r1",
		LastSeen: lastSeen.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.FirstUnread)
	assert.Equal(t, "m3", resp.FirstUnread.ID)
}

func TestHistoryService_LoadHistory_LastSeenEqualsOldest_NoUnreadQuery(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	// lastSeen exactly equals oldestInPage.CreatedAt — condition is strict Before, so no unread query fires
	pageMessages := []model.Message{
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
		{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, gomock.Any(), gomock.Any()).Return(makePage(pageMessages, false), nil)
	// No GetMessagesBetweenAsc call expected

	lastSeen := joinTime.Add(1 * time.Minute) // equal to oldestInPage.CreatedAt
	resp, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{
		RoomID:   "r1",
		LastSeen: lastSeen.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	assert.Nil(t, resp.FirstUnread)
}

// --- LoadNextMessages ---

func TestHistoryService_LoadNextMessages_BothAfterAndHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// Both after and HSS present — effective lower bound = max(after, HSS)
	// after (joinTime+1min) > HSS (joinTime), so effective = joinTime+1min
	afterTime := joinTime.Add(1 * time.Minute)
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	messages := []model.Message{
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", afterTime, gomock.Any()).Return(makePage(messages, false), nil)

	resp, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  afterTime.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 2)
	assert.False(t, resp.HasNext)
}

func TestHistoryService_LoadNextMessages_OnlyHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// No after in request, HSS present — effective lower bound = HSS, uses GetMessagesAfter
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", joinTime, gomock.Any()).Return(makePage(nil, false), nil)

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.NoError(t, err)
}

func TestHistoryService_LoadNextMessages_OnlyAfter(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// after present, HSS not found — effective lower bound = after
	afterTime := joinTime.Add(5 * time.Minute)
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", afterTime, gomock.Any()).Return(makePage(nil, false), nil)

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  afterTime.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
}

func TestHistoryService_LoadNextMessages_BothNil(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// Neither after nor HSS — no lower bound → GetAllMessagesAsc
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)
	msgs.EXPECT().GetAllMessagesAsc(ctx, "r1", gomock.Any()).Return(makePage(nil, false), nil)

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.NoError(t, err)
}

func TestHistoryService_LoadNextMessages_AfterBeforeHSS_ClampsToHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// after is before HSS — effective lower bound = HSS (the greater one)
	earlyTime := joinTime.Add(-1 * time.Hour)
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", joinTime, gomock.Any()).Return(makePage(nil, false), nil)

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  earlyTime.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
}

func TestHistoryService_LoadNextMessages_SubscriptionStoreError(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, false, fmt.Errorf("db error"))

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking subscription")
}

func TestHistoryService_LoadNextMessages_InvalidAfter(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  "not-a-timestamp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timestamp")
}

func TestHistoryService_LoadNextMessages_StoreErrorAfter(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// HSS present → GetMessagesAfter path
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", joinTime, gomock.Any()).Return(cassrepo.Page[model.Message]{}, fmt.Errorf("db error"))

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading next messages")
}

func TestHistoryService_LoadNextMessages_StoreErrorLatest(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// No HSS, no after → GetAllMessagesAsc path
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)
	msgs.EXPECT().GetAllMessagesAsc(ctx, "r1", gomock.Any()).Return(cassrepo.Page[model.Message]{}, fmt.Errorf("db error"))

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading next messages")
}

func TestHistoryService_LoadNextMessages_HasNext(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	messages := []model.Message{
		{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)},
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", joinTime, gomock.Any()).Return(makePage(messages, true), nil)

	resp, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 2)
	assert.True(t, resp.HasNext)
	assert.NotEmpty(t, resp.NextCursor)
}

// --- GetMessageByID ---

func TestHistoryService_GetMessageByID_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msg := &model.Message{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(msg, nil)

	result, err := svc.GetMessageByID(ctx, testParams, models.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.NoError(t, err)
	assert.Equal(t, "m1", result.ID)
}

func TestHistoryService_GetMessageByID_OutsideAccessWindow(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msg := &model.Message{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Hour)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(msg, nil)

	_, err := svc.GetMessageByID(ctx, testParams, models.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
}

func TestHistoryService_GetMessageByID_NotFound(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(nil, nil)

	_, err := svc.GetMessageByID(ctx, testParams, models.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHistoryService_GetMessageByID_StoreError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(nil, fmt.Errorf("db error"))

	_, err := svc.GetMessageByID(ctx, testParams, models.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading message")
}

func TestHistoryService_GetMessageByID_NoHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// nil HSS means no restriction — any message is accessible
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)
	msg := &model.Message{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Hour)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(msg, nil)

	result, err := svc.GetMessageByID(ctx, testParams, models.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.NoError(t, err)
	assert.Equal(t, "m1", result.ID)
}

func TestHistoryService_LoadNextMessages_HasNextFalse(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", joinTime, gomock.Any()).Return(makePage(nil, false), nil)

	resp, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.False(t, resp.HasNext)
	assert.Empty(t, resp.NextCursor)
}

// --- LoadSurroundingMessages ---

func TestHistoryService_LoadSurroundingMessages_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)

	beforeMsgs := []model.Message{{ID: "m4", RoomID: "r1", CreatedAt: joinTime.Add(4 * time.Minute)}}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, centralMsg.CreatedAt, gomock.Any()).Return(makePage(beforeMsgs, false), nil)

	afterMsgs := []model.Message{{ID: "m6", RoomID: "r1", CreatedAt: joinTime.Add(6 * time.Minute)}}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", centralMsg.CreatedAt, gomock.Any()).Return(makePage(afterMsgs, false), nil)

	resp, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.NoError(t, err)
	// before (reversed) + central + after = [m4, m5, m6]
	assert.Len(t, resp.Messages, 3)
	assert.Equal(t, "m4", resp.Messages[0].ID)
	assert.Equal(t, "m5", resp.Messages[1].ID)
	assert.Equal(t, "m6", resp.Messages[2].ID)
	assert.False(t, resp.MoreBefore)
	assert.False(t, resp.MoreAfter)
}

func TestHistoryService_LoadSurroundingMessages_MoreBeforeAndAfter(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)

	beforeMsgs := []model.Message{{ID: "m4", RoomID: "r1", CreatedAt: joinTime.Add(4 * time.Minute)}}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, centralMsg.CreatedAt, gomock.Any()).Return(makePage(beforeMsgs, true), nil)

	afterMsgs := []model.Message{{ID: "m6", RoomID: "r1", CreatedAt: joinTime.Add(6 * time.Minute)}}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", centralMsg.CreatedAt, gomock.Any()).Return(makePage(afterMsgs, true), nil)

	resp, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 4,
	})
	require.NoError(t, err)
	assert.True(t, resp.MoreBefore)
	assert.True(t, resp.MoreAfter)
}

func TestHistoryService_LoadSurroundingMessages_HSSBeforeMessage(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// accessSince set and before central message — before-page uses GetMessagesBetweenDesc,
	// after-page uses GetMessagesAfter (no access constraint needed for newer messages)
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)

	beforeMsgs := []model.Message{{ID: "m4", RoomID: "r1", CreatedAt: joinTime.Add(4 * time.Minute)}}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, centralMsg.CreatedAt, gomock.Any()).Return(makePage(beforeMsgs, false), nil)

	afterMsgs := []model.Message{{ID: "m6", RoomID: "r1", CreatedAt: joinTime.Add(6 * time.Minute)}}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", centralMsg.CreatedAt, gomock.Any()).Return(makePage(afterMsgs, false), nil)

	resp, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 3)
	assert.Equal(t, "m4", resp.Messages[0].ID)
	assert.Equal(t, "m5", resp.Messages[1].ID)
	assert.Equal(t, "m6", resp.Messages[2].ID)
}

func TestHistoryService_LoadSurroundingMessages_NoHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	// nil accessSince — no lower bound restriction, full history access
	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)

	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)

	beforeMsgs := []model.Message{{ID: "m4", RoomID: "r1", CreatedAt: joinTime.Add(4 * time.Minute)}}
	// since is zero — no lower bound, uses GetMessagesBefore (upper bound only)
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", centralMsg.CreatedAt, gomock.Any()).Return(makePage(beforeMsgs, false), nil)

	afterMsgs := []model.Message{{ID: "m6", RoomID: "r1", CreatedAt: joinTime.Add(6 * time.Minute)}}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", centralMsg.CreatedAt, gomock.Any()).Return(makePage(afterMsgs, false), nil)

	resp, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 3)
}

func TestHistoryService_LoadSurroundingMessages_SubscriptionError(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, false, fmt.Errorf("db error"))

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking subscription")
}

func TestHistoryService_LoadSurroundingMessages_CentralMessageOutsideWindow(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)

	oldMsg := &model.Message{ID: "m_old", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Hour)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m_old").Return(oldMsg, nil)

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m_old", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside access window")
}

func TestHistoryService_LoadSurroundingMessages_MessageNotFound(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessageByID(ctx, "r1", "nonexistent").Return(nil, nil)

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "nonexistent", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHistoryService_LoadSurroundingMessages_StoreError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(nil, fmt.Errorf("db error"))

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finding central message")
}

func TestHistoryService_LoadSurroundingMessages_BeforePageError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, centralMsg.CreatedAt, gomock.Any()).Return(cassrepo.Page[model.Message]{}, fmt.Errorf("db error"))

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading surrounding before")
}

func TestHistoryService_LoadSurroundingMessages_BeforePageError_NoHSS(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, true, nil)
	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", centralMsg.CreatedAt, gomock.Any()).Return(cassrepo.Page[model.Message]{}, fmt.Errorf("db error"))

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading surrounding before")
}

func TestHistoryService_LoadSurroundingMessages_AfterPageError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)
	beforeMsgs := []model.Message{{ID: "m4", RoomID: "r1", CreatedAt: joinTime.Add(4 * time.Minute)}}
	msgs.EXPECT().GetMessagesBetweenDesc(ctx, "r1", joinTime, centralMsg.CreatedAt, gomock.Any()).Return(makePage(beforeMsgs, false), nil)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", centralMsg.CreatedAt, gomock.Any()).Return(cassrepo.Page[model.Message]{}, fmt.Errorf("db error"))

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading surrounding after")
}

func TestHistoryService_LoadSurroundingMessages_Limit1_OnlyCentral(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, true, nil)
	centralMsg := &model.Message{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m5").Return(centralMsg, nil)
	// No before/after queries expected — half = 1/2 = 0

	resp, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 1,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 1)
	assert.Equal(t, "m5", resp.Messages[0].ID)
	assert.False(t, resp.MoreBefore)
	assert.False(t, resp.MoreAfter)
}

// --- Access Control: Not Subscribed ---

func TestHistoryService_LoadHistory_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, false, nil)

	_, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not subscribed to room")
}

func TestHistoryService_LoadNextMessages_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, false, nil)

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not subscribed to room")
}

func TestHistoryService_LoadSurroundingMessages_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, false, nil)

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not subscribed to room")
}

func TestHistoryService_GetMessageByID_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, false, nil)

	_, err := svc.GetMessageByID(ctx, testParams, models.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not subscribed to room")
}

// --- RoomID Mismatch ---

func TestHistoryService_LoadHistory_RoomIDMismatch(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()

	_, err := svc.LoadHistory(ctx, testParams, models.LoadHistoryRequest{RoomID: "wrong-room"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "roomId in body does not match subject")
}

func TestHistoryService_LoadNextMessages_RoomIDMismatch(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()

	_, err := svc.LoadNextMessages(ctx, testParams, models.LoadNextMessagesRequest{RoomID: "wrong-room"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "roomId in body does not match subject")
}

func TestHistoryService_LoadSurroundingMessages_RoomIDMismatch(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()

	_, err := svc.LoadSurroundingMessages(ctx, testParams, models.LoadSurroundingMessagesRequest{
		RoomID: "wrong-room", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "roomId in body does not match subject")
}

func TestHistoryService_GetMessageByID_RoomIDMismatch(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()

	_, err := svc.GetMessageByID(ctx, testParams, models.GetMessageByIDRequest{RoomID: "wrong-room", MessageID: "m1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "roomId in body does not match subject")
}
