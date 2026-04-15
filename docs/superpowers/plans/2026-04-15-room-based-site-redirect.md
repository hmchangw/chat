# Room-Based Site Redirect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace subject-based siteID routing in history-service with room-lookup-based routing — the server fetches the room from MongoDB and redirects based on `room.SiteID`.

**Architecture:** Subject patterns bake in the local siteID at registration time (no more `{siteID}` wildcard). A new `RouterGroup` in natsrouter enables per-group middleware. The `SiteProxy` middleware fetches the room from MongoDB, compares `room.SiteID` to the local config, and forwards to the correct site if they differ. `GetMessageByID` is registered outside the group (no room lookup needed).

**Tech Stack:** Go 1.25, NATS natsrouter, MongoDB (mongo-driver v2), testify, go.uber.org/mock

---

### Task 1: Subject Pattern Functions — Take siteID Param

**Files:**
- Modify: `pkg/subject/subject.go:154-168`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests for the new signatures**

Add a new test table in `pkg/subject/subject_test.go` after the existing `TestWildcardPatterns`:

```go
func TestNatsrouterPatterns(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"MsgHistoryPattern", subject.MsgHistoryPattern("site-a"),
			"chat.user.{account}.request.room.{roomID}.site-a.msg.history"},
		{"MsgNextPattern", subject.MsgNextPattern("site-a"),
			"chat.user.{account}.request.room.{roomID}.site-a.msg.next"},
		{"MsgSurroundingPattern", subject.MsgSurroundingPattern("site-a"),
			"chat.user.{account}.request.room.{roomID}.site-a.msg.surrounding"},
		{"MsgGetPattern", subject.MsgGetPattern("site-a"),
			"chat.user.{account}.request.room.{roomID}.site-a.msg.get"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
make test SERVICE=pkg/subject
```

Expected: compilation error — `MsgHistoryPattern` etc. accept no arguments.

- [ ] **Step 3: Update the 4 pattern functions in `subject.go`**

Replace lines 152-168 in `pkg/subject/subject.go`:

```go
// --- natsrouter patterns (use {param} placeholders for named extraction) ---

func MsgHistoryPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.history", siteID)
}

func MsgNextPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.next", siteID)
}

func MsgSurroundingPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.surrounding", siteID)
}

func MsgGetPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.get", siteID)
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
make test SERVICE=pkg/subject
```

Expected: all pass including the new `TestNatsrouterPatterns`.

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): accept siteID param in history pattern functions"
```

---

### Task 2: RouterGroup in natsrouter

**Files:**
- Create: `pkg/natsrouter/group.go`
- Create: `pkg/natsrouter/group_test.go`

- [ ] **Step 1: Write failing tests for RouterGroup**

Create `pkg/natsrouter/group_test.go`:

```go
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
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
make test SERVICE=pkg/natsrouter
```

Expected: compilation error — `RouterGroup` type does not exist, `Router.Group` method does not exist.

- [ ] **Step 3: Implement RouterGroup**

Create `pkg/natsrouter/group.go`:

```go
package natsrouter

// RouterGroup groups routes that share middleware. It implements
// Registrar so typed handlers (Register, RegisterNoBody, RegisterVoid)
// can be registered on a group exactly like on a Router.
//
// Create a group via Router.Group or RouterGroup.Group.
// Middleware added to a group only applies to routes registered on that group.
type RouterGroup struct {
	parent   Registrar
	handlers []HandlerFunc
}

// Group creates a new RouterGroup with the given middleware.
// The group delegates route registration to the Router (via parent chain).
// Middleware chain order: router middleware → parent group(s) → this group → handler.
func (r *Router) Group(mw ...HandlerFunc) *RouterGroup {
	return &RouterGroup{
		parent:   r,
		handlers: copyHandlers(mw),
	}
}

// Group creates a nested sub-group that inherits the parent group's middleware.
func (g *RouterGroup) Group(mw ...HandlerFunc) *RouterGroup {
	return &RouterGroup{
		parent:   g,
		handlers: copyHandlers(mw),
	}
}

