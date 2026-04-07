package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func makePublishFunc(published *[]publishedMsg, returnErr error) publishFunc {
	return func(subj string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
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
	validUsername := "alice"

	sub := &model.Subscription{
		User:   model.SubscriptionUser{ID: "u1", Username: validUsername},
		RoomID: validRoomID,
		Role:   model.RoleMember,
	}

	tests := []struct {
		name        string
		username    string
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
			name:     "happy path",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validUsername, validRoomID).
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
				assert.Equal(t, validUsername, msg.Username)
				assert.NotEmpty(t, msg.ID)
				assert.Len(t, published, 1)
				assert.Equal(t, subject.MsgCanonicalCreated(validSiteID), published[0].subject)
			},
		},
		{
			name:     "happy path with thread parent",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{
					ID:                    validID,
					Content:               validContent,
					ThreadParentMessageID: "parent-msg-uuid",
				}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validUsername, validRoomID).
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
				require.NoError(t, json.Unmarshal(data, &msg))
				assert.Equal(t, "parent-msg-uuid", msg.ThreadParentMessageID)

				require.Len(t, published, 1)
				var evt model.MessageEvent
				require.NoError(t, json.Unmarshal(published[0].data, &evt))
				assert.Equal(t, "parent-msg-uuid", evt.Message.ThreadParentMessageID)
			},
		},
		{
			name:     "invalid UUID",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
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
			name:     "empty content",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
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
			name:     "content exceeds 20KB",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
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
			name:     "user not in room",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validUsername, validRoomID).
					Return(nil, fmt.Errorf("user alice not subscribed to room room-1: %w", errNotSubscribed))
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: false,
		},
		{
			name:     "store infra error",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validUsername, validRoomID).
					Return(nil, fmt.Errorf("connection refused"))
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, nil), nil
			},
			wantErr:   true,
			wantInfra: true,
		},
		{
			name:     "publish fails",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().
					GetSubscription(gomock.Any(), validUsername, validRoomID).
					Return(sub, nil)
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				return makePublishFunc(nil, fmt.Errorf("nats publish error")), nil
			},
			wantErr:   true,
			wantInfra: true,
		},
		{
			name:     "malformed JSON",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   validSiteID,
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
			name:     "siteID mismatch",
			username: validUsername,
			roomID:   validRoomID,
			siteID:   "site-b",
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

			data, err := h.processMessage(context.Background(), tc.username, tc.roomID, tc.siteID, tc.buildData())

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
