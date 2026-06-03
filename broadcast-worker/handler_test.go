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
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/mention"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomcrypto"
	"github.com/hmchangw/chat/pkg/roommetacache"
	"github.com/hmchangw/chat/pkg/subject"
)

type publishRecord struct {
	subject string
	data    []byte
}

type mockPublisher struct {
	records []publishRecord
}

func (m *mockPublisher) Publish(_ context.Context, subj string, data []byte) error {
	m.records = append(m.records, publishRecord{subject: subj, data: data})
	return nil
}

func decodeRoomEvent(t *testing.T, data []byte) model.RoomEvent {
	t.Helper()
	var e model.RoomEvent
	require.NoError(t, json.Unmarshal(data, &e))
	return e
}

var (
	testChannelRoom = &model.Room{
		ID: "room-1", Name: "general", Type: model.RoomTypeChannel,
		SiteID: "site-a", UserCount: 5,
	}
	testDMRoom = &model.Room{
		ID: "dm-1", Name: "", Type: model.RoomTypeDM,
		SiteID: "site-a", UserCount: 2,
	}
	testDMSubs = []model.Subscription{
		{User: model.SubscriptionUser{ID: "alice-id", Account: "alice"}, RoomID: "dm-1"},
		{User: model.SubscriptionUser{ID: "bob-id", Account: "bob"}, RoomID: "dm-1"},
	}
	testUsers = []model.User{
		{ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001", SiteID: "site-a"},
		{ID: "u-bob", Account: "bob", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002", SiteID: "site-a"},
	}
)

func metaOf(r *model.Room) roommetacache.Meta {
	return roommetacache.Meta{
		ID:        r.ID,
		Type:      r.Type,
		Name:      r.Name,
		SiteID:    r.SiteID,
		UserCount: r.UserCount,
	}
}

func makeMessageEvent(roomID, content string, msgTime time.Time) []byte {
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: roomID, UserID: "user-1", UserAccount: "sender",
			Content: content, CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)
	return data
}

func TestHandleMessage_DispatchesByEvent(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		event       model.EventType
		wantErr     bool
		wantErrText string
	}{
		{
			name:    "created event",
			event:   model.EventCreated,
			wantErr: false,
		},
		{
			name:        "updated event without timestamps fails missing-timestamp guard",
			event:       model.EventUpdated,
			wantErr:     true,
			wantErrText: "missing EditedAt",
		},
		{
			name:        "deleted event without timestamp fails missing-timestamp guard",
			event:       model.EventDeleted,
			wantErr:     true,
			wantErrText: "missing UpdatedAt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			evt := model.MessageEvent{
				Event:  tc.event,
				SiteID: "site-a",
				Message: model.Message{
					ID: "msg-1", RoomID: "room-1", UserID: "user-1", UserAccount: "sender",
					Content: "hello", CreatedAt: msgTime,
				},
			}
			data, err := json.Marshal(evt)
			require.NoError(t, err)

			if !tc.wantErr {
				// Created path: expect the full created-flow mock calls.
				key := testRoomKey(t)
				keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)
				store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
				store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil)
			}

			h := NewHandler(store, us, pub, keyStore, true)
			err = h.HandleMessage(context.Background(), data)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrText)
				assert.Empty(t, pub.records, "stub handlers must not publish")
				return
			}

			require.NoError(t, err)
			require.Len(t, pub.records, 1)
			gotEvt := model.RoomEvent{}
			require.NoError(t, json.Unmarshal(pub.records[0].data, &gotEvt))
			assert.Equal(t, model.RoomEventNewMessage, gotEvt.Type)
		})
	}
}

func TestHandler_HandleMessage_ChannelRoom(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)
	senderUser := model.User{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}

	tests := []struct {
		name            string
		content         string
		wantMentionAll  bool
		wantMentions    []string // expected accounts in evt.Mentions (includes "all" if present)
		wantSetMentions []string // accounts for SetSubscriptionMentions (nil = not called)
	}{
		{
			name:           "no mentions",
			content:        "hello group",
			wantMentionAll: false,
		},
		{
			name:            "individual mentions",
			content:         "hey @alice and @bob",
			wantMentions:    []string{"alice", "bob"},
			wantSetMentions: []string{"alice", "bob"},
		},
		{
			name:           "mention all case insensitive",
			content:        "attention @all",
			wantMentionAll: true,
			wantMentions:   []string{"all"},
		},
		{
			name:            "mention all and individual",
			content:         "@All and @alice",
			wantMentionAll:  true,
			wantMentions:    []string{"alice", "all"},
			wantSetMentions: []string{"alice"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}

			key := testRoomKey(t)
			keyStore := NewMockRoomKeyProvider(ctrl)
			keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

			store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, tc.wantMentionAll).Return(nil)
			store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)

			if tc.wantSetMentions != nil {
				store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", gomock.InAnyOrder(tc.wantSetMentions)).Return(nil)
			}

			// Single user lookup: sender + mentions, deduped, sender first.
			switch tc.name {
			case "individual mentions":
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender", "alice", "bob"}).
					Return([]model.User{senderUser, testUsers[0], testUsers[1]}, nil)
			case "mention all and individual":
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender", "alice"}).
					Return([]model.User{senderUser, testUsers[0]}, nil)
			default:
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).
					Return([]model.User{senderUser}, nil)
			}

			h := NewHandler(store, us, pub, keyStore, true)
			err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", tc.content, msgTime))
			require.NoError(t, err)

			require.Len(t, pub.records, 1)
			assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)

			evt, msg := decryptClientMessage(t, pub.records[0].data, key)
			assert.Equal(t, model.RoomEventNewMessage, evt.Type)
			assert.Equal(t, "room-1", evt.RoomID)
			assert.Equal(t, "general", evt.RoomName)
			assert.Equal(t, "site-a", evt.SiteID)
			assert.Equal(t, 5, evt.UserCount)
			assert.Equal(t, "msg-1", evt.LastMsgID)
			assert.Greater(t, evt.Timestamp, int64(0))
			assert.Equal(t, tc.wantMentionAll, evt.MentionAll)

			assert.Equal(t, "msg-1", msg.ID)
			require.NotNil(t, msg.Sender)
			assert.Equal(t, "user-1", msg.Sender.UserID)
			assert.Equal(t, "sender", msg.Sender.Account)
			assert.Equal(t, "寄件者", msg.Sender.ChineseName)
			assert.Equal(t, "Sender Lin", msg.Sender.EngName)

			if tc.wantMentions != nil {
				require.Len(t, evt.Mentions, len(tc.wantMentions))
				mentionAccounts := make([]string, len(evt.Mentions))
				for i, m := range evt.Mentions {
					mentionAccounts[i] = m.Account
				}
				assert.ElementsMatch(t, tc.wantMentions, mentionAccounts)
			} else {
				assert.Empty(t, evt.Mentions)
			}
		})
	}
}

func TestHandler_HandleMessage_DMRoom(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		content         string
		wantSetMentions bool
		mentionedUsers  []string
		aliceHasMention bool
		bobHasMention   bool
	}{
		{
			name:            "no mentions",
			content:         "hey bob",
			wantSetMentions: false,
			aliceHasMention: false,
			bobHasMention:   false,
		},
		{
			name:            "with mention",
			content:         "hey @bob",
			wantSetMentions: true,
			mentionedUsers:  []string{"bob"},
			aliceHasMention: false,
			bobHasMention:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}

			evt := model.MessageEvent{
				Event:  model.EventCreated,
				SiteID: "site-a",
				Message: model.Message{
					ID: "msg-1", RoomID: "dm-1", UserID: "alice-id", UserAccount: "alice",
					Content: tc.content, CreatedAt: msgTime,
				},
			}
			data, _ := json.Marshal(evt)

			store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "dm-1", "msg-1", msgTime, false).Return(nil)
			store.EXPECT().GetRoomMeta(gomock.Any(), "dm-1").Return(metaOf(testDMRoom), nil)
			store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(testDMSubs, nil)

			if tc.wantSetMentions {
				store.EXPECT().SetSubscriptionMentions(gomock.Any(), "dm-1", gomock.InAnyOrder(tc.mentionedUsers)).Return(nil)
			}

			// Single user lookup: sender first, then mentioned accounts.
			if tc.wantSetMentions {
				wantAccounts := append([]string{"alice"}, tc.mentionedUsers...)
				us.EXPECT().FindUsersByAccounts(gomock.Any(), wantAccounts).
					Return([]model.User{testUsers[0], testUsers[1]}, nil)
			} else {
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"alice"}).
					Return([]model.User{testUsers[0]}, nil)
			}

			keyStore := NewMockRoomKeyProvider(ctrl)
			h := NewHandler(store, us, pub, keyStore, true)
			err := h.HandleMessage(context.Background(), data)
			require.NoError(t, err)

			require.Len(t, pub.records, 2)

			evtBySubject := map[string]model.RoomEvent{}
			for _, rec := range pub.records {
				evtBySubject[rec.subject] = decodeRoomEvent(t, rec.data)
			}

			aliceEvt := evtBySubject[subject.UserRoomEvent("alice")]
			assert.Equal(t, model.RoomEventNewMessage, aliceEvt.Type)
			assert.Greater(t, aliceEvt.Timestamp, int64(0))
			require.NotNil(t, aliceEvt.Message, "DM events must carry Message payload")
			assert.Equal(t, "msg-1", aliceEvt.Message.ID)
			require.NotNil(t, aliceEvt.Message.Sender)
			assert.Equal(t, "alice-id", aliceEvt.Message.Sender.UserID)
			assert.Equal(t, "alice", aliceEvt.Message.Sender.Account)
			assert.Equal(t, tc.aliceHasMention, aliceEvt.HasMention)

			bobEvt := evtBySubject[subject.UserRoomEvent("bob")]
			require.NotNil(t, bobEvt.Message)
			assert.Greater(t, bobEvt.Timestamp, int64(0))
			assert.Equal(t, "msg-1", bobEvt.Message.ID)
			require.NotNil(t, bobEvt.Message.Sender)
			assert.Equal(t, tc.bobHasMention, bobEvt.HasMention)
		})
	}
}