// Use appends middleware to this group's chain.
func (g *RouterGroup) Use(mw ...HandlerFunc) {
	g.handlers = append(g.handlers, mw...)
}

// addRoute prepends the group's middleware to the handler slice,
// then delegates to the parent Registrar. When the parent is a Router,
// the router's own middleware is prepended there — producing the full chain:
// router middleware → group middleware → handler.
func (g *RouterGroup) addRoute(pattern string, handlers []HandlerFunc) {
	all := make([]HandlerFunc, 0, len(g.handlers)+len(handlers))
	all = append(all, g.handlers...)
	all = append(all, handlers...)
	g.parent.addRoute(pattern, all)
}

// copyHandlers returns a copy of the slice to prevent mutation of the caller's slice.
func copyHandlers(mw []HandlerFunc) []HandlerFunc {
	if len(mw) == 0 {
		return nil
	}
	cp := make([]HandlerFunc, len(mw))
	copy(cp, mw)
	return cp
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
make test SERVICE=pkg/natsrouter
```

Expected: all tests pass including the 6 new group tests.

- [ ] **Step 5: Run lint**

```bash
make lint
```

Expected: no lint errors.

- [ ] **Step 6: Commit**

```bash
git add pkg/natsrouter/group.go pkg/natsrouter/group_test.go
git commit -m "feat(natsrouter): add RouterGroup with Gin-style middleware grouping"
```

---

### Task 3: RoomRepo in history-service

**Files:**
- Create: `history-service/internal/mongorepo/room.go`

- [ ] **Step 1: Create RoomRepo**

Create `history-service/internal/mongorepo/room.go`:

```go
package mongorepo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

const roomsCollection = "rooms"

// RoomRepo implements room lookups using MongoDB.
type RoomRepo struct {
	rooms *Collection[model.Room]
}

// NewRoomRepo creates a new MongoDB room repository.
func NewRoomRepo(db *mongo.Database) *RoomRepo {
	return &RoomRepo{
		rooms: NewCollection[model.Room](db.Collection(roomsCollection)),
	}
}

// GetRoom returns a room by its ID.
// Returns (nil, nil) when the room does not exist.
func (r *RoomRepo) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	return r.rooms.FindByID(ctx, roomID)
}
```

- [ ] **Step 2: Verify compilation**

```bash
make build SERVICE=history-service
```

Expected: compiles successfully.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/mongorepo/room.go
git commit -m "feat(history): add RoomRepo for MongoDB room lookups"
```

---

### Task 4: Rewrite SiteProxy Middleware

**Files:**
- Modify: `history-service/internal/middleware/siteproxy.go`
- Modify: `history-service/internal/middleware/siteproxy_test.go`

- [ ] **Step 1: Write failing tests for the new middleware behavior**

Replace the entire contents of `history-service/internal/middleware/siteproxy_test.go`:

