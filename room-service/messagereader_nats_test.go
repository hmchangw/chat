package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/subject"
)

func TestNATSMessageReader_GetMessageRoomAndCreatedAt(t *testing.T) {
	const (
		account   = "alice"
		roomID    = "room-eng"
		siteID    = "site-us"
		messageID = "msg-123"
	)
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// respondOn subscribes a one-shot responder on the GetMessageByID subject and
	// returns the raw request body the handler saw, so a test can assert wire shape.
	respondOn := func(t *testing.T, nc *nats.Conn, reply []byte) *[]byte {
		t.Helper()
		var gotBody []byte
		captured := &gotBody
		sub, err := nc.Subscribe(subject.MsgGet(account, roomID, siteID), func(m *nats.Msg) {
			*captured = m.Data
			_ = m.Respond(reply)
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = sub.Unsubscribe() })
		return captured
	}

	t.Run("happy path — returns room, createdAt, sender from the reply", func(t *testing.T) {
		nc := startInProcessNATS(t)
		msg := cassandra.Message{
			RoomID:    roomID,
			CreatedAt: createdAt,
			Sender:    cassandra.Participant{Account: account},
		}
		reply, _ := json.Marshal(msg)
		gotBody := respondOn(t, nc, reply)

		r := newNATSMessageReader(nc, siteID, 2*time.Second)
		gotRoom, gotCreated, gotSender, found, err := r.GetMessageRoomAndCreatedAt(context.Background(), account, roomID, messageID)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, roomID, gotRoom)
		assert.Equal(t, createdAt, gotCreated.UTC())
		assert.Equal(t, account, gotSender)

		// Request body carries the messageId history-service expects.
		var req struct {
			MessageID string `json:"messageId"`
		}
		require.NoError(t, json.Unmarshal(*gotBody, &req))
		assert.Equal(t, messageID, req.MessageID)
	})

	t.Run("not found — history NotFound maps to found=false, no error", func(t *testing.T) {
		nc := startInProcessNATS(t)
		reply, _ := json.Marshal(errcode.NotFound("message not found"))
		respondOn(t, nc, reply)

		r := newNATSMessageReader(nc, siteID, 2*time.Second)
		_, _, _, found, err := r.GetMessageRoomAndCreatedAt(context.Background(), account, roomID, messageID)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("forbidden — access-window rejection also maps to found=false", func(t *testing.T) {
		nc := startInProcessNATS(t)
		reply, _ := json.Marshal(errcode.Forbidden("message is outside access window"))
		respondOn(t, nc, reply)

		r := newNATSMessageReader(nc, siteID, 2*time.Second)
		_, _, _, found, err := r.GetMessageRoomAndCreatedAt(context.Background(), account, roomID, messageID)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("infra error — unavailable propagates as an error", func(t *testing.T) {
		nc := startInProcessNATS(t)
		reply, _ := json.Marshal(errcode.Unavailable("history down"))
		respondOn(t, nc, reply)

		r := newNATSMessageReader(nc, siteID, 2*time.Second)
		_, _, _, found, err := r.GetMessageRoomAndCreatedAt(context.Background(), account, roomID, messageID)
		require.Error(t, err)
		assert.False(t, found)
	})

	t.Run("no responder — request error propagates", func(t *testing.T) {
		nc := startInProcessNATS(t)
		// no subscriber on the subject

		r := newNATSMessageReader(nc, siteID, 200*time.Millisecond)
		_, _, _, found, err := r.GetMessageRoomAndCreatedAt(context.Background(), account, roomID, messageID)
		require.Error(t, err)
		assert.False(t, found)
	})
}