func TestHandler_HandleCreated_BotDM(t *testing.T) {
	// A non-thread new message in a BotDM room must fan out to the human
	// recipient (and skip the bot), the same way edits/deletes do via
	// publishMutation. Before the fix, handleCreated had no RoomTypeBotDM case
	// and silently dropped the message to the default branch.
	msgTime := time.Date(2026, 3, 26, 11, 30, 0, 0, time.UTC)
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	botDMMeta := roommetacache.Meta{
		ID: "botdm-1", Type: model.RoomTypeBotDM, SiteID: "site-a", UserCount: 2,
	}
	botDMSubs := []model.Subscription{
		{User: model.SubscriptionUser{ID: "alice-id", Account: "alice"}, RoomID: "botdm-1"},
		{User: model.SubscriptionUser{ID: "bot-id", Account: "helper.bot"}, RoomID: "botdm-1"},
	}

	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: "botdm-1", UserID: "alice-id", UserAccount: "alice",
			Content: "hey bot", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "botdm-1", "msg-1", msgTime, false).Return(nil)
	store.EXPECT().GetRoomMeta(gomock.Any(), "botdm-1").Return(botDMMeta, nil)
	store.EXPECT().ListSubscriptions(gomock.Any(), "botdm-1").Return(botDMSubs, nil)
	us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"alice"}).Return([]model.User{testUsers[0]}, nil)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 1, "botDM new message: only the human recipient gets the live event, bot skipped")
	assert.Equal(t, subject.UserRoomEvent("alice"), pub.records[0].subject)
}

func TestHandler_HandleMessage_Errors(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)

	t.Run("invalid json", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, us, pub, keyStore, true)

		err := h.HandleMessage(context.Background(), []byte("not json"))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("room not found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil) // combined lookup runs before UpdateRoomLastMessage
		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(errors.New("not found"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("update room fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil) // combined lookup runs before UpdateRoomLastMessage
		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(errors.New("db error"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("set subscription mentions fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender", "alice"}).Return(testUsers[:1], nil) // single combined lookup
		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
		store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", gomock.Any()).Return(errors.New("db error"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hey @alice", msgTime))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "set subscription mentions")
		assert.Empty(t, pub.records)
	})

	t.Run("unknown room type", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		unknownRoom := &model.Room{
			ID: "room-1", Name: "general", Type: "unknown",
			SiteID: "site-a", UserCount: 5,
		}
		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(unknownRoom), nil)
		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil) // sender lookup

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.NoError(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("list subscriptions fails for DM", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "dm-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "dm-1").Return(metaOf(testDMRoom), nil)
		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil) // sender lookup
		store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(nil, errors.New("db error"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, us, pub, keyStore, true)
		evt := model.MessageEvent{
			Event:  model.EventCreated,
			SiteID: "site-a",
			Message: model.Message{
				ID: "msg-1", RoomID: "dm-1", UserID: "user-1", UserAccount: "sender",
				Content: "hello", CreatedAt: msgTime,
			},
		}
		data, _ := json.Marshal(evt)
		err := h.HandleMessage(context.Background(), data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list subscriptions")
		assert.Empty(t, pub.records)
	})

	t.Run("sender mentioned resolves with user data", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		senderUser := model.User{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}
		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
		store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", []string{"sender"}).Return(nil)
		// Single lookup: sender is both the message author and the mentioned account, so the deduped list is just ["sender"].
		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return([]model.User{senderUser}, nil)

		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hey @sender", msgTime))
		require.NoError(t, err)

		require.Len(t, pub.records, 1)
		evt, _ := decryptClientMessage(t, pub.records[0].data, key)
		require.Len(t, evt.Mentions, 1)
		assert.Equal(t, "sender", evt.Mentions[0].Account)
		assert.Equal(t, "寄件者", evt.Mentions[0].ChineseName)
		assert.Equal(t, "u-sender", evt.Mentions[0].UserID)
	})

	t.Run("sender lookup fails fallback to account", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, errors.New("db error")) // sender lookup

		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.NoError(t, err)

		require.Len(t, pub.records, 1)
		_, msg := decryptClientMessage(t, pub.records[0].data, key)
		require.NotNil(t, msg.Sender)
		assert.Equal(t, "sender", msg.Sender.Account)
		assert.Equal(t, "sender", msg.Sender.ChineseName)
		assert.Equal(t, "sender", msg.Sender.EngName)
	})
}

type failingPublisher struct {
	callCount int
	failAfter int
	records   []publishRecord
}

func (p *failingPublisher) Publish(_ context.Context, subj string, data []byte) error {
	p.callCount++
	if p.callCount > p.failAfter {
		return errors.New("publish failed")
	}
	p.records = append(p.records, publishRecord{subject: subj, data: data})
	return nil
}

func TestHandler_HandleMessage_DMRoom_PublishError(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	pub := &failingPublisher{failAfter: 0}

	us := NewMockUserStore(ctrl)
	store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "dm-1", "msg-1", msgTime, false).Return(nil)
	store.EXPECT().GetRoomMeta(gomock.Any(), "dm-1").Return(metaOf(testDMRoom), nil)
	store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(testDMSubs, nil)
	us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"alice"}).Return([]model.User{testUsers[0]}, nil) // sender lookup

	keyStore := NewMockRoomKeyProvider(ctrl)
	h := NewHandler(store, us, pub, keyStore, true)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: "dm-1", UserID: "alice-id", UserAccount: "alice",
			Content: "hello", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	err := h.HandleMessage(context.Background(), data)
	require.NoError(t, err)
	assert.Equal(t, 2, pub.callCount)
}

func TestHandler_HandleMessage_ChannelRoom_Encryption(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)

	t.Run("keystore returns nil key", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(nil, nil)

		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil)

		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.ErrorIs(t, err, errNoCurrentKey)
		assert.Empty(t, pub.records)
	})

	t.Run("keystore returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(nil, errors.New("valkey down"))

		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil)

		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get room key")
		assert.Contains(t, err.Error(), "valkey down")
		assert.Empty(t, pub.records)
	})

	t.Run("published event has encrypted message and plaintext metadata", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}

		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

		store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
		us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return([]model.User{{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}}, nil)

		h := NewHandler(store, us, pub, keyStore, true)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.NoError(t, err)

		require.Len(t, pub.records, 1)
		assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)

		// Verify plaintext metadata on the RoomEvent
		var rawEvt map[string]any
		require.NoError(t, json.Unmarshal(pub.records[0].data, &rawEvt))
		assert.Equal(t, "room-1", rawEvt["roomId"])
		assert.Equal(t, "general", rawEvt["roomName"])
		assert.Equal(t, "site-a", rawEvt["siteId"])
		assert.Nil(t, rawEvt["message"], "message must be nil in published JSON")
		assert.NotNil(t, rawEvt["encryptedMessage"], "encryptedMessage must be present")

		// Verify encrypted message structure
		var evt model.RoomEvent
		require.NoError(t, json.Unmarshal(pub.records[0].data, &evt))
		require.Nil(t, evt.Message)
		require.NotEmpty(t, evt.EncryptedMessage)

		var env roomcrypto.EncryptedMessage
		require.NoError(t, json.Unmarshal(evt.EncryptedMessage, &env))
		assert.Equal(t, key.Version, env.Version)
		assert.NotEmpty(t, env.Nonce)
		assert.NotEmpty(t, env.Ciphertext)

		// Decrypt and verify the ClientMessage
		_, msg := decryptClientMessage(t, pub.records[0].data, key)
		assert.Equal(t, "msg-1", msg.ID)
		assert.Equal(t, "room-1", msg.RoomID)
		assert.Equal(t, "hello", msg.Content)
		require.NotNil(t, msg.Sender)
		assert.Equal(t, "user-1", msg.Sender.UserID)
		assert.Equal(t, "sender", msg.Sender.Account)
		assert.Equal(t, "寄件者", msg.Sender.ChineseName)
		assert.Equal(t, "Sender Lin", msg.Sender.EngName)
	})
}

