package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

func startEmbeddedJetStream(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	opts := &natsserver.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	ns, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second), "nats server did not become ready")
	t.Cleanup(ns.Shutdown)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return nc, js
}

func TestCanonicalMemberPublisher_PublishesToRoomCanonical(t *testing.T) {
	nc, js := startEmbeddedJetStream(t)

	siteID := "site-A"
	_, err := js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     stream.Rooms(siteID).Name,
		Subjects: stream.Rooms(siteID).Subjects,
	})
	require.NoError(t, err)

	captured := make(chan *nats.Msg, 1)
	sub, err := nc.Subscribe(subject.RoomCanonicalWildcard(siteID), func(m *nats.Msg) {
		captured <- m
	})
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	p := newCanonicalMemberPublisher(js, siteID)

	req := &model.AddMembersRequest{
		RoomID:           "room-1",
		Users:            []string{"u1", "u2"},
		RequesterAccount: "owner-1",
		Timestamp:        time.Now().UTC().UnixMilli(),
	}
	require.NoError(t, p.Publish(context.Background(), "owner-1", "room-1", req, "corr-1"))

	select {
	case m := <-captured:
		assert.Equal(t, subject.RoomCanonical(siteID, "member.add"), m.Subject)
		var got model.AddMembersRequest
		require.NoError(t, json.Unmarshal(m.Data, &got))
		assert.Equal(t, req.RoomID, got.RoomID)
		assert.Equal(t, req.Users, got.Users)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive canonical publish within 2s")
	}
}
