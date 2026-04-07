package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessMessage(t *testing.T) {
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	edited := now.Add(5 * time.Minute)
	threadParent := now.Add(-1 * time.Hour)

	fullMsg := model.Message{
		ID:                    "msg-1",
		RoomID:                "r1",
		CreatedAt:             now,
		Sender:                model.Participant{ID: "u1", UserName: "alice"},
		TargetUser:            &model.Participant{ID: "u2", UserName: "bob"},
		Content:               "hello world",
		Mentions:              []model.Participant{{ID: "u3", UserName: "charlie"}},
		File:                  &model.File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"},
		Card:                  &model.Card{Template: "approval", Data: []byte(`{"k":"v"}`)},
		CardAction:            &model.CardAction{Verb: "approve", CardID: "c1"},
		TShow:                 true,
		ThreadParentCreatedAt: &threadParent,
		VisibleTo:             "u1",
		Reactions:             map[string][]model.Participant{"thumbsup": {{ID: "u2", UserName: "bob"}}},
		SysMsgType:            "user_joined",
		SysMsgData:            []byte(`{"userId":"u3"}`),
		FederateFrom:          "site-remote",
		EditedAt:              &edited,
	}

	minimalMsg := model.Message{
		ID:        "msg-2",
		RoomID:    "r1",
		CreatedAt: now,
		Sender:    model.Participant{ID: "u1", UserName: "alice"},
		Content:   "hi",
	}

	tests := []struct {
		name      string
		data      []byte
		storeMsg  *model.Message
		storeErr  error
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "success with all fields",
			data:     mustMarshal(t, model.MessageEvent{Message: fullMsg, SiteID: "site-a"}),
			storeMsg: &fullMsg,
		},
		{
			name:     "success with minimal fields",
			data:     mustMarshal(t, model.MessageEvent{Message: minimalMsg, SiteID: "site-a"}),
			storeMsg: &minimalMsg,
		},
		{
			name:      "store error",
			data:      mustMarshal(t, model.MessageEvent{Message: minimalMsg, SiteID: "site-a"}),
			storeErr:  fmt.Errorf("cassandra unavailable"),
			wantErr:   true,
			errSubstr: "save message",
		},
		{
			name:      "malformed JSON",
			data:      []byte("{invalid"),
			wantErr:   true,
			errSubstr: "unmarshal message event",
		},
		{
			name:      "empty payload",
			data:      []byte{},
			wantErr:   true,
			errSubstr: "unmarshal message event",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)

			if tc.storeMsg != nil {
				store.EXPECT().
					SaveMessage(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, got model.Message) error {
						assert.Equal(t, tc.storeMsg.ID, got.ID)
						assert.Equal(t, tc.storeMsg.RoomID, got.RoomID)
						assert.Equal(t, tc.storeMsg.Sender.ID, got.Sender.ID)
						assert.Equal(t, tc.storeMsg.Content, got.Content)
						return tc.storeErr
					})
			} else if tc.storeErr != nil {
				store.EXPECT().
					SaveMessage(gomock.Any(), gomock.Any()).
					Return(tc.storeErr)
			}

			h := NewHandler(store)
			err := h.processMessage(context.Background(), tc.data)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHandler_HandleJetStreamMsg(t *testing.T) {
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	msg := model.Message{
		ID:        "msg-1",
		RoomID:    "r1",
		CreatedAt: now,
		Sender:    model.Participant{ID: "u1", UserName: "alice"},
		Content:   "hello",
	}

	t.Run("ack on success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().SaveMessage(gomock.Any(), gomock.Any()).Return(nil)

		h := NewHandler(store)
		jm := &fakeJetStreamMsg{data: mustMarshal(t, model.MessageEvent{Message: msg, SiteID: "site-a"})}
		h.HandleJetStreamMsg(jm)

		assert.True(t, jm.acked)
		assert.False(t, jm.naked)
	})

	t.Run("nak on store error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().SaveMessage(gomock.Any(), gomock.Any()).Return(fmt.Errorf("db down"))

		h := NewHandler(store)
		jm := &fakeJetStreamMsg{data: mustMarshal(t, model.MessageEvent{Message: msg, SiteID: "site-a"})}
		h.HandleJetStreamMsg(jm)

		assert.False(t, jm.acked)
		assert.True(t, jm.naked)
	})

	t.Run("nak on malformed JSON", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)

		h := NewHandler(store)
		jm := &fakeJetStreamMsg{data: []byte("{invalid")}
		h.HandleJetStreamMsg(jm)

		assert.False(t, jm.acked)
		assert.True(t, jm.naked)
	})
}

// fakeJetStreamMsg implements jetstream.Msg for unit testing.
type fakeJetStreamMsg struct {
	data  []byte
	acked bool
	naked bool
}

func (f *fakeJetStreamMsg) Data() []byte                              { return f.data }
func (f *fakeJetStreamMsg) Ack() error                                { f.acked = true; return nil }
func (f *fakeJetStreamMsg) Nak() error                                { f.naked = true; return nil }
func (f *fakeJetStreamMsg) NakWithDelay(d time.Duration) error        { return nil }
func (f *fakeJetStreamMsg) InProgress() error                         { return nil }
func (f *fakeJetStreamMsg) Term() error                               { return nil }
func (f *fakeJetStreamMsg) TermWithReason(reason string) error        { return nil }
func (f *fakeJetStreamMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (f *fakeJetStreamMsg) Headers() nats.Header                      { return nil }
func (f *fakeJetStreamMsg) Subject() string                           { return "" }
func (f *fakeJetStreamMsg) Reply() string                             { return "" }
func (f *fakeJetStreamMsg) DoubleAck(ctx context.Context) error       { return nil }

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}
