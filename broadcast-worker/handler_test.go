package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomcrypto"
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
	testGroupRoom = &model.Room{
		ID: "room-1", Name: "general", Type: model.RoomTypeGroup,
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
	testEmployees = []model.Employee{
		{AccountName: "alice", Name: "愛麗絲", EngName: "Alice Wang"},
		{AccountName: "bob", Name: "鮑勃", EngName: "Bob Chen"},
	}
)

func makeMessageEvent(roomID, content string, msgTime time.Time) []byte {
	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: roomID, UserID: "user-1", UserAccount: "sender",
			Content: content, CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)
	return data
}

func expectEmployeeLookup(store *MockStore, accountNames []string, employees []model.Employee) {
	store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.InAnyOrder(accountNames)).Return(employees, nil)
}

func TestHandler_HandleMessage_GroupRoom(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		content         string
		wantMentionAll  bool
		wantMentions    []string
		wantSetMentions bool
	}{
		{
			name:            "no mentions",
			content:         "hello group",
			wantMentionAll:  false,
			wantMentions:    nil,
			wantSetMentions: false,
		},
		{
			name:            "individual mentions",
			content:         "hey @alice and @bob",
			wantMentionAll:  false,
			wantMentions:    []string{"alice", "bob"},
			wantSetMentions: true,
		},
		{
			name:            "mention all case insensitive",
			content:         "attention @all",
			wantMentionAll:  true,
			wantMentions:    nil,
			wantSetMentions: false,
		},
		{
			name:            "mention all and individual",
			content:         "@All and @alice",
			wantMentionAll:  true,
			wantMentions:    []string{"alice"},
			wantSetMentions: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			pub := &mockPublisher{}

			key := testRoomKey(t)
			keyStore := NewMockRoomKeyProvider(ctrl)
			keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

			store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
			store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, tc.wantMentionAll).Return(nil)

			if tc.wantSetMentions {
				store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", gomock.InAnyOrder(tc.wantMentions)).Return(nil)
			}

			// Employee lookup expectations per test case
			switch tc.name {
			case "no mentions":
				expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})
			case "individual mentions":
				expectEmployeeLookup(store, []string{"sender", "alice", "bob"}, append([]model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}}, testEmployees...))
			case "mention all case insensitive":
				expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})
			case "mention all and individual":
				expectEmployeeLookup(store, []string{"sender", "alice"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}, testEmployees[0]})
			}

			h := NewHandler(store, pub, keyStore)
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
				for _, m := range evt.Mentions {
					assert.Empty(t, m.UserID, "mention participants should not have userID")
					assert.NotEmpty(t, m.ChineseName)
					assert.NotEmpty(t, m.EngName)
				}
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
			pub := &mockPublisher{}

			evt := model.MessageEvent{
				SiteID: "site-a",
				Message: model.Message{
					ID: "msg-1", RoomID: "dm-1", UserID: "alice-id", UserAccount: "alice",
					Content: tc.content, CreatedAt: msgTime,
				},
			}
			data, _ := json.Marshal(evt)

			store.EXPECT().GetRoom(gomock.Any(), "dm-1").Return(testDMRoom, nil)
			store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "dm-1", "msg-1", msgTime, false).Return(nil)
			store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(testDMSubs, nil)

			if tc.wantSetMentions {
				store.EXPECT().SetSubscriptionMentions(gomock.Any(), "dm-1", gomock.InAnyOrder(tc.mentionedUsers)).Return(nil)
			}

			// Employee lookup expectations per test case
			switch tc.name {
			case "no mentions":
				expectEmployeeLookup(store, []string{"alice"}, testEmployees[:1])
			case "with mention":
				expectEmployeeLookup(store, []string{"alice", "bob"}, testEmployees)
			}

			keyStore := NewMockRoomKeyProvider(ctrl)
			h := NewHandler(store, pub, keyStore)
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

