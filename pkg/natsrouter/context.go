package natsrouter

import (
	"context"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// HandlerFunc is the function type for handlers and middleware.
// Middleware calls c.Next() to continue the chain.
type HandlerFunc func(c *Context)

// Context carries request state through the middleware chain.
// It implements context.Context so it can be passed directly to database calls.
// Contexts are pooled — do not hold references to a Context after the handler returns.
type Context struct {
	ctx      context.Context
	Msg      *nats.Msg
	Params   Params
	keys     map[string]any
	handlers []HandlerFunc
	index    int
}

var ctxPool = sync.Pool{
	New: func() any { return &Context{} },
}

func acquireContext(msg *nats.Msg, params Params, handlers []HandlerFunc) *Context {
	c := ctxPool.Get().(*Context)
	c.ctx = context.Background()
	c.Msg = msg
	c.Params = params
	c.keys = nil // lazy init on first Set()
	c.handlers = handlers
	c.index = -1
	return c
}

func releaseContext(c *Context) {
	c.ctx = nil
	c.Msg = nil
	c.Params = Params{}
	c.keys = nil
	c.handlers = nil
	ctxPool.Put(c)
}

// NewContext creates a Context for testing handlers without a NATS connection.
func NewContext(params map[string]string) *Context {
	return &Context{
		ctx:    context.Background(),
		Params: NewParams(params),
		index:  -1,
	}
}

// context.Context implementation
func (c *Context) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c *Context) Done() <-chan struct{}       { return c.ctx.Done() }
func (c *Context) Err() error                  { return c.ctx.Err() }
func (c *Context) Value(key any) any           { return c.ctx.Value(key) }

// Next executes the next handler in the chain.
func (c *Context) Next() {
	c.index++
	for c.index < len(c.handlers) {
		c.handlers[c.index](c)
		c.index++
	}
}

// Abort stops the middleware chain.
func (c *Context) Abort() {
	c.index = len(c.handlers)
}

// IsAborted returns true if the chain was aborted.
func (c *Context) IsAborted() bool {
	return c.index >= len(c.handlers)
}

// Set stores a key-value pair for downstream handlers.
func (c *Context) Set(key string, value any) {
	if c.keys == nil {
		c.keys = make(map[string]any)
	}
	c.keys[key] = value
}

// Get returns a value by key and whether it was found.
func (c *Context) Get(key string) (any, bool) {
	if c.keys == nil {
		return nil, false
	}
	val, ok := c.keys[key]
	return val, ok
}

// MustGet returns a value by key. Panics if not found.
func (c *Context) MustGet(key string) any {
	val, ok := c.Get(key)
	if !ok {
		panic("natsrouter: key " + key + " not found in context")
	}
	return val
}

// Param returns a named parameter from the subject. Shortcut for c.Params.Get(key).
func (c *Context) Param(key string) string {
	return c.Params.Get(key)
}

// ReplyJSON marshals v as JSON and sends it as the reply.
func (c *Context) ReplyJSON(v any) {
	natsutil.ReplyJSON(c.Msg, v)
}

// ReplyError sends an error response to the client.
func (c *Context) ReplyError(msg string) {
	natsutil.ReplyError(c.Msg, msg)
}