```go
package middleware_test

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

// fakeRoomFinder is a test double for middleware.RoomFinder.
type fakeRoomFinder struct {
	rooms map[string]*model.Room
	err   error
}

func (f *fakeRoomFinder) GetRoom(_ context.Context, roomID string) (*model.Room, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rooms[roomID], nil
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

func TestSiteProxy_LocalRoom(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-1"},
	}}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
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

func TestSiteProxy_RemoteRoom_ForwardsCorrectly(t *testing.T) {
	nc := startTestNATS(t)
	forwardNC, err := nats.Connect(nc.NatsConn().ConnectedUrl())
	require.NoError(t, err)
	t.Cleanup(forwardNC.Close)

	// Remote site-2 subscriber handles forwarded requests.
	forwardNC.Subscribe("chat.user.*.request.room.*.site-2.msg.history",
		func(msg *nats.Msg) {
			if msg.Header != nil && msg.Header.Get("X-Forwarded-Site") != "" {
				msg.Respond([]byte(`{"greeting":"from site-2"}`))
			}
		})
	forwardNC.Flush()

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-2"},
	}}

	r := natsrouter.New(nc, "test-service")
	g := r.Group(middleware.SiteProxy("site-1", forwardNC, rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "local"}, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "from site-2", result.Greeting)
}

func TestSiteProxy_ForwardedHeader_SkipsLookup(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	// Room belongs to site-2, but the forwarded header should skip the lookup entirely.
	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-2"},
	}}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			return &testResp{Greeting: "forwarded-to-me"}, nil
		})

	msg := nats.NewMsg("chat.user.alice.request.room.r1.site-1.msg.history")
	msg.Data, _ = json.Marshal(testReq{Name: "test"})
	msg.Header = nats.Header{}
	msg.Header.Set("X-Forwarded-Site", "site-2")

	resp, err := nc.NatsConn().RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)

	var result testResp
	require.NoError(t, json.Unmarshal(resp.Data, &result))
	assert.Equal(t, "forwarded-to-me", result.Greeting)
}

func TestSiteProxy_RoomNotFound(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{}}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called when room not found")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "room not found", errResp.Error)
}

func TestSiteProxy_RoomLookupError(t *testing.T) {
	nc := startTestNATS(t)
	r := natsrouter.New(nc, "test-service")

	rooms := &fakeRoomFinder{err: fmt.Errorf("db connection refused")}

	g := r.Group(middleware.SiteProxy("site-1", nc.NatsConn(), rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called on room lookup error")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "internal error", errResp.Error)
}

func TestSiteProxy_RemoteRoom_ForwardError(t *testing.T) {
	nc := startTestNATS(t)

	forwardNC, err := nats.Connect(nc.NatsConn().ConnectedUrl())
	require.NoError(t, err)
	forwardNC.Close() // Close immediately to simulate failure.

	rooms := &fakeRoomFinder{rooms: map[string]*model.Room{
		"r1": {ID: "r1", SiteID: "site-2"},
	}}

	r := natsrouter.New(nc, "test-service")
	g := r.Group(middleware.SiteProxy("site-1", forwardNC, rooms))

	natsrouter.Register(g, "chat.user.{account}.request.room.{roomID}.site-1.msg.history",
		func(c *natsrouter.Context, req testReq) (*testResp, error) {
			t.Fatal("handler should not be called for failed forward")
			return nil, nil
		})

	data, _ := json.Marshal(testReq{Name: "test"})
	resp, err := nc.Request(context.Background(), "chat.user.alice.request.room.r1.site-1.msg.history", data, 2*time.Second)
	require.NoError(t, err)

	var errResp model.ErrorResponse
	require.NoError(t, json.Unmarshal(resp.Data, &errResp))
	assert.Equal(t, "remote site unavailable", errResp.Error)
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
make test SERVICE=history-service/internal/middleware
```

Expected: compilation error — `SiteProxy` signature doesn't match (expects `RoomFinder` arg).

- [ ] **Step 3: Rewrite the middleware**

Replace the entire contents of `history-service/internal/middleware/siteproxy.go`:

```go
package middleware

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// forwardedSiteHeader marks a request as already forwarded by SiteProxy.
const forwardedSiteHeader = "X-Forwarded-Site"

// siteProxyTimeout is the deadline for forwarded cross-site NATS requests.
const siteProxyTimeout = 10 * time.Second

// RoomFinder looks up a room by ID. Implemented by mongorepo.RoomRepo.
type RoomFinder interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
}

// SiteProxy returns middleware that transparently forwards requests to remote sites.
// It fetches the room from MongoDB using the "roomID" subject parameter. If the room's
// SiteID matches localSiteID, the request is handled locally. Otherwise it is forwarded
// to the remote site by replacing localSiteID with room.SiteID in the subject.
//
// Requests already forwarded from another site (X-Forwarded-Site header present) are
// handled locally to prevent infinite loops.
func SiteProxy(localSiteID string, nc *nats.Conn, rooms RoomFinder) natsrouter.HandlerFunc {
	return func(c *natsrouter.Context) {
		// Already forwarded from another site — handle locally.
		if c.Msg != nil && c.Msg.Header != nil && c.Msg.Header.Get(forwardedSiteHeader) != "" {
			c.Next()
			return
		}

		roomID := c.Param("roomID")

		room, err := rooms.GetRoom(c, roomID)
		if err != nil {
			slog.Error("room lookup failed",
				"roomID", roomID,
				"error", err,
			)
			c.ReplyError("internal error")
			c.Abort()
			return
		}
		if room == nil {
			c.ReplyError("room not found")
			c.Abort()
			return
		}

		if room.SiteID == localSiteID {
			c.Next()
			return
		}

		// Forward to the remote site's service via NATS request/reply.
		remoteSubject := strings.Replace(c.Msg.Subject, localSiteID, room.SiteID, 1)

		msg := nats.NewMsg(remoteSubject)
		msg.Data = c.Msg.Data
		msg.Header = make(nats.Header)
		for k, v := range c.Msg.Header {
			msg.Header[k] = v
		}
		msg.Header.Set(forwardedSiteHeader, localSiteID)

		resp, err := nc.RequestMsg(msg, siteProxyTimeout)
		if err != nil {
			slog.Error("remote site request failed",
				"subject", remoteSubject,
				"remoteSiteID", room.SiteID,
				"error", err,
			)
			c.ReplyError("remote site unavailable")
			c.Abort()
			return
		}

		if err := c.Msg.Respond(resp.Data); err != nil {
			slog.Error("relay response failed",
				"subject", c.Msg.Subject,
				"error", err,
			)
		}
		c.Abort()
	}
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
make test SERVICE=history-service/internal/middleware
```

