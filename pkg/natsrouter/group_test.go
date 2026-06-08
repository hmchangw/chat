package natsrouter

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// recordingRegistrar captures the (pattern, handlers) a Group forwards to its
// parent, so Group composition is unit-testable without a NATS subscription.
type recordingRegistrar struct {
	pattern  string
	handlers []HandlerFunc
}

func (r *recordingRegistrar) addRoute(pattern string, handlers []HandlerFunc) {
	r.pattern = pattern
	r.handlers = handlers
}

func TestGroup_PrependsMiddlewareInOrder(t *testing.T) {
	var order []string
	mkMW := func(tag string) HandlerFunc {
		return func(c *Context) { order = append(order, tag); c.Next() }
	}
	rec := &recordingRegistrar{}
	g := &Group{parent: rec, middleware: []HandlerFunc{mkMW("group1"), mkMW("group2")}}

	g.addRoute("subj.x", []HandlerFunc{func(c *Context) { order = append(order, "handler") }})

	require.Equal(t, "subj.x", rec.pattern)
	require.Len(t, rec.handlers, 3, "group middleware (2) + handler (1)")

	// Run the recorded chain to verify execution order.
	c := &Context{ctx: context.Background(), chain: &chainState{index: -1}}
	c.chain.handlers = rec.handlers
	c.Next()
	assert.Equal(t, []string{"group1", "group2", "handler"}, order)
}

func TestRouterGroup_WiresParentAndMiddleware(t *testing.T) {
	r := New(nil, "q")
	g := r.Group(RequireRequestID())
	require.NotNil(t, g)
	p, ok := g.parent.(*Router)
	require.True(t, ok, "parent must be the originating *Router")
	assert.Same(t, r, p)
	assert.Len(t, g.middleware, 1)
}

// A base-router chain (RequestID only) mints a request ID for a header-less
// message and runs the handler — this is the relaxed RoomsInfoBatch posture.
func TestChain_BaseMintsAndRuns_NoHeader(t *testing.T) {
	c := &Context{
		ctx:   context.Background(),
		Msg:   &nats.Msg{Subject: "x", Header: nats.Header{}},
		chain: &chainState{index: -1},
	}
	var ran bool
	var seen string
	c.chain.handlers = []HandlerFunc{
		RequestID(),
		func(c *Context) { ran = true; seen = natsutil.RequestIDFromContext(c) },
	}
	c.Next()

	require.True(t, ran, "handler must run on the minting base chain")
	assert.NotEmpty(t, seen, "RequestID must mint an ID when the header is absent")
}

// A base + strict-group chain (RequestID then RequireRequestID) still rejects a
// header-less message — this is the strict posture for dedup-critical routes.
func TestChain_StrictGroupRejects_NoHeader(t *testing.T) {
	c := &Context{
		ctx:   context.Background(),
		Msg:   &nats.Msg{Subject: "x", Header: nats.Header{}},
		chain: &chainState{index: -1},
	}
	var ran bool
	c.chain.handlers = []HandlerFunc{
		RequestID(),
		RequireRequestID(),
		func(c *Context) { ran = true },
	}
	c.Next()

	assert.False(t, ran, "strict group must reject a header-less message")
	assert.True(t, c.IsAborted())
}

func TestChain_StrictGroupPasses_ValidHeader(t *testing.T) {
	const id = "01970a4f-8c2d-7c9a-abcd-e0123456789f"
	c := &Context{
		ctx:   context.Background(),
		Msg:   &nats.Msg{Subject: "x", Header: nats.Header{natsutil.RequestIDHeader: []string{id}}},
		chain: &chainState{index: -1},
	}
	var ran bool
	c.chain.handlers = []HandlerFunc{
		RequestID(),
		RequireRequestID(),
		func(c *Context) { ran = true },
	}
	c.Next()

	require.True(t, ran, "strict group must pass a valid header through")
	got, ok := c.Get(requestIDKey)
	require.True(t, ok, "valid request ID must be stamped on the pass path")
	assert.Equal(t, id, got)
}
