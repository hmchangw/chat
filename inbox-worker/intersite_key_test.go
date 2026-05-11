package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

func startInboxNATSServer(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Port: -1}
	ns, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second), "nats server did not become ready")
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func TestNatsInterSiteKeyClient_GetRoomKey_Success(t *testing.T) {
	nc := startInboxNATSServer(t)

	_, err := nc.Subscribe(subject.ServerRoomKeyGet("site-a"), func(m *nats.Msg) {
		evt := model.RoomKeyEvent{RoomID: "r1", Version: 2, PublicKey: []byte("pk"), PrivateKey: []byte("sk")}
		data, _ := json.Marshal(evt)
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush()) // ensure subscription is registered before the request races it

	c := newNatsInterSiteKeyClient(nc, 2*time.Second)
	got, err := c.GetRoomKey(context.Background(), "site-a", "r1")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	assert.Equal(t, []byte("pk"), got.PublicKey)
}

func TestNatsInterSiteKeyClient_GetRoomKey_OriginError(t *testing.T) {
	nc := startInboxNATSServer(t)

	_, err := nc.Subscribe(subject.ServerRoomKeyGet("site-a"), func(m *nats.Msg) {
		errResp := model.ErrorResponse{Error: "some other origin failure"}
		data, _ := json.Marshal(errResp)
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	c := newNatsInterSiteKeyClient(nc, 2*time.Second)
	_, err = c.GetRoomKey(context.Background(), "site-a", "r1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "some other origin failure")
	assert.False(t, errors.Is(err, errRoomKeyAbsent), "generic origin errors must not match the absent sentinel")
}

func TestNatsInterSiteKeyClient_GetRoomKey_RoomKeyAbsentSentinel(t *testing.T) {
	nc := startInboxNATSServer(t)

	_, err := nc.Subscribe(subject.ServerRoomKeyGet("site-a"), func(m *nats.Msg) {
		errResp := model.ErrorResponse{Error: originErrRoomKeyNotFound}
		data, _ := json.Marshal(errResp)
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	c := newNatsInterSiteKeyClient(nc, 2*time.Second)
	_, err = c.GetRoomKey(context.Background(), "site-a", "r1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errRoomKeyAbsent),
		"origin's room-key-not-found reply must be detectable via errors.Is(err, errRoomKeyAbsent)")
}

func TestNatsInterSiteKeyClient_PropagatesRequestID(t *testing.T) {
	nc := startInboxNATSServer(t)

	received := make(chan string, 1)
	_, err := nc.Subscribe(subject.ServerRoomKeyGet("site-a"), func(m *nats.Msg) {
		received <- m.Header.Get("X-Request-ID")
		evt := model.RoomKeyEvent{RoomID: "r1", Version: 1, PublicKey: []byte("pk"), PrivateKey: []byte("sk")}
		data, _ := json.Marshal(evt)
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	const wantID = "01970a4f-8c2d-7c9a-abcd-e0123456789f"
	ctx := natsutil.WithRequestID(context.Background(), wantID)

	c := newNatsInterSiteKeyClient(nc, 2*time.Second)
	_, err = c.GetRoomKey(ctx, "site-a", "r1")
	require.NoError(t, err)

	select {
	case gotID := <-received:
		assert.Equal(t, wantID, gotID, "X-Request-ID header must be forwarded to origin")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request")
	}
}
