package natsrouter

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContext_Copy_DeepCopiesKeys proves that mutating the original context's
// keys after Copy() does not leak into the copy. Without a deep copy, both
// contexts would share the same map and this test would fail.
func TestContext_Copy_DeepCopiesKeys(t *testing.T) {
	c := NewContext(nil)
	c.Set("user", "alice")
	c.Set("count", 42)

	cp := c.Copy()

	c.Set("user", "bob")
	c.Set("count", 99)
	c.Set("added-after-copy", true)

	assert.Equal(t, "alice", cp.MustGet("user"), "copy must retain original value")
	assert.Equal(t, 42, cp.MustGet("count"), "copy must retain original value")

	_, ok := cp.Get("added-after-copy")
	assert.False(t, ok, "keys added to original after Copy must not appear in copy")
}

// TestContext_Copy_DeepCopiesParams proves Params are deep-copied so mutating
// the original's params map does not affect the copy.
func TestContext_Copy_DeepCopiesParams(t *testing.T) {
	c := NewContext(map[string]string{"account": "alice", "roomID": "r1"})

	cp := c.Copy()

	// Mutate the original's underlying map (simulating pool-reuse writing
	// into a shared map).
	c.Params.values["account"] = "mallory"

	assert.Equal(t, "alice", cp.Param("account"), "copy's params must not share underlying map")
	assert.Equal(t, "r1", cp.Param("roomID"))
}

// TestContext_Copy_IsDetachedFromChain proves the copy will not run any
// middleware or handlers — its handler chain is detached.
func TestContext_Copy_IsDetachedFromChain(t *testing.T) {
	c := NewContext(nil)
	c.handlers = []HandlerFunc{
		func(c *Context) { t.Fatal("original handler should not run via copy") },
	}

	cp := c.Copy()

	assert.True(t, cp.IsAborted(), "copy must report as aborted so middleware won't run")

	// Calling Next on the copy must be a no-op.
	cp.Next()
}

// TestContext_Copy_NilKeys verifies Copy of a context that never had Set() called.
func TestContext_Copy_NilKeys(t *testing.T) {
	c := NewContext(map[string]string{"id": "1"})
	cp := c.Copy()

	_, ok := cp.Get("anything")
	assert.False(t, ok)

	// Setting on the copy must work (lazy init on the copy's own map).
	cp.Set("k", "v")
	assert.Equal(t, "v", cp.MustGet("k"))

	// ...and must not leak back into the original.
	_, ok = c.Get("k")
	assert.False(t, ok)
}

// TestContext_ConcurrentKeysAccess_NoRace proves that Set and Get are safe to
// call concurrently. Without a mutex, Go's map detector panics on concurrent
// writes and the race detector flags concurrent read/write.
func TestContext_ConcurrentKeysAccess_NoRace(t *testing.T) {
	c := NewContext(nil)
	c.Set("seed", 0)

	const n = 500
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			c.Set("a"+strconv.Itoa(i), i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			c.Set("b"+strconv.Itoa(i), i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, _ = c.Get("a" + strconv.Itoa(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, _ = c.Get("b" + strconv.Itoa(i))
		}
	}()
	wg.Wait()
}

// TestContext_Copy_SafeAcrossPoolReuse is the critical race test.
//
// It simulates the production scenario:
//
//  1. Request #1 acquires a context from the pool, populates it, creates a
//     Copy, then is released.
//  2. A background goroutine continues reading from the copy (e.g., async
//     metric emission, fire-and-forget logging).
//  3. Concurrently, request #2 acquires the same pooled struct and writes
//     to it.
//
// With the race detector enabled, this test fails unless Copy() produces a
// fully-detached context (independent keys map, independent params map)
// AND the keys map is mutex-protected.
func TestContext_Copy_SafeAcrossPoolReuse(t *testing.T) {
	c1 := acquireContext(
		context.Background(),
		nil,
		NewParams(map[string]string{"req": "1"}),
		nil,
	)
	c1.Set("id", "alice")
	c1.Set("role", "admin")

	cp := c1.Copy()

	releaseContext(c1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			require.Equal(t, "alice", cp.MustGet("id"))
			require.Equal(t, "admin", cp.MustGet("role"))
			require.Equal(t, "1", cp.Param("req"))
		}
	}()

	// Concurrently simulate many other requests reusing the pooled struct.
	for i := 0; i < 200; i++ {
		c2 := acquireContext(
			context.Background(),
			nil,
			NewParams(map[string]string{"req": strconv.Itoa(i + 2)}),
			nil,
		)
		c2.Set("id", "bob")
		c2.Set("role", "guest")
		releaseContext(c2)
	}
	<-done
}
