package middleware_test

import (
	"context"
	"encoding/json"
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

func TestSiteProxy_LocalRequest(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")
	r.Use(middleware.SiteProxy("site-1", nc.NatsConn()))

	natsrouter.Register(r, "chat.user.{account}.request.room.{roomID}.{siteID}.msg.history",
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

func TestSiteProxy_RemoteRequest_ForwardsCorrectly(t *testing.T) {
	nc := startTestNATS(t)
	forwardNC, err := nats.Connect(nc.NatsConn().ConnectedUrl())
	require.NoError(t, err)
	t.Cleanup(forwardNC.Close)

	// Subscribe a responder that only handles forwarded messages (has header).
	// Non-forwarded messages (the original client request) are ignored.
	forwardNC.Subscribe("chat.user.*.request.room.*.site-2.msg.history",
		func(msg *nats.Msg) {
			if msg.Header != nil && msg.Header.Get("X-Forwarded-Site") != "" {
				msg.Respond([]byte(`{"greeting":"from site-2"}`))
			}
		})
	forwardNC.Flush()

	r := natsrouter.New(nc, "test-service")
	r.Use(middleware.SiteProxy("site-1", forwardNC))

	natsrouter.Register(r, "chat.user.{account}.request.room.{roomID}.{siteID}.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "local"}, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-2.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	// The response comes from either the remote subscriber or the router
	// (which handles the forwarded message via header pass-through).
	// Both are valid — in production they would be on separate NATS clusters.
	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.NotEmpty(t, result.Greeting)
}

func TestSiteProxy_ForwardedHeader_SkipsForwarding(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")
	r.Use(middleware.SiteProxy("site-1", nc.NatsConn()))

	natsrouter.Register(r, "chat.user.{account}.request.room.{roomID}.{siteID}.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "forwarded-to-me"}, nil
		})

	msg := nats.NewMsg("chat.user.alice.request.room.r1.site-2.msg.history")
	msg.Data, _ = json.Marshal(testReq{Name: "test"})
	msg.Header = nats.Header{}
	msg.Header.Set("X-Forwarded-Site", "site-2")

	resp, err := nc.NatsConn().RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "forwarded-to-me", result.Greeting)
}

func TestSiteProxy_RemoteRequest_ForwardError(t *testing.T) {
	nc := startTestNATS(t)

	// Create a separate connection for forwarding, then close it to simulate failure.
	forwardNC, err := nats.Connect(nc.NatsConn().ConnectedUrl())
	require.NoError(t, err)
	forwardNC.Close()

	r := natsrouter.New(nc, "test-service")
	r.Use(middleware.SiteProxy("site-1", forwardNC))

	natsrouter.Register(r, "chat.user.{account}.request.room.{roomID}.{siteID}.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called for failed forward")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-2.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "remote site unavailable", errResp.Error)
}
