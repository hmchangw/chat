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

// Context carries request state through the middleware chain. It implements
// context.Context and is safe to pass anywhere a context.Context is expected,
// including consumers that retain it past the handler's return (net/http
// keep-alive cancel watchers, background goroutines, deferred async work).
//
// Most fields — Msg, Params, keys — are set once at acquire and then stable.
// The underlying ctx field may be replaced by SetContext, but only under the
// single-writer contract: middleware must call SetContext before c.Next(),
// never concurrently with handler-spawned goroutines that read c.Value.
type Context struct {
	ctx    context.Context
	Msg    *nats.Msg
	Params Params
	keys   map[string]any
	mu     sync.RWMutex

	chain *chainState
}

// chainState holds the per-request middleware-chain bookkeeping. It lives in
// a sync.Pool; nothing inside it is exposed via methods an outside goroutine
// would call (Next/Abort/IsAborted are handler-internal), so pool reuse is
// race-free w.r.t. external observers of *Context.
type chainState struct {
	handlers []HandlerFunc
	index    int
}

var chainPool = sync.Pool{
	New: func() any { return &chainState{} },
}

func acquireContext(ctx context.Context, msg *nats.Msg, params Params, handlers []HandlerFunc) *Context {
	cs := chainPool.Get().(*chainState)
	cs.handlers = handlers
	cs.index = -1
	return &Context{
		ctx:    ctx,
		Msg:    msg,
		Params: params,
		chain:  cs,
	}
}

func releaseContext(c *Context) {
	c.chain.handlers = nil
	c.chain.index = 0
	chainPool.Put(c.chain)
	// c itself is left to GC. External ctx consumers may still hold it;
	// every field they can observe is stable from the moment of construction
	// (Msg, Params, keys); the underlying ctx may have been swapped by
	// SetContext during the chain but is no longer mutated once handlers return.
}

// runChain executes the handler chain against c; for tests in this package only.
func runChain(c *Context, handlers []HandlerFunc) {
	cs := chainPool.Get().(*chainState)
	cs.handlers = handlers
	cs.index = -1
	c.chain = cs
	c.Next()
	releaseContext(c)
}

// NewContext creates a Context for testing handlers without a NATS connection.
func NewContext(params map[string]string) *Context {
	return &Context{
		ctx:    context.Background(),
		Params: NewParams(params),
		chain:  &chainState{index: -1},
	}
}

// context.Context implementation — delegates to c.ctx. Safe for async consumers
// provided no middleware calls SetContext concurrently; see SetContext doc.
func (c *Context) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c *Context) Done() <-chan struct{}       { return c.ctx.Done() }
func (c *Context) Err() error                  { return c.ctx.Err() }
func (c *Context) Value(key any) any           { return c.ctx.Value(key) }

// Next executes the next handler in the chain.
func (c *Context) Next() {
	c.chain.index++
	for c.chain.index < len(c.chain.handlers) {
		c.chain.handlers[c.chain.index](c)
		c.chain.index++
	}
}

// Abort stops the middleware chain.
func (c *Context) Abort() {
	c.chain.index = len(c.chain.handlers)
}

// IsAborted returns true if the chain was aborted.
func (c *Context) IsAborted() bool {
	return c.chain.index >= len(c.chain.handlers)
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

// SetContext replaces the underlying context.Context; call only from middleware before c.Next() (single-writer contract — racing with handler-spawned goroutines is unsafe).
func (c *Context) SetContext(ctx context.Context) {
	c.ctx = ctx
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