func TestHandler_HandleMessage_Errors(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)

	t.Run("invalid json", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)

		err := h.HandleMessage(context.Background(), []byte("not json"))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("room not found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(nil, errors.New("not found"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("update room fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(errors.New("db error"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("set subscription mentions fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", gomock.Any()).Return(errors.New("db error"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hey @alice", msgTime))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "set subscription mentions")
		assert.Empty(t, pub.records)
	})

	t.Run("unknown room type", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		unknownRoom := &model.Room{
			ID: "room-1", Name: "general", Type: "unknown",
			SiteID: "site-a", UserCount: 5,
		}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(unknownRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(nil, nil)

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.NoError(t, err)
		assert.Empty(t, pub.records)
	})

	t.Run("list subscriptions fails for DM", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		store.EXPECT().GetRoom(gomock.Any(), "dm-1").Return(testDMRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "dm-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(nil, nil)
		store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(nil, errors.New("db error"))

		keyStore := NewMockRoomKeyProvider(ctrl)
		h := NewHandler(store, pub, keyStore)
		evt := model.MessageEvent{
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

	t.Run("sender mentioned deduplicates lookup", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().SetSubscriptionMentions(gomock.Any(), "room-1", []string{"sender"}).Return(nil)
		expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})

		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hey @sender", msgTime))
		require.NoError(t, err)

		require.Len(t, pub.records, 1)
		evt, _ := decryptClientMessage(t, pub.records[0].data, key)
		require.Len(t, evt.Mentions, 1)
		assert.Equal(t, "sender", evt.Mentions[0].Account)
		assert.Equal(t, "寄件者", evt.Mentions[0].ChineseName)
	})

	t.Run("employee lookup fails fallback to account", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(nil, errors.New("db error"))

		h := NewHandler(store, pub, keyStore)
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

	store.EXPECT().GetRoom(gomock.Any(), "dm-1").Return(testDMRoom, nil)
	store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "dm-1", "msg-1", msgTime, false).Return(nil)
	store.EXPECT().ListSubscriptions(gomock.Any(), "dm-1").Return(testDMSubs, nil)
	store.EXPECT().FindEmployeesByAccountNames(gomock.Any(), gomock.Any()).Return(testEmployees, nil)

	keyStore := NewMockRoomKeyProvider(ctrl)
	h := NewHandler(store, pub, keyStore)
	evt := model.MessageEvent{
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

func TestHandler_HandleMessage_GroupRoom_Encryption(t *testing.T) {
	msgTime := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)

	t.Run("keystore returns nil key", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(nil, nil)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})

		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no current key")
		assert.Empty(t, pub.records)
	})

	t.Run("keystore returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(nil, errors.New("valkey down"))

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})

		h := NewHandler(store, pub, keyStore)
		err := h.HandleMessage(context.Background(), makeMessageEvent("room-1", "hello", msgTime))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get room key")
		assert.Contains(t, err.Error(), "valkey down")
		assert.Empty(t, pub.records)
	})

	t.Run("published event has encrypted message and plaintext metadata", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		pub := &mockPublisher{}

		key := testRoomKey(t)
		keyStore := NewMockRoomKeyProvider(ctrl)
		keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(testGroupRoom, nil)
		store.EXPECT().UpdateRoomOnNewMessage(gomock.Any(), "room-1", "msg-1", msgTime, false).Return(nil)
		expectEmployeeLookup(store, []string{"sender"}, []model.Employee{{AccountName: "sender", Name: "寄件者", EngName: "Sender Lin"}})

		h := NewHandler(store, pub, keyStore)
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
		assert.NotEmpty(t, env.EphemeralPublicKey)
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

func TestBuildMentionParticipants(t *testing.T) {
	employees := map[string]model.Employee{
		"alice": {AccountName: "alice", Name: "愛麗絲", EngName: "Alice Wang"},
	}

	t.Run("empty accounts returns nil", func(t *testing.T) {
		result := buildMentionParticipants(nil, employees)
		assert.Nil(t, result)
	})

	t.Run("employee found uses employee data", func(t *testing.T) {
		result := buildMentionParticipants([]string{"alice"}, employees)
		require.Len(t, result, 1)
		assert.Equal(t, "alice", result[0].Account)
		assert.Equal(t, "愛麗絲", result[0].ChineseName)
		assert.Equal(t, "Alice Wang", result[0].EngName)
		assert.Empty(t, result[0].UserID)
	})

	t.Run("employee not found falls back to account", func(t *testing.T) {
		result := buildMentionParticipants([]string{"unknown"}, employees)
		require.Len(t, result, 1)
		assert.Equal(t, "unknown", result[0].Account)
		assert.Equal(t, "unknown", result[0].ChineseName)
		assert.Equal(t, "unknown", result[0].EngName)
	})

	t.Run("mixed found and not found", func(t *testing.T) {
		result := buildMentionParticipants([]string{"alice", "unknown"}, employees)
		require.Len(t, result, 2)
		assert.Equal(t, "愛麗絲", result[0].ChineseName)
		assert.Equal(t, "unknown", result[1].ChineseName)
	})
}

func TestBuildClientMessage(t *testing.T) {
	msg := &model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
		Content: "hello", CreatedAt: time.Now(),
	}

	t.Run("employee found", func(t *testing.T) {
		employees := map[string]model.Employee{
			"alice": {AccountName: "alice", Name: "愛麗絲", EngName: "Alice Wang"},
		}
		cm := buildClientMessage(msg, employees)
		assert.Equal(t, "m1", cm.ID)
		require.NotNil(t, cm.Sender)
		assert.Equal(t, "u1", cm.Sender.UserID)
		assert.Equal(t, "alice", cm.Sender.Account)
		assert.Equal(t, "愛麗絲", cm.Sender.ChineseName)
		assert.Equal(t, "Alice Wang", cm.Sender.EngName)
	})

	t.Run("employee not found", func(t *testing.T) {
		cm := buildClientMessage(msg, map[string]model.Employee{})
		require.NotNil(t, cm.Sender)
		assert.Equal(t, "alice", cm.Sender.ChineseName)
		assert.Equal(t, "alice", cm.Sender.EngName)
	})
}

func TestDetectMentionAll(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"@All uppercase", "attention @All everyone", true},
		{"@all lowercase", "hey @all", true},
		{"@HERE uppercase", "look @HERE please", true},
		{"@here lowercase", "look @here please", true},
		{"no mentions", "just a normal message", false},
		{"partial match not detected", "email@all.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, detectMentionAll(tc.content))
		})
	}
}

func TestExtractMentionedAccounts(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"two mentions", "hey @Alice and @Bob", []string{"alice", "bob"}},
		{"no mentions", "no mentions here", nil},
		{"dedup case insensitive", "@alice @Alice", []string{"alice"}},
		{"@all excluded", "hey @all and @alice", []string{"alice"}},
		{"@here excluded", "@here @bob", []string{"bob"}},
		{"mixed case", "hey @BOB", []string{"bob"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractMentionedAccounts(tc.content)
			if tc.want == nil {
				assert.Empty(t, got)
			} else {
				assert.ElementsMatch(t, tc.want, got)
			}
		})
	}
}