func TestBuildClientMessage(t *testing.T) {
	msg := &model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
		Content: "hello", CreatedAt: time.Now(),
	}

	t.Run("user found", func(t *testing.T) {
		users := map[string]model.User{
			"alice": {ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"},
		}
		cm := buildClientMessage(msg, users)
		assert.Equal(t, "m1", cm.ID)
		require.NotNil(t, cm.Sender)
		assert.Equal(t, "u1", cm.Sender.UserID)
		assert.Equal(t, "alice", cm.Sender.Account)
		assert.Equal(t, "愛麗絲", cm.Sender.ChineseName)
		assert.Equal(t, "Alice Wang", cm.Sender.EngName)
	})

	t.Run("user not found", func(t *testing.T) {
		cm := buildClientMessage(msg, map[string]model.User{})
		require.NotNil(t, cm.Sender)
		assert.Equal(t, "alice", cm.Sender.ChineseName)
		assert.Equal(t, "alice", cm.Sender.EngName)
	})
}

func TestHandler_FetchAndUpdateRoom_Missing(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)

	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}

	us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil) // single combined lookup
	store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "ghost-room", "msg-1", msgTime, false).
		Return(fmt.Errorf("update room last message ghost-room: %w", mongo.ErrNoDocuments))

	keyStore := NewMockRoomKeyProvider(ctrl)
	h := NewHandler(store, us, pub, keyStore, true)

	err := h.HandleMessage(context.Background(), makeMessageEvent("ghost-room", "hello", msgTime))
	require.Error(t, err)
	require.ErrorIs(t, err, mongo.ErrNoDocuments)
	assert.Empty(t, pub.records)
}

func TestHandleUpdated_ChannelRoomScopedPublish(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	roomID := "r1"
	room := &model.Room{ID: roomID, Type: model.RoomTypeChannel, SiteID: "site-a"}
	store.EXPECT().GetRoom(gomock.Any(), roomID).Return(room, nil)

	edited := time.Date(2026, 5, 14, 12, 5, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventUpdated,
		SiteID:    "site-a",
		Timestamp: edited.UnixMilli(),
		Message: model.Message{
			ID:          "msg-1",
			RoomID:      roomID,
			UserID:      "u-alice",
			UserAccount: "alice",
			Content:     "updated content",
			CreatedAt:   time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
			EditedAt:    &edited,
			UpdatedAt:   &edited,
		},
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, false)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 1, "channel: single room-scoped publish")
	c := pub.records[0]
	assert.Equal(t, subject.RoomEvent(roomID), c.subject)
	var roomEvt model.EditRoomEvent
	require.NoError(t, json.Unmarshal(c.data, &roomEvt))
	assert.Equal(t, model.RoomEventMessageEdited, roomEvt.Type)
	assert.Equal(t, roomID, roomEvt.RoomID)
	assert.Equal(t, "site-a", roomEvt.SiteID)
	assert.Equal(t, "msg-1", roomEvt.MessageID)
	assert.Equal(t, "updated content", roomEvt.NewContent)
	assert.Empty(t, roomEvt.EncryptedNewContent)
	assert.Equal(t, "alice", roomEvt.EditedBy)
	assert.True(t, roomEvt.EditedAt.Equal(edited))
	assert.True(t, roomEvt.UpdatedAt.Equal(edited))
	assert.Equal(t, edited.UnixMilli(), roomEvt.Timestamp, "Timestamp must propagate from evt.Timestamp, not time.Now()")
}

func TestHandleUpdated_EncryptedChannel_EncryptsContent(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	roomID := "r1"
	room := &model.Room{ID: roomID, Type: model.RoomTypeChannel, SiteID: "site-a"}
	key := testRoomKey(t)
	store.EXPECT().GetRoom(gomock.Any(), roomID).Return(room, nil)
	keyStore.EXPECT().Get(gomock.Any(), roomID).Return(key, nil)

	edited := time.Date(2026, 5, 14, 12, 5, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventUpdated,
		SiteID:    "site-a",
		Timestamp: edited.UnixMilli(),
		Message: model.Message{
			ID: "msg-1", RoomID: roomID, UserID: "u-alice", UserAccount: "alice",
			Content:   "secret edit",
			CreatedAt: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
			EditedAt:  &edited, UpdatedAt: &edited,
		},
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 1, "channel: single room-scoped publish")
	c := pub.records[0]
	assert.Equal(t, subject.RoomEvent(roomID), c.subject)
	var roomEvt model.EditRoomEvent
	require.NoError(t, json.Unmarshal(c.data, &roomEvt))
	assert.Empty(t, roomEvt.NewContent, "plaintext must be cleared when encrypted")
	require.NotEmpty(t, roomEvt.EncryptedNewContent)
	var env roomcrypto.EncryptedMessage
	require.NoError(t, json.Unmarshal(roomEvt.EncryptedNewContent, &env))
	assert.Equal(t, key.Version, env.Version)
	plaintext, err := decryptForTest(&env, key.KeyPair.PrivateKey)
	require.NoError(t, err)
	assert.Equal(t, "secret edit", plaintext)
}

func TestHandleUpdated_MissingEditedAt_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	evt := model.MessageEvent{
		Event: model.EventUpdated,
		Message: model.Message{
			ID: "msg-1", RoomID: "r1", UserAccount: "alice", Content: "x",
		},
		// EditedAt / UpdatedAt deliberately nil
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, true)
	err = h.HandleMessage(context.Background(), data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing EditedAt")
	assert.Empty(t, pub.records)
}

func TestHandleDeleted_ChannelRoomScopedPublish(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	roomID := "r1"
	room := &model.Room{ID: roomID, Type: model.RoomTypeChannel, SiteID: "site-a"}
	store.EXPECT().GetRoom(gomock.Any(), roomID).Return(room, nil)

	deletedAt := time.Date(2026, 5, 14, 12, 10, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventDeleted,
		SiteID:    "site-a",
		Timestamp: deletedAt.UnixMilli(),
		Message: model.Message{
			ID:          "msg-1",
			RoomID:      roomID,
			UserID:      "u-alice",
			UserAccount: "alice",
			CreatedAt:   time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
			UpdatedAt:   &deletedAt,
		},
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 1, "channel: single room-scoped publish")
	c := pub.records[0]
	assert.Equal(t, subject.RoomEvent(roomID), c.subject)
	var roomEvt model.DeleteRoomEvent
	require.NoError(t, json.Unmarshal(c.data, &roomEvt))
	assert.Equal(t, model.RoomEventMessageDeleted, roomEvt.Type)
	assert.Equal(t, roomID, roomEvt.RoomID)
	assert.Equal(t, "site-a", roomEvt.SiteID)
	assert.Equal(t, "msg-1", roomEvt.MessageID)
	assert.Equal(t, "alice", roomEvt.DeletedBy)
	assert.True(t, roomEvt.DeletedAt.Equal(deletedAt))
	assert.True(t, roomEvt.UpdatedAt.Equal(deletedAt))
	assert.Equal(t, deletedAt.UnixMilli(), roomEvt.Timestamp, "Timestamp must propagate from evt.Timestamp, not time.Now()")
}

func TestHandleDeleted_MissingUpdatedAt_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	evt := model.MessageEvent{
		Event: model.EventDeleted,
		Message: model.Message{
			ID: "msg-1", RoomID: "r1", UserAccount: "alice",
		},
		// UpdatedAt deliberately nil
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, true)
	err = h.HandleMessage(context.Background(), data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing UpdatedAt")
	assert.Empty(t, pub.records)
}

