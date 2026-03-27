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

func TestHandleRequest_Success(t *testing.T) {
	nc := startTestNATS(t)

	// Subscribe with HandleRequest
	_, err := nc.Subscribe("test.subject", func(msg *nats.Msg) {
		HandleRequest(msg, func(ctx context.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "hello " + req.Name}, nil
		})
	})
	require.NoError(t, err)

	// Send request
	data, _ := json.Marshal(testReq{Name: "world"})
	resp, err := nc.Request("test.subject", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "hello world", result.Greeting)
}

func TestHandleRequest_InvalidJSON(t *testing.T) {
	nc := startTestNATS(t)

	_, err := nc.Subscribe("test.subject", func(msg *nats.Msg) {
		HandleRequest(msg, func(ctx context.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called for invalid JSON")
			return nil, nil
		})
	})
	require.NoError(t, err)

	resp, err := nc.Request("test.subject", []byte("not json"), 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "invalid request payload", errResp.Error)
}

func TestHandleRequest_HandlerError(t *testing.T) {
	nc := startTestNATS(t)

	_, err := nc.Subscribe("test.subject", func(msg *nats.Msg) {
		HandleRequest(msg, func(ctx context.Context, req testReq) (*testResp, error) {
			return nil, fmt.Errorf("something broke")
		})
	})
	require.NoError(t, err)

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.subject", data, 2*time.Second)
	require.NoError(t, err)

	// Should get sanitized error, not the raw "something broke"
	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
}
