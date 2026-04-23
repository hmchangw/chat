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
//
// Contexts are pooled — do not hold references to a Context after the handler
// returns. If you need the context in a goroutine or after the response is
// sent (async metrics, deferred logging, background work), call Copy() to
// obtain a detached, pool-safe copy.
type Context struct {
	ctx      context.Context
	Msg      *nats.Msg
	Params   Params
	keys     map[string]any
	handlers []HandlerFunc
	index    int

	// mu protects keys. Set/Get may race against a Copy() reader running in
	// a goroutine, or against concurrent middleware that has been extended
	// to share the context across goroutines.
	mu sync.RWMutex
}

var ctxPool = sync.Pool{
	New: func() any { return &Context{} },
}

func acquireContext(ctx context.Context, msg *nats.Msg, params Params, handlers []HandlerFunc) *Context {
	c := ctxPool.Get().(*Context)
	c.ctx = ctx
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

// Copy returns a detached copy of the Context that is safe to use after the
// handler returns (e.g., in goroutines, deferred closures, async metric
// emission). The copy:
//
//   - is NOT managed by the pool, so the underlying struct will not be
//     reused while a goroutine still holds the reference,
//   - deep-copies the keys map and the Params map so mutations to the
//     original (including pool reuse by a subsequent request) cannot leak
//     into the copy,
//   - shares the Msg pointer — do not call Respond on it from a background
//     goroutine; publish a fresh NATS message instead,
//   - has no handler chain: Next() is a no-op and IsAborted() returns true.
func (c *Context) Copy() *Context {
	cp := &Context{
		ctx:    c.ctx,
		Msg:    c.Msg,
		Params: cloneParams(c.Params),
	}
	c.mu.RLock()
	if len(c.keys) > 0 {
		cp.keys = make(map[string]any, len(c.keys))
		for k, v := range c.keys {
			cp.keys[k] = v
		}
	}
	c.mu.RUnlock()
	// handlers is nil and index is 0; Next() and Abort() semantics follow
	// naturally because c.index >= len(c.handlers) == 0.
	return cp
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
	c.mu.Lock()
	if c.keys == nil {
		c.keys = make(map[string]any)
	}
	c.keys[key] = value
	c.mu.Unlock()
}

// Get returns a value by key and whether it was found.
func (c *Context) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
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

// ReplyRouteError sends a structured error response with an optional code.
// Use this from middleware when you need machine-readable error codes.
func (c *Context) ReplyRouteError(e *RouteError) {
	natsutil.ReplyJSON(c.Msg, e)
}