func TestHandleUpdated_DMRoom_FansOutToBothMembers(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	roomID := "dm-alice-bob"
	room := &model.Room{
		ID:       roomID,
		Type:     model.RoomTypeDM,
		SiteID:   "site-a",
		Accounts: []string{"alice", "bob"},
	}
	store.EXPECT().GetRoom(gomock.Any(), roomID).Return(room, nil)

	edited := time.Date(2026, 5, 14, 12, 5, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventUpdated,
		SiteID:    "site-a",
		Timestamp: edited.UnixMilli(),
		Message: model.Message{
			ID:          "msg-1",
			RoomID:      roomID,
			UserID:      "u-alice",
			UserAccount: "alice",
			Content:     "updated content",
			CreatedAt:   time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
			EditedAt:    &edited,
			UpdatedAt:   &edited,
		},
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 2, "per-user fan-out: one publish per DM member")
	subjects := map[string]bool{}
	for _, c := range pub.records {
		subjects[c.subject] = true
		var roomEvt model.EditRoomEvent
		require.NoError(t, json.Unmarshal(c.data, &roomEvt))
		assert.Equal(t, model.RoomEventMessageEdited, roomEvt.Type)
		assert.Equal(t, roomID, roomEvt.RoomID)
		assert.Equal(t, "site-a", roomEvt.SiteID)
		assert.Equal(t, "msg-1", roomEvt.MessageID)
		assert.Equal(t, "updated content", roomEvt.NewContent)
		assert.Equal(t, "alice", roomEvt.EditedBy)
		assert.True(t, roomEvt.EditedAt.Equal(edited))
		assert.True(t, roomEvt.UpdatedAt.Equal(edited))
		assert.Equal(t, edited.UnixMilli(), roomEvt.Timestamp, "Timestamp must propagate from evt.Timestamp, not time.Now()")
	}
	assert.True(t, subjects[subject.UserRoomEvent("alice")])
	assert.True(t, subjects[subject.UserRoomEvent("bob")])
}

func TestHandleDeleted_DMRoom_FansOutToBothMembers(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	roomID := "dm-alice-bob"
	room := &model.Room{
		ID:       roomID,
		Type:     model.RoomTypeDM,
		SiteID:   "site-a",
		Accounts: []string{"alice", "bob"},
	}
	store.EXPECT().GetRoom(gomock.Any(), roomID).Return(room, nil)

	deletedAt := time.Date(2026, 5, 14, 12, 10, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventDeleted,
		SiteID:    "site-a",
		Timestamp: deletedAt.UnixMilli(),
		Message: model.Message{
			ID:          "msg-1",
			RoomID:      roomID,
			UserID:      "u-alice",
			UserAccount: "alice",
			CreatedAt:   time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
			UpdatedAt:   &deletedAt,
		},
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 2, "per-user fan-out: one publish per DM member")
	subjects := map[string]bool{}
	for _, c := range pub.records {
		subjects[c.subject] = true
		var roomEvt model.DeleteRoomEvent
		require.NoError(t, json.Unmarshal(c.data, &roomEvt))
		assert.Equal(t, model.RoomEventMessageDeleted, roomEvt.Type)
		assert.Equal(t, roomID, roomEvt.RoomID)
		assert.Equal(t, "site-a", roomEvt.SiteID)
		assert.Equal(t, "msg-1", roomEvt.MessageID)
		assert.Equal(t, "alice", roomEvt.DeletedBy)
		assert.True(t, roomEvt.DeletedAt.Equal(deletedAt))
		assert.True(t, roomEvt.UpdatedAt.Equal(deletedAt))
		assert.Equal(t, deletedAt.UnixMilli(), roomEvt.Timestamp, "Timestamp must propagate from evt.Timestamp, not time.Now()")
	}
	assert.True(t, subjects[subject.UserRoomEvent("alice")])
	assert.True(t, subjects[subject.UserRoomEvent("bob")])
}

func TestHandleUpdated_BotDMRoom_SkipsBotAccount(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	roomID := "botdm-alice-helper.bot"
	room := &model.Room{
		ID:       roomID,
		Type:     model.RoomTypeBotDM,
		SiteID:   "site-a",
		Accounts: []string{"alice", "helper.bot"},
	}
	store.EXPECT().GetRoom(gomock.Any(), roomID).Return(room, nil)

	edited := time.Date(2026, 5, 14, 12, 5, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventUpdated,
		SiteID:    "site-a",
		Timestamp: edited.UnixMilli(),
		Message: model.Message{
			ID:          "msg-1",
			RoomID:      roomID,
			UserID:      "u-alice",
			UserAccount: "alice",
			Content:     "updated content",
			CreatedAt:   time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
			EditedAt:    &edited,
			UpdatedAt:   &edited,
		},
	}
	data, err := json.Marshal(&evt)
	require.NoError(t, err)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 1, "botDM: only the human recipient gets the live event")
	assert.Equal(t, subject.UserRoomEvent("alice"), pub.records[0].subject)
}

func TestHandler_HandleThreadCreated(t *testing.T) {
	msgTime := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	const parentMsgID = "parent-msg-1"
	const siteID = "site-a"
	const sender = "alice"

	tests := []struct {
		name            string
		content         string
		threadFollowers map[string]struct{}
		metaErr         error
		listErr         error
		userLookupErr   error
		wantSubjects    []string
		wantErrContains string
	}{
		{
			name:            "fans out to thread subscribers excluding sender",
			content:         "hello thread",
			threadFollowers: map[string]struct{}{sender: {}, "bob": {}, "carol": {}},
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("carol"),
			},
		},
		{
			name:            "mentioned non-subscriber included in fan-out",
			content:         "hey @dave",
			threadFollowers: map[string]struct{}{"bob": {}},
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("dave"),
			},
		},
		{
			name:            "mentioned user already a thread subscriber - deduped",
			content:         "hey @bob",
			threadFollowers: map[string]struct{}{"bob": {}},
			wantSubjects:    []string{subject.UserRoomEvent("bob")},
		},
		{
			name:            "only sender in subscriber list - no publish",
			content:         "hello",
			threadFollowers: map[string]struct{}{sender: {}},
			wantSubjects:    nil,
		},
		{
			name:            "empty subscriber list and no mentions - no publish",
			content:         "hello",
			threadFollowers: map[string]struct{}{},
			wantSubjects:    nil,
		},
		{
			name:            "GetRoomMeta error - returns error",
			content:         "hello",
			threadFollowers: map[string]struct{}{"bob": {}},
			metaErr:         errors.New("mongo down"),
			wantErrContains: "get room meta",
		},
		{
			name:            "GetThreadFollowers error - returns error",
			content:         "hello",
			listErr:         errors.New("db error"),
			wantErrContains: "get thread followers",
		},
		{
			name:            "user lookup error - warns and continues, subscriber still notified",
			content:         "hello",
			threadFollowers: map[string]struct{}{"bob": {}},
			userLookupErr:   errors.New("db error"),
			wantSubjects:    []string{subject.UserRoomEvent("bob")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			evt := model.MessageEvent{
				Event:  model.EventCreated,
				SiteID: siteID,
				Message: model.Message{
					ID:                    "reply-1",
					RoomID:                "room-1",
					UserID:                "u-alice",
					UserAccount:           sender,
					Content:               tc.content,
					CreatedAt:             msgTime,
					ThreadParentMessageID: parentMsgID,
					TShow:                 false,
				},
			}
			data, _ := json.Marshal(evt)

			// Call order: GetRoomMeta (always) → GetThreadFollowers (channel only) →
			// early-return if empty fanOut → FindUsersByAccounts → publish.
			// SetSubscriptionMentions is NOT called for channel room TShow=false replies.
			fanOut := threadFanOutAccounts(sender, tc.threadFollowers, mention.Parse(tc.content).Accounts)
			needsUserLookup := tc.listErr == nil && tc.metaErr == nil && len(fanOut) > 0

			if needsUserLookup {
				var expectedLookup []string
				switch tc.content {
				case "hey @dave":
					expectedLookup = []string{sender, "dave"}
				case "hey @bob":
					expectedLookup = []string{sender, "bob"}
				default:
					expectedLookup = []string{sender}
				}
				us.EXPECT().FindUsersByAccounts(gomock.Any(), expectedLookup).Return(nil, tc.userLookupErr)
			}

			switch {
			case tc.metaErr != nil:
				// GetRoomMeta is always first; a meta error short-circuits before GetThreadFollowers.
				store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(roommetacache.Meta{}, tc.metaErr)
			case tc.listErr != nil:
				// GetRoomMeta succeeds (channel room), then GetThreadFollowers errors.
				store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
				store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(nil, tc.listErr)
			default:
				store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
				store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(tc.threadFollowers, nil)
				// SetSubscriptionMentions must NOT be called for TShow=false channel thread replies:
				// the reply is invisible in the main channel, so a channel-room mention badge would
				// show up without any visible message to explain it.
			}

			h := NewHandler(store, us, pub, keyStore, false)
			err := h.HandleMessage(context.Background(), data)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrContains)
				assert.Empty(t, pub.records)
				return
			}

			require.NoError(t, err)
			gotSubjects := make([]string, len(pub.records))
			for i, r := range pub.records {
				gotSubjects[i] = r.subject
			}
			assert.ElementsMatch(t, tc.wantSubjects, gotSubjects)

			for _, r := range pub.records {
				var roomEvt model.RoomEvent
				require.NoError(t, json.Unmarshal(r.data, &roomEvt))
				assert.Equal(t, model.RoomEventNewMessage, roomEvt.Type)
				assert.Equal(t, "room-1", roomEvt.RoomID)
				assert.Equal(t, siteID, roomEvt.SiteID)
				require.NotNil(t, roomEvt.Message)
				assert.Equal(t, "reply-1", roomEvt.Message.ID)
				assert.Equal(t, parentMsgID, roomEvt.Message.ThreadParentMessageID)
			}
		})
	}
}

