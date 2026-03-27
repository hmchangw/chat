package natshandler

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

	"github.com/hmchangw/chat/pkg/model"
)

type testReq struct {
	Name string `json:"name"`
}

type testResp struct {
	Greeting string `json:"greeting"`
}

func startTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Port: -1}
	ns, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	ns.Start()
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func TestRegister_Success(t *testing.T) {
	nc := startTestNATS(t)
	h := New(nc, "test-service")

	err := Register(h, "chat.user.*.request.room.*.site-1.msg.test", func(ctx context.Context, userID string, req testReq) (*testResp, error) {
		return &testResp{Greeting: "hello " + req.Name + " from " + userID}, nil
	})
	require.NoError(t, err)

	data, _ := json.Marshal(testReq{Name: "world"})
	resp, err := nc.Request("chat.user.u1.request.room.r1.site-1.msg.test", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "hello world from u1", result.Greeting)
}

func TestRegister_InvalidJSON(t *testing.T) {
	nc := startTestNATS(t)
	h := New(nc, "test-service")

	err := Register(h, "chat.user.*.request.room.*.site-1.msg.test", func(ctx context.Context, userID string, req testReq) (*testResp, error) {
		t.Fatal("handler should not be called for invalid JSON")
		return nil, nil
	})
	require.NoError(t, err)

	resp, err := nc.Request("chat.user.u1.request.room.r1.site-1.msg.test", []byte("not json"), 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "invalid request payload", errResp.Error)
}

func TestRegister_HandlerError(t *testing.T) {
	nc := startTestNATS(t)
	h := New(nc, "test-service")

	err := Register(h, "chat.user.*.request.room.*.site-1.msg.test", func(ctx context.Context, userID string, req testReq) (*testResp, error) {
		return nil, fmt.Errorf("something broke")
	})
	require.NoError(t, err)

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("chat.user.u1.request.room.r1.site-1.msg.test", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
}

func TestRegister_InvalidSubject(t *testing.T) {
	nc := startTestNATS(t)
	h := New(nc, "test-service")

	err := Register(h, "invalid.subject.*", func(ctx context.Context, userID string, req testReq) (*testResp, error) {
		t.Fatal("handler should not be called for invalid subject")
		return nil, nil
	})
	require.NoError(t, err)

	resp, err := nc.Request("invalid.subject.foo", []byte("{}"), 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "invalid subject", errResp.Error)
}

func TestRegister_ExtractsUserID(t *testing.T) {
	nc := startTestNATS(t)
	h := New(nc, "test-service")

	var capturedUserID string
	err := Register(h, "chat.user.*.request.room.*.site-1.msg.test", func(ctx context.Context, userID string, req testReq) (*testResp, error) {
		capturedUserID = userID
		return &testResp{}, nil
	})
	require.NoError(t, err)

	data, _ := json.Marshal(testReq{Name: "test"})
	_, err = nc.Request("chat.user.alice123.request.room.r1.site-1.msg.test", data, 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "alice123", capturedUserID)
}
