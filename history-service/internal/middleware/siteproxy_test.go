package middleware_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/history-service/internal/middleware"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

type testReq struct {
	Name string `json:"name"`
}

type testResp struct {
	Greeting string `json:"greeting"`
}

// fakeRoomFinder is a test double for middleware.RoomFinder.
type fakeRoomFinder struct {
	rooms map[string]*model.Room
	err   error
}

func (f *fakeRoomFinder) GetRoom(_ context.Context, roomID string) (*model.Room, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rooms[roomID], nil
}

func startTestNATS(t *testing.T) *otelnats.Conn {
	t.Helper()
	opts := &natsserver.Options{Port: -1}
	ns, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second), "nats server did not become ready")
	t.Cleanup(ns.Shutdown)

	nc, err := otelnats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func TestSiteProxy_LocalRoom(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-1"},
	}}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "local " + c.Param("roomID")}, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "local r1", result.Greeting)
}

func TestSiteProxy_RemoteRoom_ForwardsCorrectly(t *testing.T) {
	nc := startTestNATS(t)
	forwardNC, err := nats.Connect(nc.NatsConn().ConnectedUrl())
	require.NoError(t, err)
	t.Cleanup(forwardNC.Close)

	// Remote site-2 subscriber handles forwarded requests.
	forwardNC.Subscribe("chat.user.*.request.room.*.site-2.msg.history",
		func(msg *nats.Msg) {
			if msg.Header != nil && msg.Header.Get("X-Forwarded-Site") != "" {
				msg.Respond([]byte(`{"greeting":"from site-2"}`))
			}
		})
	forwardNC.Flush()

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-2"},
	}}

	r := natsrouter.New(nc, "test-service")
	g := r.Group(middleware.SiteProxy("site-1", forwardNC, rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "local"}, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "from site-2", result.Greeting)
}

func TestSiteProxy_ForwardedHeader_SkipsLookup(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	// Room belongs to site-2, but the forwarded header should skip the lookup entirely.
	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-2"},
	}}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "forwarded-to-me"}, nil
		})

	msg := nats.NewMsg("chat.user.alice.request.room.r1.site-1.msg.history")
	msg.Data, _ = json.Marshal(testReq{Name: "test"})
	msg.Header = nats.Header{}
	msg.Header.Set("X-Forwarded-Site", "site-2")

	resp, err := nc.NatsConn().RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "forwarded-to-me", result.Greeting)
}

func TestSiteProxy_RoomNotFound(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{}}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called when room not found")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "room not found", errResp.Error)
}

func TestSiteProxy_RoomLookupError(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	rooms := &fakeRoomFinder{err: fmt.Errorf("db connection refused")}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called on room lookup error")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
}

func TestSiteProxy_RemoteRoom_ForwardError(t *testing.T) {
	nc := startTestNATS(t)

	forwardNC, err := nats.Connect(nc.NatsConn().ConnectedUrl())
	require.NoError(t, err)
	forwardNC.Close() // Close immediately to simulate failure.

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-2"},
	}}

	r := natsrouter.New(nc, "test-service")
	g := r.Group(middleware.SiteProxy("site-1", forwardNC, rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called for failed forward")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "remote site unavailable", errResp.Error)
}
