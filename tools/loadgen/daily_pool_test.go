//go:build integration

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

func TestDirectPool_ReceivesBroadcast(t *testing.T) {
	url := testutil.NATS(t)
	ncPub, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(func() { ncPub.Close() })

	col := NewCollector(NewMetrics(), "test")
	pool := newDirectPool(url, col)
	t.Cleanup(pool.Close)

	u := &userState{ID: "u-1", Account: "user-1", Rooms: []string{"room-test"}}
	require.NoError(t, pool.Add(u))

	// Publish a fake broadcast event with LastMsgID set.
	evt := model.RoomEvent{Type: model.RoomEventNewMessage, LastMsgID: "msg-42", RoomID: "room-test"}
	data, err := json.Marshal(evt)
	require.NoError(t, err)

	col.RecordPublishBroadcastOnly("msg-42", time.Now())
	require.NoError(t, ncPub.Publish(subject.RoomEvent("room-test"), data))
	require.NoError(t, ncPub.Flush())

	require.Eventually(t, func() bool {
		return col.E2Count() == 1
	}, 2*time.Second, 20*time.Millisecond)
}
