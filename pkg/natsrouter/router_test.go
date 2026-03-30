package natsrouter

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
	r := New(nc, "test-service")

	Register(r, "chat.user.{userID}.request.room.{roomID}.site-1.msg.test",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			return &testResp{Greeting: "hello " + req.Name + " from " + p.Get("userID")}, nil
		})

	data, _ := json.Marshal(testReq{Name: "world"})
	resp, err := nc.Request("chat.user.alice.request.room.r1.site-1.msg.test", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "hello world from alice", result.Greeting)
}

func TestRegister_ParamsExtraction(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	var captured Params
	Register(r, "chat.user.{userID}.request.room.{roomID}.{siteID}.msg.test",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			captured = p
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	_, err := nc.Request("chat.user.alice.request.room.room-42.site-prod.msg.test", data, 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "alice", captured.Get("userID"))
	assert.Equal(t, "room-42", captured.Get("roomID"))
	assert.Equal(t, "site-prod", captured.Get("siteID"))
}

func TestRegister_InvalidJSON(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called for invalid JSON")
			return nil, nil
		})

	resp, err := nc.Request("test.123", []byte("not json"), 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "invalid request payload", errResp.Error)
}

func TestRegister_HandlerError(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			return nil, fmt.Errorf("something broke")
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
}

func TestRegisterNoBody_Success(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	RegisterNoBody(r, "chat.user.{userID}.request.rooms.get.{roomID}",
		func(ctx context.Context, p Params) (*testResp, error) {
			return &testResp{Greeting: "room " + p.Get("roomID")}, nil
		})

	resp, err := nc.Request("chat.user.alice.request.rooms.get.room-42", nil, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "room room-42", result.Greeting)
}

func TestMiddleware_ExecutionOrder(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	doneCh := make(chan []string, 1)

	// Use the outermost middleware to capture the full order after chain completes.
	var order []string
	r.Use(func(next nats.MsgHandler) nats.MsgHandler {
		return func(msg *nats.Msg) {
			next(msg)
			doneCh <- order // send after entire chain (all afters) is done
		}
	})

	makeMiddleware := func(name string) Middleware {
		return func(next nats.MsgHandler) nats.MsgHandler {
			return func(msg *nats.Msg) {
				order = append(order, name+":before")
				next(msg)
				order = append(order, name+":after")
			}
		}
	}

	r.Use(makeMiddleware("A"))
	r.Use(makeMiddleware("B"))
	r.Use(makeMiddleware("C"))

	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			order = append(order, "handler")
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	_, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	result := <-doneCh
	assert.Equal(t, []string{
		"A:before", "B:before", "C:before",
		"handler",
		"C:after", "B:after", "A:after",
	}, result)
}

func TestMiddleware_ShortCircuit(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	r.Use(func(next nats.MsgHandler) nats.MsgHandler {
		return func(msg *nats.Msg) {
			// Short-circuit — don't call next
			msg.Respond([]byte(`{"rejected":true}`))
		}
	})

	handlerCalled := false
	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			handlerCalled = true
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	assert.False(t, handlerCalled)
	assert.Contains(t, string(resp.Data), "rejected")
}

func TestRecovery_CatchesPanic(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")
	r.Use(Recovery())

	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			panic("boom!")
		})

	data, _ := json.Marshal(testReq{})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
}

func TestRegister_NoParams(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "static.subject",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			return &testResp{Greeting: "hello " + req.Name}, nil
		})

	data, _ := json.Marshal(testReq{Name: "world"})
	resp, err := nc.Request("static.subject", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "hello world", result.Greeting)
}

func TestRegister_RouteError(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			return nil, ErrWithCode("not_found", "thing not found")
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var result RouteError
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "thing not found", result.Message)
	assert.Equal(t, "not_found", result.Code)
}

func TestRegister_RouteErrorSimple(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			return nil, Errf("user %s not allowed", "alice")
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var result RouteError
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "user alice not allowed", result.Message)
	assert.Equal(t, "", result.Code)
}

func TestRegister_InternalErrorNotExposed(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			return nil, fmt.Errorf("database connection refused")
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
	assert.NotContains(t, string(resp.Data), "database")
}

func TestRegisterVoid_Success(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	processed := make(chan string, 1)
	RegisterVoid(r, "events.{type}",
		func(ctx context.Context, p Params, req testReq) error {
			processed <- p.Get("type") + ":" + req.Name
			return nil
		})

	data, _ := json.Marshal(testReq{Name: "hello"})
	err := nc.Publish("events.typing", data)
	require.NoError(t, err)

	select {
	case result := <-processed:
		assert.Equal(t, "typing:hello", result)
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called within timeout")
	}
}

func TestRegisterVoid_NoReply(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	RegisterVoid(r, "events.{type}",
		func(ctx context.Context, p Params, req testReq) error {
			return nil
		})

	// Use Request (expects reply) — should timeout since RegisterVoid doesn't reply
	data, _ := json.Marshal(testReq{Name: "hello"})
	_, err := nc.Request("events.typing", data, 200*time.Millisecond)
	assert.ErrorIs(t, err, nats.ErrTimeout)
}

func TestRouteError_Error(t *testing.T) {
	e := ErrWithCode("not_found", "room not found")
	assert.Equal(t, "not_found: room not found", e.Error())

	e2 := Err("simple error")
	assert.Equal(t, "simple error", e2.Error())
}

func TestRouteError_WrappedInFmtErrorf(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	// RouteError wrapped with fmt.Errorf should still be detected via errors.As
	Register(r, "test.{id}",
		func(ctx context.Context, p Params, req testReq) (*testResp, error) {
			return nil, fmt.Errorf("context: %w", ErrWithCode("forbidden", "not allowed"))
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var result RouteError
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "not allowed", result.Message)
	assert.Equal(t, "forbidden", result.Code)
}
