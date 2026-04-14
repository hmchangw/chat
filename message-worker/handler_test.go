package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessMessage(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	user := &model.User{
		ID:          "u-1",
		Account:     "alice",
		SiteID:      "site-a",
		EngName:     "Alice Wang",
		ChineseName: "愛麗絲",
	}
	msg := model.Message{
		ID:          "msg-1",
		RoomID:      "r1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello",
		CreatedAt:   now,
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a", Timestamp: now.UnixMilli()}
	validData, _ := json.Marshal(evt)

	threadMsg := model.Message{
		ID:                    "msg-2",
		RoomID:                "r1",
		UserID:                "u-1",
		UserAccount:           "alice",
		Content:               "thread reply",
		CreatedAt:             now,
		ThreadParentMessageID: "msg-1",
	}
	threadEvt := model.MessageEvent{Message: threadMsg, SiteID: "site-a", Timestamp: now.UnixMilli()}
	threadData, _ := json.Marshal(threadEvt)

	bobUser := &model.User{
		ID:          "u-bob",
		Account:     "bob",
		SiteID:      "site-a",
		EngName:     "Bob Chen",
		ChineseName: "鮑勃",
	}

	// Event with a real user mention — Mentions field is absent in the inbound event
	// and will be populated by resolveMentions.
	evtWithMention := model.MessageEvent{
		Message: model.Message{
			ID: "msg-3", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
			Content:   "hey @bob can you check this?",
			CreatedAt: now,
		},
		SiteID: "site-a", Timestamp: now.UnixMilli(),
	}
	dataWithMention, _ := json.Marshal(evtWithMention)

	// Expected stored message: Mentions resolved to full Participant.
	msgWithMention := model.Message{
		ID: "msg-3", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
		Content:   "hey @bob can you check this?",
		CreatedAt: now,
		Mentions: []model.Participant{{
			UserID: "u-bob", Account: "bob", ChineseName: "鮑勃", EngName: "Bob Chen",
		}},
	}

	// Event with @all — no user lookup should occur.
	evtWithAll := model.MessageEvent{
		Message: model.Message{
			ID: "msg-4", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
			Content:   "hello @all please read",
			CreatedAt: now,
		},
		SiteID: "site-a", Timestamp: now.UnixMilli(),
	}
	dataWithAll, _ := json.Marshal(evtWithAll)

	msgWithAll := model.Message{
		ID: "msg-4", RoomID: "r1", UserID: "u-1", UserAccount: "alice",
		Content:   "hello @all please read",
		CreatedAt: now,
		Mentions:  []model.Participant{{Account: "all", EngName: "all"}},
	}

	expectedSender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     msg.UserAccount,
	}

	tests := []struct {
		name       string
		data       []byte
		setupMocks func(store *MockStore, userStore *MockUserStore, threadStore *MockThreadStore)
		wantErr    bool
	}{
		{
			name: "happy path — user found and message saved",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), &msg, &expectedSender, "site-a").Return(nil)
			},
		},
		{
			name: "user not found — NAK without saving",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").
					Return(nil, errors.New("user not found"))
			},
			wantErr: true,
		},
		{
			name: "user store DB error — NAK without saving",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").
					Return(nil, errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
		{
			name: "save error — NAK after user lookup",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), &msg, &expectedSender, "site-a").
					Return(errors.New("cassandra: write timeout"))
			},
			wantErr: true,
		},
		{
			name:       "malformed JSON — NAK immediately",
			data:       []byte("{invalid"),
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {},
			wantErr:    true,
		},
		{
			name: "thread message — calls SaveThreadMessage not SaveMessage",
			data: threadData,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveThreadMessage(gomock.Any(), &threadMsg, &expectedSender, "site-a").Return(nil)
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-1").
					Return(&model.ThreadRoom{ID: "tr-1"}, nil)
				// Subsequent-reply path now also fetches the parent author and
				// upserts their subscription (guards against orphaned parent
				// subscription from a partial first-reply failure).
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-1").
					Return(&cassParticipant{ID: "u-parent", Account: "parent-user"}, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
				ts.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-1", "msg-2", now).Return(nil)
			},
		},
		{
			name: "thread message save error — NAK after user lookup",
			data: threadData,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveThreadMessage(gomock.Any(), &threadMsg, &expectedSender, "site-a").
					Return(errors.New("cassandra: write timeout"))
			},
			wantErr: true,
		},
		{
			name: "mention resolved to Participant and stored",
			data: dataWithMention,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
					Return([]model.User{*bobUser}, nil)
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), &msgWithMention, &expectedSender, "site-a").Return(nil)
			},
		},
		{
			name: "@all stored as special Participant without DB lookup",
			data: dataWithAll,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), &msgWithAll, &expectedSender, "site-a").Return(nil)
			},
		},
		{
			name: "mention user lookup error — NAK before sender lookup",
			data: dataWithMention,
			setupMocks: func(store *MockStore, us *MockUserStore, ts *MockThreadStore) {
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
					Return(nil, errors.New("mongo: connection refused"))
				// FindUserByID and SaveMessage must NOT be called
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStore := NewMockStore(ctrl)
			mockUserStore := NewMockUserStore(ctrl)
			mockThreadStore := NewMockThreadStore(ctrl)
			tt.setupMocks(mockStore, mockUserStore, mockThreadStore)

			h := NewHandler(mockStore, mockUserStore, mockThreadStore)
			err := h.processMessage(context.Background(), tt.data)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHandler_HandleThreadRoomAndSubscriptions(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	parentSender := &cassParticipant{
		ID:      "u-parent",
		Account: "parent-user",
	}

	msg := &model.Message{
		ID:                    "msg-reply",
		RoomID:                "r1",
		UserID:                "u-replier",
		UserAccount:           "replier",
		Content:               "thread reply",
		CreatedAt:             now,
		ThreadParentMessageID: "msg-parent",
	}

	tests := []struct {
		name       string
		msg        *model.Message
		siteID     string
		setupMocks func(store *MockStore, ts *MockThreadStore)
		wantErr    bool
	}{
		{
			name:   "first reply — different users — creates room and two subscriptions",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, room *model.ThreadRoom) error {
						assert.Equal(t, "msg-parent", room.ParentMessageID)
						assert.Equal(t, "r1", room.RoomID)
						assert.Equal(t, "site-a", room.SiteID)
						assert.Equal(t, "msg-reply", room.LastMsgID)
						return nil
					})
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "u-parent", sub.UserID)
						assert.Equal(t, "parent-user", sub.UserAccount)
						assert.True(t, sub.LastSeenAt.IsZero(), "parent's LastSeenAt should be zero")
						return nil
					})
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "u-replier", sub.UserID)
						assert.Equal(t, "replier", sub.UserAccount)
						assert.Equal(t, now, sub.LastSeenAt, "replier's LastSeenAt should be now")
						return nil
					})
			},
		},
		{
			name:   "first reply — parent message not found — ack-and-skip",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(nil, fmt.Errorf("wrap: %w", errMessageNotFound))
			},
			wantErr: false,
		},
		{
			name:   "subsequent reply — parent message not found — ack-and-skip",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(nil, fmt.Errorf("wrap: %w", errMessageNotFound))
			},
			wantErr: false,
		},
		{
			name: "first reply — same user — creates room and one subscription",
			msg: &model.Message{
				ID:                    "msg-reply",
				RoomID:                "r1",
				UserID:                "u-parent",
				UserAccount:           "parent-user",
				Content:               "self reply",
				CreatedAt:             now,
				ThreadParentMessageID: "msg-parent",
			},
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "u-parent", sub.UserID)
						return nil
					})
			},
		},
		{
			name:   "first reply — GetMessageSender fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(nil, errors.New("cassandra: read timeout"))
			},
			wantErr: true,
		},
		{
			name:   "first reply — UpsertThreadSubscription fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					Return(errors.New("mongo: write error"))
			},
			wantErr: true,
		},
		{
			name:   "first reply — replier UpsertThreadSubscription fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				// Parent upsert succeeds
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
				// Replier upsert fails
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					Return(errors.New("mongo: write error"))
			},
			wantErr: true,
		},
		{
			name:   "subsequent reply — upserts subscription and updates last message",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "tr-existing", sub.ThreadRoomID)
						assert.Equal(t, "u-parent", sub.UserID)
						assert.True(t, sub.LastSeenAt.IsZero(), "parent's LastSeenAt should be zero")
						return nil
					})
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, sub *model.ThreadSubscription) error {
						assert.Equal(t, "tr-existing", sub.ThreadRoomID)
						assert.Equal(t, "u-replier", sub.UserID)
						assert.Equal(t, now, sub.LastSeenAt, "replier's LastSeenAt should be now")
						return nil
					})
				ts.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-existing", "msg-reply", now).
					Return(nil)
			},
		},
		{
			name:   "subsequent reply — GetThreadRoomByParentMessageID fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(nil, errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
		{
			name:   "subsequent reply — UpsertThreadSubscription fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).
					Return(errors.New("mongo: write error"))
			},
			wantErr: true,
		},
		{
			name:   "subsequent reply — UpdateThreadRoomLastMessage fails — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errThreadRoomExists)
				ts.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "msg-parent").
					Return(&model.ThreadRoom{ID: "tr-existing"}, nil)
				store.EXPECT().GetMessageSender(gomock.Any(), "msg-parent").
					Return(parentSender, nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
				ts.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil)
				ts.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-existing", "msg-reply", now).
					Return(errors.New("mongo: write error"))
			},
			wantErr: true,
		},
		{
			name:   "CreateThreadRoom unexpected error — returns error",
			msg:    msg,
			siteID: "site-a",
			setupMocks: func(store *MockStore, ts *MockThreadStore) {
				ts.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).
					Return(errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStore := NewMockStore(ctrl)
			mockThreadStore := NewMockThreadStore(ctrl)
			mockUserStore := NewMockUserStore(ctrl)
			tt.setupMocks(mockStore, mockThreadStore)

			h := NewHandler(mockStore, mockUserStore, mockThreadStore)
			err := h.handleThreadRoomAndSubscriptions(context.Background(), tt.msg, tt.siteID)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseMentions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "no mentions", content: "hello world", want: nil},
		{name: "single mention", content: "hello @bob", want: []string{"bob"}},
		{name: "multiple mentions", content: "@alice check with @bob", want: []string{"alice", "bob"}},
		{name: "mention at start", content: "@alice hello", want: []string{"alice"}},
		{name: "at-all", content: "hey @all check this", want: []string{"all"}},
		{name: "email-style mention", content: "ping @user@domain.com", want: []string{"user@domain.com"}},
		{name: "quoted reply prefix", content: ">@alice this is quoted", want: []string{"alice"}},
		{name: "duplicates deduplicated", content: "@bob and @bob again", want: []string{"bob"}},
		{name: "dots and hyphens", content: "cc @first.last and @my-user", want: []string{"first.last", "my-user"}},
		{name: "empty content", content: "", want: nil},
		{name: "trailing period not captured", content: "hey @bob. check this", want: []string{"bob"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseMentions(tt.content))
		})
	}
}