// TestHandler_HandleThreadCreated_MentionFieldsOnRoomEvent verifies that MentionAll
// and Mentions are correctly populated on the RoomEvent published for a channel thread reply.
func TestHandler_HandleThreadCreated_MentionFieldsOnRoomEvent(t *testing.T) {
	msgTime := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	const parentMsgID = "parent-mention-1"
	const siteID = "site-a"
	carolUser := model.User{ID: "u-carol", Account: "carol", EngName: "Carol Li", ChineseName: "卡羅爾", SiteID: siteID}

	tests := []struct {
		name           string
		content        string
		mockUsers      []model.User
		wantSubjects   []string // all expected publish subjects
		wantMentionAll bool
		wantMentions   []string // expected Account values in every evt.Mentions
	}{
		{
			name:           "@all mention sets MentionAll=true on room event",
			content:        "@all please review",
			mockUsers:      nil,
			wantSubjects:   []string{subject.UserRoomEvent("bob")},
			wantMentionAll: true,
			wantMentions:   []string{"all"},
		},
		{
			name:      "resolved @mention carried on room event",
			content:   "hey @carol",
			mockUsers: []model.User{carolUser},
			// carol is also in fanOut via @-mention, so both bob and carol receive.
			wantSubjects:   []string{subject.UserRoomEvent("bob"), subject.UserRoomEvent("carol")},
			wantMentionAll: false,
			wantMentions:   []string{"carol"},
		},
		{
			name:           "no mentions — MentionAll=false, Mentions empty",
			content:        "plain reply",
			mockUsers:      nil,
			wantSubjects:   []string{subject.UserRoomEvent("bob")},
			wantMentionAll: false,
			wantMentions:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			evt := model.MessageEvent{
				Event:  model.EventCreated,
				SiteID: siteID,
				Message: model.Message{
					ID:                    "reply-m1",
					RoomID:                "room-1",
					UserID:                "u-alice",
					UserAccount:           "alice",
					Content:               tc.content,
					CreatedAt:             msgTime,
					ThreadParentMessageID: parentMsgID,
					TShow:                 false,
				},
			}
			data, _ := json.Marshal(evt)

			store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
			store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(
				map[string]struct{}{"bob": {}}, nil,
			)
			// SetSubscriptionMentions must NOT be called for TShow=false channel thread replies.
			var lookupAccounts []string
			switch {
			case tc.wantMentionAll && (len(tc.wantMentions) == 0 || tc.wantMentions[0] == "all"):
				lookupAccounts = []string{"alice"}
			case len(tc.mockUsers) > 0:
				lookupAccounts = []string{"alice", "carol"}
			default:
				lookupAccounts = []string{"alice"}
			}
			us.EXPECT().FindUsersByAccounts(gomock.Any(), lookupAccounts).Return(tc.mockUsers, nil)

			h := NewHandler(store, us, pub, keyStore, false)
			require.NoError(t, h.HandleMessage(context.Background(), data))

			gotSubjects := make([]string, len(pub.records))
			for i, r := range pub.records {
				gotSubjects[i] = r.subject
			}
			assert.ElementsMatch(t, tc.wantSubjects, gotSubjects)
			require.NotEmpty(t, pub.records, "at least one event expected")

			// Every published event must carry the same correct mention fields.
			for _, r := range pub.records {
				var roomEvt model.RoomEvent
				require.NoError(t, json.Unmarshal(r.data, &roomEvt))
				assert.Equal(t, tc.wantMentionAll, roomEvt.MentionAll, "MentionAll mismatch")
				if len(tc.wantMentions) == 0 {
					assert.Empty(t, roomEvt.Mentions, "expected no Mentions")
				} else {
					require.Len(t, roomEvt.Mentions, len(tc.wantMentions))
					accounts := make([]string, len(roomEvt.Mentions))
					for i, m := range roomEvt.Mentions {
						accounts[i] = m.Account
					}
					assert.ElementsMatch(t, tc.wantMentions, accounts)
				}
			}
		})
	}
}

func TestHandler_HandleThreadCreated_DMRoom(t *testing.T) {
	// Thread replies to DM rooms must always fan out to all DM members via
	// publishDMEvents regardless of thread subscription state (Fix 2).
	msgTime := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	const parentMsgID = "parent-dm-1"
	const siteID = "site-a"
	const sender = "alice"

	tests := []struct {
		name         string
		wantSubjects []string
	}{
		{
			name: "only sender in thread subs - still publishes to all DM members",
			wantSubjects: []string{
				subject.UserRoomEvent("alice"),
				subject.UserRoomEvent("bob"),
			},
		},
		{
			name: "empty thread subs - still publishes to all DM members",
			wantSubjects: []string{
				subject.UserRoomEvent("alice"),
				subject.UserRoomEvent("bob"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			evt := model.MessageEvent{
				Event:  model.EventCreated,
				SiteID: siteID,
				Message: model.Message{
					ID:                    "reply-dm-1",
					RoomID:                "dm-1",
					UserID:                "u-alice",
					UserAccount:           sender,
					Content:               "hello",
					CreatedAt:             msgTime,
					ThreadParentMessageID: parentMsgID,
					TShow:                 false,
				},
			}
			data, _ := json.Marshal(evt)

			store.EXPECT().GetRoomMeta(gomock.Any(), "dm-1").Return(metaOf(testDMRoom), nil)
			store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "dm-1", "reply-dm-1", msgTime, false).Return(nil)
			us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{sender}).Return(nil, nil)
			store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(testDMSubs, nil)

			h := NewHandler(store, us, pub, keyStore, false)
			err := h.HandleMessage(context.Background(), data)

			require.NoError(t, err)
			gotSubjects := make([]string, len(pub.records))
			for i, r := range pub.records {
				gotSubjects[i] = r.subject
			}
			assert.ElementsMatch(t, tc.wantSubjects, gotSubjects)

			for _, r := range pub.records {
				var roomEvt model.RoomEvent
				require.NoError(t, json.Unmarshal(r.data, &roomEvt))
				assert.Equal(t, model.RoomEventNewMessage, roomEvt.Type)
				assert.Equal(t, "dm-1", roomEvt.RoomID)
				assert.Equal(t, siteID, roomEvt.SiteID)
				require.NotNil(t, roomEvt.Message)
				assert.Equal(t, "reply-dm-1", roomEvt.Message.ID)
				assert.Equal(t, parentMsgID, roomEvt.Message.ThreadParentMessageID)
			}
		})
	}
}

func TestHandler_ThreadCreated_TShow_FallsThroughToRoomBroadcast(t *testing.T) {
	msgTime := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID:                    "reply-tshow",
			RoomID:                "room-1",
			UserID:                "u-alice",
			UserAccount:           "alice",
			Content:               "also in channel",
			CreatedAt:             msgTime,
			ThreadParentMessageID: "parent-msg-1",
			TShow:                 true,
		},
	}
	data, _ := json.Marshal(evt)

	key := testRoomKey(t)
	keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)
	us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"alice"}).Return(nil, nil)
	store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "reply-tshow", msgTime, false).Return(nil)
	store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	require.Len(t, pub.records, 1)
	assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)
}

