package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/config"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/mongorepo"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/history-service/internal/service/mocks"
	"github.com/hmchangw/chat/pkg/errcode"
	pkgmodel "github.com/hmchangw/chat/pkg/model"
)

func newThreadListService(t *testing.T) (
	*service.HistoryService,
	*mocks.MockMessageRepository,
	*mocks.MockSubscriptionRepository,
	*mocks.MockRoomRepository,
	*mocks.MockThreadSubscriptionRepository,
) {
	ctrl := gomock.NewController(t)
	msgs := mocks.NewMockMessageRepository(ctrl)
	subs := mocks.NewMockSubscriptionRepository(ctrl)
	rooms := mocks.NewMockRoomRepository(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)
	threadRooms := mocks.NewMockThreadRoomRepository(ctrl)
	threadSubs := mocks.NewMockThreadSubscriptionRepository(ctrl)
	users := mocks.NewMockUserStore(ctrl)
	customEmojis := mocks.NewMockCustomEmojiStore(ctrl)
	cfg := &config.Config{MessageHistoryFloorDays: 90, LargeRoomThreshold: 500, MaxPinnedPerRoom: 10, PinEnabled: true}
	svc := service.New(msgs, subs, rooms, pub, threadRooms, threadSubs, users, customEmojis, cfg)
	return svc, msgs, subs, rooms, threadSubs
}

func TestHistoryService_ListThreadSubscriptions_Success(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	rows := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-1", RoomID: "r1", SiteID: "site-a", RoomName: "general", RoomType: pkgmodel.RoomTypeChannel, ParentMessageID: "p1", LastMsgID: "m1", LastMsgAt: base.Add(5 * time.Hour), LastSeenAt: ptrTime(base.Add(2 * time.Hour)), HasMention: true},
		{ThreadRoomID: "tr-2", RoomID: "r1", SiteID: "site-a", RoomName: "general", RoomType: pkgmodel.RoomTypeChannel, ParentMessageID: "p2", LastMsgID: "m2", LastMsgAt: base.Add(3 * time.Hour), LastSeenAt: nil},
	}
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, true, nil)
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{
		{MessageID: "p1", RoomID: "r1", Msg: "parent 1", TCount: intPtr(4)},
		{MessageID: "m1", RoomID: "r1", Msg: "last 1"},
		{MessageID: "p2", RoomID: "r1", Msg: "parent 2"},
		{MessageID: "m2", RoomID: "r1", Msg: "last 2"},
	}, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r1").Return(nil, true, nil)

	// limit 2: the batch fills the page, so the fill loop stops after one fetch.
	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice", Limit: 2})
	require.NoError(t, err)
	require.Len(t, resp.Items, 2)
	assert.True(t, resp.HasMore)

	first := resp.Items[0]
	assert.Equal(t, "tr-1", first.ThreadRoomID)
	assert.Equal(t, "general", first.RoomName)
	assert.Equal(t, pkgmodel.RoomTypeChannel, first.RoomType)
	assert.Equal(t, "site-a", first.SiteID)
	assert.True(t, first.HasMention)
	assert.Equal(t, base.Add(5*time.Hour).UnixMilli(), first.LastMsgAt)
	require.NotNil(t, first.LastSeenAt)
	assert.Equal(t, base.Add(2*time.Hour).UnixMilli(), *first.LastSeenAt)
	require.NotNil(t, first.ParentMessage)
	assert.Equal(t, "p1", first.ParentMessage.MessageID)
	require.NotNil(t, first.ParentMessage.TCount)
	assert.Equal(t, 4, *first.ParentMessage.TCount) // reply count rides on the parent
	require.NotNil(t, first.LastMessage)
	assert.Equal(t, "m1", first.LastMessage.MessageID)
	assert.True(t, first.Unread) // lastMsgAt 5h > lastSeenAt 2h

	second := resp.Items[1]
	assert.Nil(t, second.LastSeenAt)
	assert.True(t, second.Unread) // never-seen ⇒ unread
}

func TestHistoryService_ListThreadSubscriptions_Empty(t *testing.T) {
	svc, _, _, _, threadSubs := newThreadListService(t)
	// No rows ⇒ no message/room/access lookups at all.
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, false, nil)

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice"})
	require.NoError(t, err)
	assert.Empty(t, resp.Items)
	assert.NotNil(t, resp.Items) // never nil — JSON [] not null
	assert.False(t, resp.HasMore)
}

func TestHistoryService_ListThreadSubscriptions_MissingAccount(t *testing.T) {
	svc, _, _, _, _ := newThreadListService(t)
	_, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Limit: 10})
	require.Error(t, err)
	ec := errcode.Classify(context.Background(), err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.CodeBadRequest, ec.Code)
}