Expected: all 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/middleware/siteproxy.go history-service/internal/middleware/siteproxy_test.go
git commit -m "feat(history): rewrite SiteProxy to use room-based site routing"
```

---

### Task 5: Update RegisterHandlers and main.go Wiring

**Files:**
- Modify: `history-service/internal/service/service.go:40-49`
- Modify: `history-service/cmd/main.go`

- [ ] **Step 1: Update `RegisterHandlers` in `service.go`**

Replace the `RegisterHandlers` method (lines 40-49) in `history-service/internal/service/service.go`:

```go
// RegisterHandlers wires all NATS endpoints for the history service.
// Routes on g go through the SiteProxy middleware (room-based site routing).
// GetMessageByID is registered directly on r (no room lookup needed).
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, g *natsrouter.RouterGroup, siteID string) {
	natsrouter.Register(g, subject.MsgHistoryPattern(siteID), s.LoadHistory)
	natsrouter.Register(g, subject.MsgNextPattern(siteID), s.LoadNextMessages)
	natsrouter.Register(g, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
	natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
}
```

- [ ] **Step 2: Update `main.go` wiring**

Replace `history-service/cmd/main.go` from line 59 (after `mongoRepo := ...`) through line 67 (after `svc.RegisterHandlers(router)`) with:

```go
	roomRepo := mongorepo.NewRoomRepo(mongoClient.Database(cfg.Mongo.DB))
	mongoRepo := mongorepo.NewSubscriptionRepo(mongoClient.Database(cfg.Mongo.DB))
	svc := service.New(cassRepo, mongoRepo)
	router := natsrouter.New(nc, "history-service")
	router.Use(natsrouter.Recovery())
	router.Use(natsrouter.Logging())
	g := router.Group(middleware.SiteProxy(cfg.SiteID, nc.NatsConn(), roomRepo))

	svc.RegisterHandlers(router, g, cfg.SiteID)
```

Note: the `mongoRepo` line moves after `roomRepo` — both use the same `mongoClient.Database(cfg.Mongo.DB)`.

- [ ] **Step 3: Verify compilation**

```bash
make build SERVICE=history-service
```

Expected: compiles successfully.

- [ ] **Step 4: Run all history-service unit tests**

```bash
make test SERVICE=history-service
```

Expected: all tests pass. The service-level tests in `messages_test.go` use `natsrouter.NewContext` directly and don't call `RegisterHandlers`, so the signature change doesn't affect them.

- [ ] **Step 5: Run lint on entire repo**

```bash
make lint
```

Expected: no lint errors.

- [ ] **Step 6: Commit**

```bash
git add history-service/internal/service/service.go history-service/cmd/main.go
git commit -m "feat(history): wire room-based site routing via RouterGroup"
```

- [ ] **Step 7: Push all commits**

```bash
git push -u origin claude/fix-siteid-redirect-dYx7O
```