func TestHandler_HandleThreadUpdated(t *testing.T) {
	const parentMsgID = "parent-msg-1"
	const siteID = "site-a"
	const sender = "alice"

	editedAt := time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)

	makeThreadUpdatedEvent := func(tshow bool) (model.MessageEvent, []byte) {
		evt := model.MessageEvent{
			Event:     model.EventUpdated,
			SiteID:    siteID,
			Timestamp: editedAt.UnixMilli(),
			Message: model.Message{
				ID:                    "reply-1",
				RoomID:                "room-1",
				UserID:                "u-alice",
				UserAccount:           sender,
				Content:               "edited reply",
				CreatedAt:             time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
				EditedAt:              &editedAt,
				UpdatedAt:             &editedAt,
				ThreadParentMessageID: parentMsgID,
				TShow:                 tshow,
			},
		}
		data, _ := json.Marshal(evt)
		return evt, data
	}

	tests := []struct {
		name            string
		threadFollowers map[string]struct{}
		tshow           bool
		getRoomErr      error
		listErr         error
		wantSubjects    []string
		wantErrContains string
		wantNewContent  string // empty defaults to "edited reply"
		customData      []byte // non-nil overrides makeThreadUpdatedEvent output
		setupMocks      func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider)
	}{
		{
			name:            "all thread subscribers receive edit event, sender excluded",
			threadFollowers: map[string]struct{}{"bob": {}, "carol": {}},
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("carol"),
			},
		},
		{
			name:            "empty subscriber list → no publish, no error",
			threadFollowers: map[string]struct{}{},
			wantSubjects:    nil,
		},
		{
			name:            "sender in subscriber list is excluded",
			threadFollowers: map[string]struct{}{sender: {}, "bob": {}},
			wantSubjects:    []string{subject.UserRoomEvent("bob")},
		},
		{
			name:            "GetRoom error → error returned, no publish",
			getRoomErr:      errors.New("db error"),
			wantErrContains: "get room",
		},
		{
			name:            "GetThreadFollowers error → error returned, no publish",
			listErr:         errors.New("db error"),
			wantErrContains: "get thread followers for parent",
		},
		{
			name: "DM room → edit fans out to all members",
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{
					ID: "room-1", Type: model.RoomTypeDM, SiteID: siteID,
					Accounts: []string{"alice", "bob"},
				}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				// GetThreadFollowers must NOT be called for DM rooms.
			},
			wantSubjects: []string{
				subject.UserRoomEvent("alice"),
				subject.UserRoomEvent("bob"),
			},
		},
		{
			name: "BotDM room → edit reaches human, bot skipped",
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{
					ID: "room-1", Type: model.RoomTypeBotDM, SiteID: siteID,
					Accounts: []string{"alice", "helper.bot"},
				}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
			},
			wantSubjects: []string{subject.UserRoomEvent("alice")},
		},
		{
			name:  "TShow=true → falls through to room broadcast, thread handler not called",
			tshow: true,
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				// GetThreadFollowers must NOT be called
			},
			wantSubjects: []string{subject.RoomEvent("room-1")},
		},
		{
			name: "@mentioned non-subscriber receives edit broadcast",
			customData: func() []byte {
				at := editedAt
				evt := model.MessageEvent{
					Event:     model.EventUpdated,
					SiteID:    siteID,
					Timestamp: editedAt.UnixMilli(),
					Message: model.Message{
						ID:                    "reply-1",
						RoomID:                "room-1",
						UserID:                "u-alice",
						UserAccount:           sender,
						Content:               "hey @dave",
						CreatedAt:             time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
						EditedAt:              &at,
						UpdatedAt:             &at,
						ThreadParentMessageID: parentMsgID,
						TShow:                 false,
					},
				}
				b, _ := json.Marshal(evt)
				return b
			}(),
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(
					map[string]struct{}{"bob": {}}, nil,
				)
			},
			wantSubjects:   []string{subject.UserRoomEvent("bob"), subject.UserRoomEvent("dave")},
			wantNewContent: "hey @dave",
		},
		{
			name: "nil EditedAt returns error without panicking",
			customData: func() []byte {
				evt := model.MessageEvent{
					Event:  model.EventUpdated,
					SiteID: siteID,
					Message: model.Message{
						ID:                    "reply-1",
						RoomID:                "room-1",
						UserAccount:           sender,
						ThreadParentMessageID: parentMsgID,
						TShow:                 false,
						// EditedAt deliberately nil
					},
				}
				b, _ := json.Marshal(evt)
				return b
			}(),
			// No setupMocks — nil guard fires before any store call.
			wantErrContains: "missing EditedAt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			var data []byte
			if tc.customData != nil {
				data = tc.customData
			} else {
				_, data = makeThreadUpdatedEvent(tc.tshow)
			}

			if tc.setupMocks != nil {
				tc.setupMocks(store, us, keyStore)
			} else if tc.customData == nil {
				// Channel-room cases. GetRoom is fetched first so the handler
				// can route by room type; GetThreadFollowers follows only
				// for channel rooms.
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				switch {
				case tc.getRoomErr != nil:
					store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(nil, tc.getRoomErr)
				case tc.listErr != nil:
					store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
					store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(nil, tc.listErr)
				default:
					store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
					store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(tc.threadFollowers, nil)
				}
			}

			h := NewHandler(store, us, pub, keyStore, false)
			err := h.HandleMessage(context.Background(), data)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrContains)
				assert.Empty(t, pub.records)
				return
			}

			require.NoError(t, err)
			gotSubjects := make([]string, len(pub.records))
			for i, r := range pub.records {
				gotSubjects[i] = r.subject
			}
			assert.ElementsMatch(t, tc.wantSubjects, gotSubjects)

			wantContent := tc.wantNewContent
			if wantContent == "" {
				wantContent = "edited reply"
			}
			for _, r := range pub.records {
				var roomEvt model.EditRoomEvent
				require.NoError(t, json.Unmarshal(r.data, &roomEvt))
				assert.Equal(t, model.RoomEventMessageEdited, roomEvt.Type)
				assert.Equal(t, "room-1", roomEvt.RoomID)
				assert.Equal(t, siteID, roomEvt.SiteID)
				assert.Equal(t, "reply-1", roomEvt.MessageID)
				assert.Equal(t, wantContent, roomEvt.NewContent)
				assert.Equal(t, sender, roomEvt.EditedBy)
				assert.True(t, roomEvt.EditedAt.Equal(editedAt))
				assert.True(t, roomEvt.UpdatedAt.Equal(editedAt))
				assert.Equal(t, editedAt.UnixMilli(), roomEvt.Timestamp, "Timestamp must propagate from evt.Timestamp, not time.Now()")
			}
		})
	}
}

