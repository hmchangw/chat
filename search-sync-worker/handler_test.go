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
	"github.com/hmchangw/chat/pkg/searchengine"
)

// stubMsg implements jetstream.Msg for testing.
type stubMsg struct {
	data   []byte
	acked  bool
	nacked bool
}

func (m *stubMsg) Data() []byte                              { return m.data }
func (m *stubMsg) Ack() error                                { m.acked = true; return nil }
func (m *stubMsg) Nak() error                                { m.nacked = true; return nil }
func (m *stubMsg) NakWithDelay(time.Duration) error          { return nil }
func (m *stubMsg) InProgress() error                         { return nil }
func (m *stubMsg) Term() error                               { return nil }
func (m *stubMsg) TermWithReason(string) error               { return nil }
func (m *stubMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (m *stubMsg) Subject() string                           { return "" }
func (m *stubMsg) Reply() string                             { return "" }
func (m *stubMsg) Headers() nats.Header                      { return nil }
func (m *stubMsg) DoubleAck(context.Context) error           { return nil }

func makeStubMsg(t *testing.T, evt *model.MessageEvent) *stubMsg {
	t.Helper()
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	return &stubMsg{data: data}
}

func TestHandler_Add(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	h := NewHandler(store, newMessageCollection("msgs-v1"), 500)

	evt := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content: "hello", CreatedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		},
		SiteID: "site-a", Timestamp: 100,
	}
	msg := makeStubMsg(t, &evt)

	h.Add(msg)
	assert.Equal(t, 1, h.BufferLen())
}

func TestHandler_Add_MalformedJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	h := NewHandler(store, newMessageCollection("msgs-v1"), 500)

	msg := &stubMsg{data: []byte("{invalid")}
	h.Add(msg)
	assert.Equal(t, 0, h.BufferLen())
	assert.True(t, msg.acked)
}

func TestHandler_Flush(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	baseEvt := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content: "hello", CreatedAt: ts,
		},
		SiteID: "site-a", Timestamp: 100,
	}

	t.Run("all items succeed — all acked", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(1)).
			Return([]searchengine.BulkResult{{Status: 201}}, nil)

		h := NewHandler(store, newMessageCollection("msgs-v1"), 500)
		msg := makeStubMsg(t, &baseEvt)
		h.Add(msg)
		h.Flush(context.Background())

		assert.True(t, msg.acked)
		assert.False(t, msg.nacked)
		assert.Equal(t, 0, h.BufferLen())
	})

	t.Run("version conflict (409) — acked not nacked", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(1)).
			Return([]searchengine.BulkResult{{Status: 409, Error: "version conflict"}}, nil)

		h := NewHandler(store, newMessageCollection("msgs-v1"), 500)
		msg := makeStubMsg(t, &baseEvt)
		h.Add(msg)
		h.Flush(context.Background())

		assert.True(t, msg.acked)
		assert.False(t, msg.nacked)
	})

	t.Run("item failure — nacked for redelivery", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(1)).
			Return([]searchengine.BulkResult{{Status: 500, Error: "internal"}}, nil)

		h := NewHandler(store, newMessageCollection("msgs-v1"), 500)
		msg := makeStubMsg(t, &baseEvt)
		h.Add(msg)
		h.Flush(context.Background())

		assert.False(t, msg.acked)
		assert.True(t, msg.nacked)
	})

	t.Run("total bulk failure — all nacked", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(2)).
			Return(nil, fmt.Errorf("connection refused"))

		h := NewHandler(store, newMessageCollection("msgs-v1"), 500)
		msg1 := makeStubMsg(t, &baseEvt)
		evt2 := baseEvt
		evt2.Message.ID = "m2"
		msg2 := makeStubMsg(t, &evt2)

		h.Add(msg1)
		h.Add(msg2)
		h.Flush(context.Background())

		assert.True(t, msg1.nacked)
		assert.True(t, msg2.nacked)
		assert.Equal(t, 0, h.BufferLen())
	})

	t.Run("empty flush is no-op", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		h := NewHandler(store, newMessageCollection("msgs-v1"), 500)
		h.Flush(context.Background())
		assert.Equal(t, 0, h.BufferLen())
	})

	t.Run("mixed results — per-item ack/nak", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(3)).
			Return([]searchengine.BulkResult{
				{Status: 201},
				{Status: 409, Error: "version conflict"},
				{Status: 500, Error: "shard failure"},
			}, nil)

		h := NewHandler(store, newMessageCollection("msgs-v1"), 500)
		msgs := make([]*stubMsg, 3)
		for i := range msgs {
			evt := baseEvt
			evt.Message.ID = fmt.Sprintf("m%d", i)
			msgs[i] = makeStubMsg(t, &evt)
			h.Add(msgs[i])
		}
		h.Flush(context.Background())

		assert.True(t, msgs[0].acked, "201 should be acked")
		assert.True(t, msgs[1].acked, "409 should be acked")
		assert.True(t, msgs[2].nacked, "500 should be nacked")
	})
}