func TestHistoryService_ListThreadSubscriptions_AccessWindowDropsParent(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	since := base.Add(4 * time.Hour)

	rows := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-old", RoomID: "r1", SiteID: "site-a", ParentMessageID: "p-old", LastMsgID: "m-old", LastMsgAt: base.Add(5 * time.Hour)},
	}
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, false, nil)
	// Parent created before the access window.
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{
		{MessageID: "p-old", RoomID: "r1", CreatedAt: base.Add(1 * time.Hour)},
	}, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r1").Return(&since, true, nil)

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice"})
	require.NoError(t, err)
	assert.Empty(t, resp.Items) // dropped — parent predates access window
}

func TestHistoryService_ListThreadSubscriptions_AccessWindowDropsDeletedParent(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	since := base.Add(4 * time.Hour)

	rows := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-gone", RoomID: "r1", SiteID: "site-a", ParentMessageID: "p-gone", LastMsgID: "m-gone", LastMsgAt: base.Add(5 * time.Hour)},
	}
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, false, nil)
	// Parent absent from hydration (deleted / not replicated) — its creation time
	// cannot be verified against the access window, so the thread must be dropped.
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{
		{MessageID: "m-gone", RoomID: "r1", CreatedAt: base.Add(5 * time.Hour)},
	}, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r1").Return(&since, true, nil)

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice"})
	require.NoError(t, err)
	assert.Empty(t, resp.Items) // dropped — parent unverifiable within a restricted access window
}

func TestHistoryService_ListThreadSubscriptions_FullAccessKeepsDeletedParent(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	rows := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-gone", RoomID: "r1", SiteID: "site-a", ParentMessageID: "p-gone", LastMsgID: "m-gone", LastMsgAt: base.Add(5 * time.Hour)},
	}
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, false, nil)
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{
		{MessageID: "m-gone", RoomID: "r1", CreatedAt: base.Add(5 * time.Hour)},
	}, nil)
	// Full history access (since == nil): a missing parent does not drop the thread.
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r1").Return(nil, true, nil)

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice", Limit: 10})
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "tr-gone", resp.Items[0].ThreadRoomID)
	assert.Nil(t, resp.Items[0].ParentMessage)
}

func TestHistoryService_ListThreadSubscriptions_NotSubscribedRoomDropped(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	rows := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-1", RoomID: "r1", SiteID: "site-a", ParentMessageID: "p1", LastMsgID: "m1", LastMsgAt: base.Add(5 * time.Hour)},
		{ThreadRoomID: "tr-2", RoomID: "r2", SiteID: "site-a", ParentMessageID: "p2", LastMsgID: "m2", LastMsgAt: base.Add(3 * time.Hour)},
	}
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, false, nil)
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{
		{MessageID: "p1", RoomID: "r1"}, {MessageID: "m1", RoomID: "r1"},
		{MessageID: "p2", RoomID: "r2"}, {MessageID: "m2", RoomID: "r2"},
	}, nil)
	// r1 subscribed; r2 not subscribed ⇒ r2's threads are dropped, r1's kept.
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r1").Return(nil, true, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r2").Return(nil, false, nil)

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice", Limit: 10})
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "tr-1", resp.Items[0].ThreadRoomID)
	assert.Equal(t, "r1", resp.Items[0].RoomID)
}

func TestHistoryService_ListThreadSubscriptions_AccessErrorDegrades(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	rows := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-1", RoomID: "r1", SiteID: "site-a", ParentMessageID: "p1", LastMsgID: "m1", LastMsgAt: base.Add(5 * time.Hour)},
		{ThreadRoomID: "tr-2", RoomID: "r2", SiteID: "site-a", ParentMessageID: "p2", LastMsgID: "m2", LastMsgAt: base.Add(3 * time.Hour)},
	}
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, false, nil)
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{
		{MessageID: "p1", RoomID: "r1"}, {MessageID: "m1", RoomID: "r1"},
		{MessageID: "p2", RoomID: "r2"}, {MessageID: "m2", RoomID: "r2"},
	}, nil)
	// r2's access lookup fails ⇒ log + continue: r2 dropped, the page still succeeds.
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r1").Return(nil, true, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r2").Return(nil, false, errors.New("mongo timeout"))

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice", Limit: 10})
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "r1", resp.Items[0].RoomID)
}

