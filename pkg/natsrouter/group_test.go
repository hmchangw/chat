package natsrouter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroup_MiddlewareApplied(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	var order []string
	g := r.Group(func(c *Context) {
		order = append(order, "group-mw")
		c.Next()
	})

	Register(g, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			order = append(order, "handler")
			return &testResp{Greeting: "ok"}, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "test.123", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "ok", result.Greeting)
	assert.Equal(t, []string{"group-mw", "handler"}, order)
}

func TestGroup_RouterMiddlewareRunsFirst(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	doneCh := make(chan []string, 1)
	var order []string

	r.Use(func(c *Context) {
		c.Next()
		doneCh <- order
	})
	r.Use(func(c *Context) {
		order = append(order, "router-mw")
		c.Next()
	})

	g := r.Group(func(c *Context) {
		order = append(order, "group-mw")
		c.Next()
	})

	Register(g, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			order = append(order, "handler")
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	_, err := nc.Request(context.Background(), "test.123", data, 2*time.Second)
	require.NoError(t, err)

	result := <-doneCh
	assert.Equal(t, []string{"router-mw", "group-mw", "handler"}, result)
}

func TestGroup_NestedGroups(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	doneCh := make(chan []string, 1)
	var order []string

	r.Use(func(c *Context) {
		c.Next()
		doneCh <- order
	})

	outer := r.Group(func(c *Context) {
		order = append(order, "outer")
		c.Next()
	})
	inner := outer.Group(func(c *Context) {
		order = append(order, "inner")
		c.Next()
	})

	Register(inner, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			order = append(order, "handler")
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	_, err := nc.Request(context.Background(), "test.123", data, 2*time.Second)
	require.NoError(t, err)

	result := <-doneCh
	assert.Equal(t, []string{"outer", "inner", "handler"}, result)
}

func TestGroup_Use(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	doneCh := make(chan []string, 1)
	var order []string

	r.Use(func(c *Context) {
		c.Next()
		doneCh <- order
	})

	g := r.Group()
	g.Use(func(c *Context) {
		order = append(order, "added-via-use")
		c.Next()
	})

	Register(g, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			order = append(order, "handler")
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	_, err := nc.Request(context.Background(), "test.123", data, 2*time.Second)
	require.NoError(t, err)

	result := <-doneCh
	assert.Equal(t, []string{"added-via-use", "handler"}, result)
}

func TestGroup_Isolation(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	g1 := r.Group(func(c *Context) {
		c.Set("from", "g1")
		c.Next()
	})
	g2 := r.Group(func(c *Context) {
		c.Set("from", "g2")
		c.Next()
	})

	Register(g1, "test.g1.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			from, _ := c.Get("from")
			return &testResp{Greeting: from.(string)}, nil
		})
	Register(g2, "test.g2.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			from, _ := c.Get("from")
			return &testResp{Greeting: from.(string)}, nil
		})

	data, _ := json.Marshal(testReq{})

	resp1, err := nc.Request(context.Background(), "test.g1.123", data, 2*time.Second)
	require.NoError(t, err)
	var r1 testResp
	require.NoError(t, json.Unmarshal(resp1.Data, &r1))
	assert.Equal(t, "g1", r1.Greeting)

	resp2, err := nc.Request(context.Background(), "test.g2.456", data, 2*time.Second)
	require.NoError(t, err)
	var r2 testResp
	require.NoError(t, json.Unmarshal(resp2.Data, &r2))
	assert.Equal(t, "g2", r2.Greeting)
}

func TestGroup_ShortCircuit(t *testing.T) {
	nc := startTestNATS(t)
	r := New(nc, "test-service")

	g := r.Group(func(c *Context) {
		c.ReplyError("blocked by group")
		c.Abort()
	})

	handlerCalled := false
	Register(g, "test.{id}",
		func(c *Context, req testReq) (*testResp, error) {
			handlerCalled = true
			return &testResp{}, nil
		})

	data, _ := json.Marshal(testReq{})
	resp, err := nc.Request(context.Background(), "test.123", data, 2*time.Second)
	require.NoError(t, err)

	assert.False(t, handlerCalled)
	assert.Contains(t, string(resp.Data), "blocked by group")
}
