package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/history-service/internal/service/mocks"
	"github.com/hmchangw/chat/pkg/model"
)

var joinTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newService(t *testing.T) (*service.HistoryService, *mocks.MockMessageRepository, *mocks.MockSubscriptionRepository) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	return service.New(msgs, subs), msgs, subs
}

func TestHistoryService_LoadHistory_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	messages := make([]model.Message, 4)
	for i := range messages {
		messages[i] = model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1",
			CreatedAt: joinTime.Add(time.Duration(4-i) * time.Minute),
		}
	}
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(messages, nil)

	resp, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 4)
	assert.False(t, resp.HasMore)
}

func TestHistoryService_LoadHistory_HasMore(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	messages := make([]model.Message, 51)
	for i := range messages {
		messages[i] = model.Message{ID: fmt.Sprintf("m%d", i), RoomID: "r1", CreatedAt: joinTime.Add(time.Duration(i) * time.Minute)}
	}
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(messages, nil)

	resp, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 50)
	assert.True(t, resp.HasMore)
}

func TestHistoryService_LoadHistory_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, nil)

	_, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.Error(t, err)
}

func TestHistoryService_LoadHistory_FirstUnread(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	lastSeen := joinTime.Add(2 * time.Minute)
	messages := []model.Message{
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
		{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(messages, nil)

	resp, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{
		RoomID:   "r1",
		LastSeen: lastSeen.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.FirstUnread)
	assert.Equal(t, "m3", resp.FirstUnread.ID)
}

func TestHistoryService_LoadNextMessages_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	after := joinTime.Add(1 * time.Minute)
	messages := []model.Message{
		{ID: "m2", RoomID: "r1", CreatedAt: joinTime.Add(2 * time.Minute)},
		{ID: "m3", RoomID: "r1", CreatedAt: joinTime.Add(3 * time.Minute)},
	}
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", after, 51).Return(messages, nil)

	resp, err := svc.LoadNextMessages(ctx, "u1", model.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  after.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	assert.Len(t, resp.Messages, 2)
	assert.False(t, resp.HasMore)
}

func TestHistoryService_LoadNextMessages_ClampsToHistorySharedSince(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	earlyTime := joinTime.Add(-1 * time.Hour)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", joinTime, 51).Return(nil, nil)

	_, err := svc.LoadNextMessages(ctx, "u1", model.LoadNextMessagesRequest{
		RoomID: "r1",
		After:  earlyTime.Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
}

func TestHistoryService_GetMessageByID_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	msg := &model.Message{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(msg, nil)

	result, err := svc.GetMessageByID(ctx, "u1", model.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.NoError(t, err)
	assert.Equal(t, "m1", result.ID)
}

func TestHistoryService_GetMessageByID_OutsideAccessWindow(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	msg := &model.Message{ID: "m1", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Hour)}
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(msg, nil)

	_, err := svc.GetMessageByID(ctx, "u1", model.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
}

// --- LoadHistory error paths ---

func TestHistoryService_LoadHistory_StoreError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(nil, fmt.Errorf("db down"))

	_, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading history")
}

func TestHistoryService_LoadHistory_InvalidBefore(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	_, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1", Before: "not-a-timestamp"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing before")
}

func TestHistoryService_LoadHistory_SubscriptionError(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, fmt.Errorf("db error"))

	_, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking subscription")
}

func TestHistoryService_LoadHistory_EmptyResult(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)
	msgs.EXPECT().GetMessagesBefore(ctx, "r1", joinTime, gomock.Any(), 51).Return(nil, nil)

	resp, err := svc.LoadHistory(ctx, "u1", model.LoadHistoryRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Empty(t, resp.Messages)
	assert.False(t, resp.HasMore)
}

// --- LoadNextMessages error paths ---

func TestHistoryService_LoadNextMessages_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, nil)

	_, err := svc.LoadNextMessages(ctx, "u1", model.LoadNextMessagesRequest{RoomID: "r1"})
	require.Error(t, err)
}

func TestHistoryService_LoadNextMessages_StoreError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", gomock.Any(), 51).Return(nil, fmt.Errorf("db error"))

	_, err := svc.LoadNextMessages(ctx, "u1", model.LoadNextMessagesRequest{RoomID: "r1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading next messages")
}