func TestHistoryService_ListThreadSubscriptions_FillsPageAcrossFilteredBatch(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	// Batch 1 is entirely a room the user isn't subscribed to (filtered out) with
	// more pending; batch 2 has the visible item. The leaf must keep scanning and
	// return batch 2's item rather than an empty page with HasMore.
	batch1 := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-1", RoomID: "r-denied", SiteID: "site-a", ParentMessageID: "p1", LastMsgID: "m1", LastMsgAt: base.Add(5 * time.Hour)},
	}
	batch2 := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-2", RoomID: "r-ok", SiteID: "site-a", ParentMessageID: "p2", LastMsgID: "m2", LastMsgAt: base.Add(3 * time.Hour)},
	}
	gomock.InOrder(
		threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Nil(), "", gomock.Any()).Return(batch1, true, nil),
		// Second fetch resumes at batch 1's last scanned position.
		threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Not(gomock.Nil()), "tr-1", gomock.Any()).Return(batch2, false, nil),
	)
	gomock.InOrder(
		msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{{MessageID: "p1", RoomID: "r-denied"}, {MessageID: "m1", RoomID: "r-denied"}}, nil),
		msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{{MessageID: "p2", RoomID: "r-ok"}, {MessageID: "m2", RoomID: "r-ok"}}, nil),
	)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r-denied").Return(nil, false, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r-ok").Return(nil, true, nil)

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice", Limit: 1})
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "tr-2", resp.Items[0].ThreadRoomID)
	assert.False(t, resp.HasMore) // batch 2 exhausted the source
}

func TestHistoryService_ListThreadSubscriptions_FillLoopBounded(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	// Every scan returns a filtered (not-subscribed) batch with more still
	// pending. The loop is bounded, so after maxThreadListScans it returns an
	// empty page that still reports HasMore — the accepted residual of option B.
	row := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-x", RoomID: "r-denied", SiteID: "site-a", ParentMessageID: "px", LastMsgID: "mx", LastMsgAt: base.Add(time.Hour)},
	}
	const maxScans = 5 // keep in sync with maxThreadListScans
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(row, true, nil).Times(maxScans)
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{{MessageID: "px", RoomID: "r-denied"}, {MessageID: "mx", RoomID: "r-denied"}}, nil).Times(maxScans)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r-denied").Return(nil, false, nil) // memoized ⇒ once

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice", Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, resp.Items)
	assert.True(t, resp.HasMore)
}

func TestHistoryService_ListThreadSubscriptions_MissingRoomMetaDegrades(t *testing.T) {
	svc, msgs, subs, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	// Row with no room name/type — the pipeline's rooms $lookup found no doc.
	rows := []mongorepo.ThreadSubRow{
		{ThreadRoomID: "tr-1", RoomID: "r1", SiteID: "site-a", ParentMessageID: "p1", LastMsgID: "m1", LastMsgAt: base.Add(1 * time.Hour)},
	}
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, false, nil)
	msgs.EXPECT().GetMessagesByIDs(gomock.Any(), gomock.Any()).Return([]models.Message{
		{MessageID: "p1", RoomID: "r1"},
	}, nil)
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "alice", "r1").Return(nil, true, nil)

	resp, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice"})
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	assert.Empty(t, resp.Items[0].RoomName)
	assert.Empty(t, string(resp.Items[0].RoomType))
}

func TestHistoryService_ListThreadSubscriptions_RepoError(t *testing.T) {
	svc, _, _, _, threadSubs := newThreadListService(t)
	threadSubs.EXPECT().ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, false, errors.New("mongo down"))

	_, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice"})
	require.Error(t, err)
	ec := errcode.Classify(context.Background(), err)
	require.NotNil(t, ec)
	assert.Equal(t, errcode.CodeInternal, ec.Code)
}

func TestHistoryService_ListThreadSubscriptions_LimitClamp(t *testing.T) {
	svc, _, _, _, threadSubs := newThreadListService(t)
	var gotLimit int
	threadSubs.EXPECT().
		ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ *time.Time, _ string, limit int) ([]mongorepo.ThreadSubRow, bool, error) {
			gotLimit = limit
			return nil, false, nil
		})

	_, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{Account: "alice", Limit: 9999})
	require.NoError(t, err)
	assert.Equal(t, 100, gotLimit) // clamped to maxPageSize
}

func TestHistoryService_ListThreadSubscriptions_CursorConverted(t *testing.T) {
	svc, _, _, _, threadSubs := newThreadListService(t)
	base := time.Date(2026, 2, 1, 5, 0, 0, 0, time.UTC)
	var gotTs *time.Time
	var gotID string
	threadSubs.EXPECT().
		ListUserThreadSubscriptions(gomock.Any(), "alice", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, ts *time.Time, id string, _ int) ([]mongorepo.ThreadSubRow, bool, error) {
			gotTs, gotID = ts, id
			return nil, false, nil
		})

	ms := base.UnixMilli()
	_, err := svc.ListThreadSubscriptions(testContext(), pkgmodel.ThreadSubscriptionListRequest{
		Account: "alice", CursorLastMsgAt: &ms, CursorThreadRoomID: "tr-9",
	})
	require.NoError(t, err)
	require.NotNil(t, gotTs)
	assert.Equal(t, base.UnixMilli(), gotTs.UnixMilli())
	assert.Equal(t, "tr-9", gotID)
}
