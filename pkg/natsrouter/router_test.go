package natsrouter

import (
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
	require.True(t, ns.ReadyForConnections(5*time.Second), "nats server did not become ready")
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func TestRegister_Success(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "chat.user.{account}.request.room.{roomID}.site-1.msg.test",
		func(c *Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "hello " + req.Name + " from " + c.Param("account")}, nil
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
	Register(r, "chat.user.{account}.request.room.{roomID}.{siteID}.msg.test",
		func(c *Context, req testReq) (*testResp, error) {
			captured = c.Params
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	_, err := nc.Request("chat.user.alice.request.room.room-42.site-prod.msg.test", data, 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "alice", captured.Get("account"))
	assert.Equal(t, "room-42", captured.Get("roomID"))
	assert.Equal(t, "site-prod", captured.Get("siteID"))
}

func TestRegister_InvalidJSON(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	Register(r, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
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
		func(c *Context, req testReq) (*testResp, error) {
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

	RegisterNoBody(r, "chat.user.{account}.request.rooms.get.{roomID}",
		func(c *Context) (*testResp, error) {
			return &testResp{Greeting: "room " + c.Param("roomID")}, nil
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
	r.Use(func(c *Context) {
		c.Next()
		doneCh <- order // send after entire chain (all afters) is done
	})

	makeMiddleware := func(name string) HandlerFunc {
		return func(c *Context) {
			order = append(order, name+":before")
			c.Next()
			order = append(order, name+":after")
		}
	}

	r.Use(makeMiddleware("A"))
	r.Use(makeMiddleware("B"))
	r.Use(makeMiddleware("C"))

	Register(r, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
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

	r.Use(func(c *Context) {
		// Short-circuit — abort and respond directly
		c.Abort()
		c.Msg.Respond([]byte(`{"rejected":true}`))
	})

	handlerCalled := false
	Register(r, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
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
		func(c *Context, req testReq) (*testResp, error) {
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
		func(c *Context, req testReq) (*testResp, error) {
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
		func(c *Context, req testReq) (*testResp, error) {
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
		func(c *Context, req testReq) (*testResp, error) {
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
		func(c *Context, req testReq) (*testResp, error) {
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
		func(c *Context, req testReq) error {
			processed <- c.Param("type") + ":" + req.Name
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
		func(c *Context, req testReq) error {
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
		func(c *Context, req testReq) (*testResp, error) {
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

func TestContext_SetGet(t *testing.T) {
	c := NewContext(map[string]string{"id": "123"})
	c.Set("user", "alice")

	val, ok := c.Get("user")
	assert.True(t, ok)
	assert.Equal(t, "alice", val)

	assert.Equal(t, "alice", c.MustGet("user"))
	assert.Equal(t, "123", c.Param("id"))

	_, ok = c.Get("nonexistent")
	assert.False(t, ok)
}

func TestContext_MustGet_Panics(t *testing.T) {
	c := NewContext(nil)
	require.Panics(t, func() { c.MustGet("nope") })
}

func TestContext_Abort(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	handlerCalled := false
	r.Use(func(c *Context) {
		c.Abort()
		// Don't call Next
	})

	Register(r, "test.abort",
		func(c *Context, req testReq) (*testResp, error) {
			handlerCalled = true
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	_, _ = nc.Request("test.abort", data, 200*time.Millisecond)
	assert.False(t, handlerCalled)
}

func TestRequestID_Generated(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")
	r.Use(RequestID())

	var capturedID string
	Register(r, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			val, ok := c.Get("requestID")
			require.True(t, ok)
			capturedID = val.(string)
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	_, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)
	assert.NotEmpty(t, capturedID)
}

func TestRequestID_FromHeader(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")
	r.Use(RequestID())

	var capturedID string
	Register(r, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			capturedID = c.MustGet("requestID").(string)
			return &testResp{}, nil
		})

	msg := nats.NewMsg("test.123")
	msg.Data, _ = json.Marshal(testReq{Name: "test"})
	msg.Header = nats.Header{}
	msg.Header.Set("X-Request-ID", "custom-req-id-42")

	resp, err := nc.RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)
	assert.NotEmpty(t, string(resp.Data))
	assert.Equal(t, "custom-req-id-42", capturedID)
}

func TestRegisterNoBody_HandlerError(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	RegisterNoBody(r, "test.{id}",
		func(c *Context) (*testResp, error) {
			return nil, fmt.Errorf("something failed")
		})

	resp, err := nc.Request("test.123", nil, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
}

func TestRegisterNoBody_RouteError(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	RegisterNoBody(r, "test.{id}",
		func(c *Context) (*testResp, error) {
			return nil, ErrNotFound("item not found")
		})

	resp, err := nc.Request("test.123", nil, 2*time.Second)
	require.NoError(t, err)

	var result RouteError
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "item not found", result.Message)
	assert.Equal(t, "not_found", result.Code)
}

func TestLogging_LogsRequest(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")
	r.Use(Logging())

	Register(r, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "ok"}, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "ok", result.Greeting)
}

func TestReplyRouteError(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	r.Use(func(c *Context) {
		c.ReplyRouteError(ErrForbidden("access denied"))
		c.Abort()
	})

	Register(r, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request("test.123", data, 2*time.Second)
	require.NoError(t, err)

	var result RouteError
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "access denied", result.Message)
	assert.Equal(t, "forbidden", result.Code)
}

func TestErrConstants(t *testing.T) {
	e := ErrBadRequest("invalid input")
	assert.Equal(t, "bad_request", e.Code)
	assert.Equal(t, "invalid input", e.Message)

	e = ErrNotFound("not here")
	assert.Equal(t, "not_found", e.Code)

	e = ErrForbidden("nope")
	assert.Equal(t, "forbidden", e.Code)

	e = ErrConflict("already exists")
	assert.Equal(t, "conflict", e.Code)
}
