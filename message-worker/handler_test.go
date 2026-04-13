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

	expectedSender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     msg.UserAccount,
	}

	tests := []struct {
		name       string
		data       []byte
		setupMocks func(store *MockStore, userStore *MockUserStore)
		wantErr    bool
	}{
		{
			name: "happy path — user found and message saved",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), &msg, &expectedSender, "site-a").Return(nil)
			},
		},
		{
			name: "user not found — NAK without saving",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").
					Return(nil, errors.New("user not found"))
			},
			wantErr: true,
		},
		{
			name: "user store DB error — NAK without saving",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").
					Return(nil, errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
		{
			name: "save error — NAK after user lookup",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), &msg, &expectedSender, "site-a").
					Return(errors.New("cassandra: write timeout"))
			},
			wantErr: true,
		},
		{
			name:       "malformed JSON — NAK immediately",
			data:       []byte("{invalid"),
			setupMocks: func(store *MockStore, us *MockUserStore) {},
			wantErr:    true,
		},
		{
			name: "thread message — calls SaveThreadMessage not SaveMessage",
			data: threadData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveThreadMessage(gomock.Any(), &threadMsg, &expectedSender, "site-a").Return(nil)
			},
		},
		{
			name: "thread message save error — NAK after user lookup",
			data: threadData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveThreadMessage(gomock.Any(), &threadMsg, &expectedSender, "site-a").
					Return(errors.New("cassandra: write timeout"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStore := NewMockStore(ctrl)
			mockUserStore := NewMockUserStore(ctrl)
			tt.setupMocks(mockStore, mockUserStore)

			h := NewHandler(mockStore, mockUserStore)
			err := h.processMessage(context.Background(), tt.data)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseMentions(tt.content))
		})
	}
}