func TestHistoryService_LoadNextMessages_EmptyAfterGetsLatest(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)
	// When after is empty (zero), it should NOT be clamped — zero time passes through
	msgs.EXPECT().GetMessagesAfter(ctx, "r1", time.Time{}, 51).Return(nil, nil)

	_, err := svc.LoadNextMessages(ctx, "u1", model.LoadNextMessagesRequest{RoomID: "r1"})
	require.NoError(t, err)
}

// --- GetMessageByID error paths ---

func TestHistoryService_GetMessageByID_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, nil)

	_, err := svc.GetMessageByID(ctx, "u1", model.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
}

func TestHistoryService_GetMessageByID_NotFound(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(nil, nil)

	_, err := svc.GetMessageByID(ctx, "u1", model.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHistoryService_GetMessageByID_StoreError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)
	msgs.EXPECT().GetMessageByID(ctx, "r1", "m1").Return(nil, fmt.Errorf("db error"))

	_, err := svc.GetMessageByID(ctx, "u1", model.GetMessageByIDRequest{RoomID: "r1", MessageID: "m1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading message")
}

// --- LoadSurroundingMessages ---

func TestHistoryService_LoadSurroundingMessages_Success(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	before := []model.Message{
		{ID: "m4", RoomID: "r1", CreatedAt: joinTime.Add(4 * time.Minute)},
	}
	after := []model.Message{
		{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)},
		{ID: "m6", RoomID: "r1", CreatedAt: joinTime.Add(6 * time.Minute)},
	}
	msgs.EXPECT().GetSurroundingMessages(ctx, "r1", "m5", 6).Return(before, after, nil)

	resp, err := svc.LoadSurroundingMessages(ctx, "u1", model.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Before, 1)
	assert.Len(t, resp.After, 2)
}

func TestHistoryService_LoadSurroundingMessages_NotSubscribed(t *testing.T) {
	svc, _, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(nil, nil)

	_, err := svc.LoadSurroundingMessages(ctx, "u1", model.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
}

func TestHistoryService_LoadSurroundingMessages_CentralMessageOutsideWindow(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	// Central message is before HistorySharedSince
	before := []model.Message{}
	after := []model.Message{
		{ID: "m_old", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Hour)},
	}
	msgs.EXPECT().GetSurroundingMessages(ctx, "r1", "m_old", 6).Return(before, after, nil)

	_, err := svc.LoadSurroundingMessages(ctx, "u1", model.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m_old", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside access window")
}

func TestHistoryService_LoadSurroundingMessages_StoreError(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)
	msgs.EXPECT().GetSurroundingMessages(ctx, "r1", "m5", 6).Return(nil, nil, fmt.Errorf("db error"))

	_, err := svc.LoadSurroundingMessages(ctx, "u1", model.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading surrounding")
}

func TestHistoryService_LoadSurroundingMessages_FiltersHistorySharedSince(t *testing.T) {
	svc, msgs, subs := newService(t)
	ctx := context.Background()

	subs.EXPECT().GetHistorySharedSince(ctx, "u1", "r1").Return(&joinTime, nil)

	// Some before messages are before HistorySharedSince — should be filtered
	before := []model.Message{
		{ID: "old", RoomID: "r1", CreatedAt: joinTime.Add(-1 * time.Minute)},
		{ID: "new", RoomID: "r1", CreatedAt: joinTime.Add(1 * time.Minute)},
	}
	after := []model.Message{
		{ID: "m5", RoomID: "r1", CreatedAt: joinTime.Add(5 * time.Minute)},
	}
	msgs.EXPECT().GetSurroundingMessages(ctx, "r1", "m5", 6).Return(before, after, nil)

	resp, err := svc.LoadSurroundingMessages(ctx, "u1", model.LoadSurroundingMessagesRequest{
		RoomID: "r1", MessageID: "m5", Limit: 6,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Before, 1)
	assert.Equal(t, "new", resp.Before[0].ID)
	assert.Len(t, resp.After, 1)
}
