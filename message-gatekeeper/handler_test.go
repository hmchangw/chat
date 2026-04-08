package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func makePublishFunc(published *[]publishedMsg, returnErr error) publishFunc {
	return func(_ context.Context, subj string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
		if published != nil {
			*published = append(*published, publishedMsg{subject: subj, data: data})
		}
		if returnErr != nil {
			return nil, returnErr
		}
		return &jetstream.PubAck{}, nil
	}
}

type publishedMsg struct {
	subject string
	data    []byte
}

func TestHandler_ProcessMessage(t *testing.T) {
	validID := uuid.New().String()
	validContent := "hello world"
	validSiteID := "site-a"
	validRoomID := "room-1"
	validAccount := "alice"

	sub := &model.Subscription{
		User:   model.SubscriptionUser{ID: "u1", Account: validAccount},
		RoomID: validRoomID,
		Role:   model.RoleMember,
	}

	tests := []struct {
		name        string
		account     string
		roomID      string
		siteID      string
		buildData   func() []byte
		setupStore  func(s *MockStore)
		setupPub    func() (publishFunc, *[]publishedMsg)
		wantErr     bool
		wantInfra   bool
		checkResult func(t *testing.T, data []byte, published []publishedMsg)
	}{
		{
			name:    "happy path",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validAccount, validRoomID).
					Return(sub, nil)
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				var published []publishedMsg
				return makePublishFunc(&published, nil), &published
			},
			wantErr: false,
			checkResult: func(t *testing.T, data []byte, published []publishedMsg) {
				require.NotNil(t, data)
				var msg model.Message
				err := json.Unmarshal(data, &msg)
				require.NoError(t, err)
				assert.Equal(t, validContent, msg.Content)
				assert.Equal(t, "u1", msg.UserID)
				assert.Equal(t, validRoomID, msg.RoomID)
				assert.Equal(t, validAccount, msg.UserAccount)
				assert.NotEmpty(t, msg.ID)
				assert.Len(t, published, 1)
				assert.Equal(t, subject.MsgCanonicalCreated(validSiteID), published[0].subject)
				// Verify MessageEvent has Timestamp set
				var evt model.MessageEvent
				err = json.Unmarshal(published[0].data, &evt)
				require.NoError(t, err)
				assert.Greater(t, evt.Timestamp, int64(0))
			},
		},
		{
			name:    "happy path with thread parent",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				return []byte(fmt.Sprintf(
					`{"id":%q,"content":%q,"requestId":"req-1","threadParentMessageId":"parent-msg-uuid","threadParentMessageCreatedAt":"2026-01-01T10:00:00Z"}`,
					validID, validContent,
				))
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validAccount, validRoomID).
					Return(sub, nil)
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				var published []publishedMsg
				return makePublishFunc(&published, nil), &published
			},
			wantErr: false,
			checkResult: func(t *testing.T, data []byte, published []publishedMsg) {
				parentTS := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
				require.NotNil(t, data)
				var msg model.Message
				require.NoError(t, json.Unmarshal(data, &msg))
				assert.Equal(t, "parent-msg-uuid", msg.ThreadParentMessageID)
				require.NotNil(t, msg.ThreadParentMessageCreatedAt)
				assert.Equal(t, parentTS, msg.ThreadParentMessageCreatedAt.UTC())

				require.Len(t, published, 1)
				var evt model.MessageEvent
				require.NoError(t, json.Unmarshal(published[0].data, &evt))
				assert.Equal(t, "parent-msg-uuid", evt.Message.ThreadParentMessageID)
				require.NotNil(t, evt.Message.ThreadParentMessageCreatedAt)
				assert.Equal(t, parentTS, evt.Message.ThreadParentMessageCreatedAt.UTC())
				assert.Greater(t, evt.Timestamp, int64(0))
			},
		},
		{
			name:    "thread parent ID without timestamp",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{
					ID:                    validID,
					Content:               validContent,
					ThreadParentMessageID: "parent-msg-uuid",
				}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
		{
			name:    "invalid UUID",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: "not-a-uuid", Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
		{
			name:    "empty content",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: ""}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
		{
			name:    "content exceeds 20KB",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: strings.Repeat("x", 20*1024+1)}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
		{
			name:    "user not in room",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validAccount, validRoomID).
					Return(nil, fmt.Errorf("user alice not subscribed to room room-1: %w", errNotSubscribed))
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
		{
			name:    "store infra error",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validAccount, validRoomID).
					Return(nil, fmt.Errorf("connection refused"))
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: true,
		},
		{
			name:    "publish fails",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validAccount, validRoomID).
					Return(sub, nil)
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, fmt.Errorf("nats publish error")), nil
			},
			wantErr:   true,
			wantInfra: true,
		},
		{
			name:    "malformed JSON",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  validSiteID,
			buildData: func() []byte {
				return []byte("{not valid json}")
			},
			setupStore: func(s *MockStore) {},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
		{
			name:    "siteID mismatch",
			account: validAccount,
			roomID:  validRoomID,
			siteID:  "site-b",
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			tc.setupStore(store)

			pub, publishedPtr := tc.setupPub()

			h := &Handler{
				store:   store,
				publish: pub,
				siteID:  validSiteID,
			}

			data, err := h.processMessage(context.Background(), tc.account, tc.roomID, tc.siteID, tc.buildData())

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantInfra {
					var ie *infraError
					assert.True(t, errors.As(err, &ie), "expected infraError, got %T: %v", err, err)
				} else {
					var ie *infraError
					assert.False(t, errors.As(err, &ie), "expected non-infra error, got infraError: %v", err)
				}
			} else {
				require.NoError(t, err)
				if tc.checkResult != nil {
					var published []publishedMsg
					if publishedPtr != nil {
						published = *publishedPtr
					}
					tc.checkResult(t, data, published)
				}
			}
		})
	}
}