func TestHandler_HandleThreadDeleted(t *testing.T) {
	const parentMsgID = "parent-msg-1"
	const siteID = "site-a"
	const sender = "alice"

	deletedAt := time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC)

	makeThreadDeletedEvent := func(tshow bool, newTCount *int) (model.MessageEvent, []byte) {
		evt := model.MessageEvent{
			Event:     model.EventDeleted,
			SiteID:    siteID,
			Timestamp: deletedAt.UnixMilli(),
			NewTCount: newTCount,
			Message: model.Message{
				ID:                    "reply-1",
				RoomID:                "room-1",
				UserID:                "u-alice",
				UserAccount:           sender,
				CreatedAt:             time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
				UpdatedAt:             &deletedAt,
				ThreadParentMessageID: parentMsgID,
				TShow:                 tshow,
			},
		}
		data, _ := json.Marshal(evt)
		return evt, data
	}

	tests := []struct {
		name            string
		threadFollowers map[string]struct{}
		tshow           bool
		listErr         error
		wantSubjects    []string
		wantErrContains string
		customData      []byte // non-nil overrides makeThreadDeletedEvent output
		setupMocks      func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider)
		newTCount       *int // when set, the event has NewTCount
		skipPayloadLoop bool // skip the DeleteRoomEvent payload loop for tests with mixed event types
	}{
		{
			name:            "all thread subscribers receive the delete event, sender excluded",
			threadFollowers: map[string]struct{}{"bob": {}, "carol": {}},
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("carol"),
			},
		},
		{
			name:            "sender in subscriber list is excluded",
			threadFollowers: map[string]struct{}{sender: {}, "bob": {}},
			wantSubjects:    []string{subject.UserRoomEvent("bob")},
		},
		{
			name:            "empty subscriber list → no publish, no error",
			threadFollowers: map[string]struct{}{},
			wantSubjects:    nil,
		},
		{
			name:            "GetThreadFollowers error → error returned, no publish",
			listErr:         errors.New("db error"),
			wantErrContains: "get thread followers for parent",
		},
		{
			name:  "TShow=true → falls through to room broadcast, GetThreadFollowers NOT called",
			tshow: true,
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				// GetThreadFollowers must NOT be called
			},
			wantSubjects: []string{subject.RoomEvent("room-1")},
		},
		{
			name:      "TShow=true with NewTCount publishes room delete AND badge event",
			tshow:     true,
			newTCount: func() *int { v := 2; return &v }(),
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				// GetThreadFollowers must NOT be called (TShow=true uses room broadcast)
			},
			wantSubjects: []string{
				subject.RoomEvent("room-1"), // room delete event
				subject.RoomEvent("room-1"), // badge metadata event
			},
			skipPayloadLoop: true,
		},
		{
			name: "nil UpdatedAt returns error without panicking",
			customData: func() []byte {
				evt := model.MessageEvent{
					Event:  model.EventDeleted,
					SiteID: siteID,
					Message: model.Message{
						ID:                    "reply-1",
						RoomID:                "room-1",
						UserAccount:           sender,
						ThreadParentMessageID: parentMsgID,
						TShow:                 false,
						// UpdatedAt deliberately nil
					},
				}
				b, _ := json.Marshal(evt)
				return b
			}(),
			// No setupMocks — nil guard fires before any store call.
			wantErrContains: "missing UpdatedAt",
		},
		{
			name:            "empty subscriber list with NewTCount publishes badge event to channel",
			threadFollowers: map[string]struct{}{},
			newTCount:       func() *int { v := 3; return &v }(),
			wantSubjects:    []string{subject.RoomEvent("room-1")},
			skipPayloadLoop: true,
		},
		{
			name:            "thread subscribers + NewTCount publishes both delete and badge events",
			threadFollowers: map[string]struct{}{"bob": {}},
			newTCount:       func() *int { v := 2; return &v }(),
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.RoomEvent("room-1"),
			},
			skipPayloadLoop: true,
		},
		{
			name:            "DM room with NewTCount fans out delete and badge to all members",
			skipPayloadLoop: true,
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{
					ID:       "room-1",
					Type:     model.RoomTypeDM,
					SiteID:   siteID,
					Accounts: []string{"alice", "bob"},
				}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				// GetThreadFollowers must NOT be called for DM rooms.
			},
			newTCount: func() *int { v := 1; return &v }(),
			wantSubjects: []string{
				subject.UserRoomEvent("alice"),
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("alice"),
				subject.UserRoomEvent("bob"),
			},
		},
		{
			name: "DM room without NewTCount fans out delete to all members",
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{
					ID:       "room-1",
					Type:     model.RoomTypeDM,
					SiteID:   siteID,
					Accounts: []string{"alice", "bob"},
				}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
			},
			wantSubjects: []string{
				subject.UserRoomEvent("alice"),
				subject.UserRoomEvent("bob"),
			},
		},
		{
			name: "BotDM room delete reaches human, bot skipped",
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{
					ID:       "room-1",
					Type:     model.RoomTypeBotDM,
					SiteID:   siteID,
					Accounts: []string{"alice", "helper.bot"},
				}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
			},
			wantSubjects: []string{subject.UserRoomEvent("alice")},
		},
		{
			name:            "GetRoom error stops all publishing",
			newTCount:       func() *int { v := 2; return &v }(),
			wantErrContains: "get room",
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(nil, errors.New("mongo: connection refused"))
				// GetRoom is the first store call; GetThreadFollowers never runs.
			},
		},
		{
			name: "unknown room type with NewTCount — badge fires but publishes nothing",
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{ID: "room-1", Type: "unknown", SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				// GetThreadFollowers must NOT be called for unknown room types.
				// publishThreadBadge is called but publishThreadMetadata's own default
				// branch logs a warning and publishes nothing.
			},
			newTCount:       func() *int { v := 3; return &v }(),
			wantSubjects:    nil,
			skipPayloadLoop: true,
		},
		{
			name: "@mentioned non-subscriber receives delete event",
			customData: func() []byte {
				evt := model.MessageEvent{
					Event:  model.EventDeleted,
					SiteID: siteID,
					Message: model.Message{
						ID:                    "reply-1",
						RoomID:                "room-1",
						UserID:                "u-alice",
						UserAccount:           sender,
						Content:               "check this @dave",
						CreatedAt:             time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
						UpdatedAt:             &deletedAt,
						ThreadParentMessageID: parentMsgID,
						TShow:                 false,
					},
				}
				b, _ := json.Marshal(evt)
				return b
			}(),
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(
					map[string]struct{}{"bob": {}}, nil,
				)
			},
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("dave"),
			},
			skipPayloadLoop: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			var data []byte
			if tc.customData != nil {
				data = tc.customData
			} else {
				_, data = makeThreadDeletedEvent(tc.tshow, tc.newTCount)
			}

			if tc.setupMocks != nil {
				tc.setupMocks(store, us, keyStore)
			} else if tc.customData == nil {
				// Channel-room cases. GetRoom is fetched first to route by room
				// type; GetThreadFollowers follows for channel rooms.
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
				switch {
				case tc.listErr != nil:
					store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(nil, tc.listErr)
				default:
					store.EXPECT().GetThreadFollowers(gomock.Any(), parentMsgID).Return(tc.threadFollowers, nil)
				}
			}

			h := NewHandler(store, us, pub, keyStore, false)
			err := h.HandleMessage(context.Background(), data)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrContains)
				assert.Empty(t, pub.records)
				return
			}

			require.NoError(t, err)
			gotSubjects := make([]string, len(pub.records))
			for i, r := range pub.records {
				gotSubjects[i] = r.subject
			}
			assert.ElementsMatch(t, tc.wantSubjects, gotSubjects)

			if !tc.skipPayloadLoop {
				for _, r := range pub.records {
					var roomEvt model.DeleteRoomEvent
					require.NoError(t, json.Unmarshal(r.data, &roomEvt))
					assert.Equal(t, model.RoomEventMessageDeleted, roomEvt.Type)
					assert.Equal(t, "room-1", roomEvt.RoomID)
					assert.Equal(t, siteID, roomEvt.SiteID)
					assert.Equal(t, "reply-1", roomEvt.MessageID)
					assert.Equal(t, sender, roomEvt.DeletedBy)
					assert.True(t, roomEvt.DeletedAt.Equal(deletedAt))
					assert.True(t, roomEvt.UpdatedAt.Equal(deletedAt))
					assert.Equal(t, deletedAt.UnixMilli(), roomEvt.Timestamp, "Timestamp must propagate from evt.Timestamp, not time.Now()")
				}
			}
		})
	}
}

func TestHandler_HandleMessage_EventThreadReplyAdded(t *testing.T) {
	const parentMsgID = "parent-1"
	const siteID = "site-a"

	makeThreadReplyAddedEvent := func(roomID, parentID, replyID string, newTCount *int) []byte {
		evt := model.MessageEvent{
			Event:  model.EventThreadReplyAdded,
			SiteID: siteID,
			Message: model.Message{
				ID:                    replyID,
				RoomID:                roomID,
				ThreadParentMessageID: parentID,
			},
			NewTCount: newTCount,
		}
		data, _ := json.Marshal(evt)
		return data
	}

	tests := []struct {
		name            string
		roomID          string
		newTCount       *int
		wantSubjects    []string
		wantErrContains string
		setupMocks      func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider)
	}{
		{
			name:         "nil NewTCount — no publish, no GetRoom call",
			roomID:       "room-1",
			newTCount:    nil,
			wantSubjects: nil,
			// setupMocks nil: no store calls expected
		},
		{
			name:         "channel room — publishes ThreadMetadataUpdatedEvent to RoomEvent",
			roomID:       "room-1",
			newTCount:    func() *int { v := 5; return &v }(),
			wantSubjects: []string{subject.RoomEvent("room-1")},
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
			},
		},
		{
			name:      "DM room — publishes to each member's UserRoomEvent skipping bots",
			roomID:    "dm-1",
			newTCount: func() *int { v := 3; return &v }(),
			wantSubjects: []string{
				subject.UserRoomEvent("alice"),
				subject.UserRoomEvent("bob"),
			},
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				room := &model.Room{
					ID:       "dm-1",
					Type:     model.RoomTypeDM,
					SiteID:   siteID,
					Accounts: []string{"alice", "bob", "helper.bot"},
				}
				store.EXPECT().GetRoom(gomock.Any(), "dm-1").Return(room, nil)
			},
		},
		{
			name:            "GetRoom error — returns error",
			roomID:          "room-1",
			newTCount:       func() *int { v := 2; return &v }(),
			wantErrContains: "get room",
			setupMocks: func(store *MockStore, us *MockUserStore, keyStore *MockRoomKeyProvider) {
				store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(nil, errors.New("mongo down"))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			data := makeThreadReplyAddedEvent(tc.roomID, parentMsgID, "reply-1", tc.newTCount)

			if tc.setupMocks != nil {
				tc.setupMocks(store, us, keyStore)
			}

			h := NewHandler(store, us, pub, keyStore, false)
			err := h.HandleMessage(context.Background(), data)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrContains)
				assert.Empty(t, pub.records)
				return
			}

			require.NoError(t, err)
			gotSubjects := make([]string, len(pub.records))
			for i, r := range pub.records {
				gotSubjects[i] = r.subject
			}
			assert.ElementsMatch(t, tc.wantSubjects, gotSubjects)

			if tc.newTCount != nil && len(tc.wantSubjects) > 0 {
				// Verify the channel room publish carries a valid ThreadMetadataUpdatedEvent.
				// For DM rooms, each publish uses the same payload so check the first record.
				var tmeEvt model.ThreadMetadataUpdatedEvent
				require.NoError(t, json.Unmarshal(pub.records[0].data, &tmeEvt))
				assert.Equal(t, model.RoomEventThreadMetadataUpdated, tmeEvt.Type)
				assert.Equal(t, tc.roomID, tmeEvt.RoomID)
				assert.Equal(t, siteID, tmeEvt.SiteID)
				assert.Equal(t, parentMsgID, tmeEvt.ParentMessageID)
				assert.Equal(t, "reply-1", tmeEvt.ReplyMessageID)
				assert.Equal(t, *tc.newTCount, tmeEvt.NewTCount)
				assert.Equal(t, model.ThreadActionReplyAdded, tmeEvt.Action)
				assert.NotZero(t, tmeEvt.Timestamp)
			}
		})
	}
}

func TestHandler_HandleMessage_EventThreadReplyAdded_PublishError_Propagated(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	keyStore := NewMockRoomKeyProvider(ctrl)

	room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: "site-a"}
	store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)

	// failAfter: 0 → fails on the very first publish call.
	failPub := &failingPublisher{failAfter: 0}

	newTCount := 5
	evt := model.MessageEvent{
		Event:  model.EventThreadReplyAdded,
		SiteID: "site-a",
		Message: model.Message{
			ID:                    "reply-1",
			RoomID:                "room-1",
			ThreadParentMessageID: "parent-1",
		},
		NewTCount: &newTCount,
	}
	data, _ := json.Marshal(evt)

	h := NewHandler(store, us, failPub, keyStore, false)
	err := h.HandleMessage(context.Background(), data)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish failed")
}

func TestHandler_HandleMessage_ChannelEncryptionDisabled(t *testing.T) {
	msgTime := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	senderUser := model.User{ID: "u-sender", Account: "sender", EngName: "Sender Lin", ChineseName: "寄件者", SiteID: "site-a"}

	tests := []struct {
		name            string
		content         string
		wantMentionAll  bool
		wantMentions    []string
		wantSetMentions []string
	}{
		{
			name:           "plaintext no mentions",
			content:        "hello group",
			wantMentionAll: false,
		},
		{
			name:            "plaintext individual mention",
			content:         "hey @alice",
			wantMentions:    []string{"alice"},
			wantSetMentions: []string{"alice"},
		},
		{
			name:           "plaintext mention all",
			content:        "attention @all",
			wantMentionAll: true,
			wantMentions:   []string{"all"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}

			store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "msg-1", msgTime, tc.wantMentionAll).Return(nil)
			store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
			if tc.wantSetMentions != nil {
				store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", gomock.InAnyOrder(tc.wantSetMentions)).Return(nil)
			}

			if tc.name == "plaintext individual mention" {
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender", "alice"}).
					Return([]model.User{senderUser, testUsers[0]}, nil)
			} else {
				us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).
					Return([]model.User{senderUser}, nil)
			}

			// nil keyStore — handler must NOT dereference it when encrypt=false
			h := NewHandler(store, us, pub, nil, false)
			err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", tc.content, msgTime))
			require.NoError(t, err)

			require.Len(t, pub.records, 1)
			assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)

			var evt model.RoomEvent
			require.NoError(t, json.Unmarshal(pub.records[0].data, &evt))
			require.NotNil(t, evt.Message, "plaintext channel events must carry Message")
			assert.Empty(t, evt.EncryptedMessage, "plaintext channel events must NOT carry EncryptedMessage")
			assert.Equal(t, "msg-1", evt.Message.ID)
			require.NotNil(t, evt.Message.Sender)
			assert.Equal(t, "sender", evt.Message.Sender.Account)
			assert.Equal(t, tc.wantMentionAll, evt.MentionAll)

			if tc.wantMentions != nil {
				accounts := make([]string, len(evt.Mentions))
				for i, m := range evt.Mentions {
					accounts[i] = m.Account
				}
				assert.ElementsMatch(t, tc.wantMentions, accounts)
			} else {
				assert.Empty(t, evt.Mentions)
			}
		})
	}
}

func TestThreadFanOutAccounts(t *testing.T) {
	tests := []struct {
		name          string
		senderAccount string
		followers     map[string]struct{}
		extraAccounts []string
		want          []string
	}{
		{
			name:          "sender in followers is excluded",
			senderAccount: "alice",
			followers:     map[string]struct{}{"alice": {}, "bob": {}},
			want:          []string{"bob"},
		},
		{
			name:          "duplicate via extra accounts is deduped",
			senderAccount: "alice",
			followers:     map[string]struct{}{"bob": {}, "carol": {}},
			want:          []string{"bob", "carol"},
		},
		{
			name:          "extra account not in followers is included",
			senderAccount: "alice",
			followers:     map[string]struct{}{"bob": {}},
			extraAccounts: []string{"dave"},
			want:          []string{"bob", "dave"},
		},
		{
			name:          "extra account already in followers is not duplicated",
			senderAccount: "alice",
			followers:     map[string]struct{}{"bob": {}},
			extraAccounts: []string{"bob"},
			want:          []string{"bob"},
		},
		{
			name:          "empty followers and no extras returns nil",
			senderAccount: "alice",
			followers:     map[string]struct{}{},
			extraAccounts: nil,
			want:          nil,
		},
		{
			name:          "bot account in followers is excluded",
			senderAccount: "alice",
			followers:     map[string]struct{}{"bob": {}, "helper.bot": {}},
			want:          []string{"bob"},
		},
		{
			name:          "bot account in extra accounts is excluded",
			senderAccount: "alice",
			followers:     map[string]struct{}{"bob": {}},
			extraAccounts: []string{"agent.bot", "carol"},
			want:          []string{"bob", "carol"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := threadFanOutAccounts(tc.senderAccount, tc.followers, tc.extraAccounts)
			assert.Equal(t, tc.want, got)
		})
	}
}

// errorPublisher returns a fixed error on every Publish call.
type errorPublisher struct{ err error }

func (e *errorPublisher) Publish(_ context.Context, _ string, _ []byte) error { return e.err }

func TestHandler_HandleThreadCreated_PublishError_PropagatesForJetStreamRetry(t *testing.T) {
	// publishToThreadAccounts must return an error so the caller propagates it to JetStream
	// for redelivery — thread events otherwise lose delivery guarantees that non-thread
	// channel events get via publishMutation.
	msgTime := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	keyStore := NewMockRoomKeyProvider(ctrl)
	pubErr := errors.New("nats: connection closed")
	pub := &errorPublisher{err: pubErr}

	store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
	store.EXPECT().GetThreadFollowers(gomock.Any(), "parent-1").
		Return(map[string]struct{}{"bob": {}}, nil)
	us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return(nil, nil)

	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID:                    "reply-1",
			RoomID:                "room-1",
			UserID:                "u-sender",
			UserAccount:           "sender",
			Content:               "hello",
			CreatedAt:             msgTime,
			ThreadParentMessageID: "parent-1",
			TShow:                 false,
		},
	}
	data, _ := json.Marshal(evt)

	h := NewHandler(store, us, pub, keyStore, false)
	err := h.HandleMessage(context.Background(), data)

	require.Error(t, err, "publish failure must propagate so JetStream retries")
	assert.ErrorIs(t, err, pubErr)
}

func TestHandler_HandleThreadTCountUpdated_EmptyParentMsgID_Skips(t *testing.T) {
	// handleThreadTCountUpdated must guard against an empty ThreadParentMessageID:
	// publishing a ThreadMetadataUpdatedEvent with ParentMessageID="" would corrupt
	// client badge state since the event cannot be correlated to any thread.
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	newTCount := 3
	evt := model.MessageEvent{
		Event:  model.EventThreadReplyAdded,
		SiteID: "site-a",
		Message: model.Message{
			ID:                    "reply-1",
			RoomID:                "room-1",
			ThreadParentMessageID: "", // malformed: empty parent ID
		},
		NewTCount: &newTCount,
	}
	data, _ := json.Marshal(evt)

	// GetRoom must NOT be called when ThreadParentMessageID is empty.

	h := NewHandler(store, us, pub, keyStore, false)
	err := h.HandleMessage(context.Background(), data)

	require.NoError(t, err)
	assert.Empty(t, pub.records, "no event must be published for malformed thread reply")
}
