# NATS Graceful Shutdown for Kubernetes Rolling Updates

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add timeout-aware graceful shutdown to all services so Kubernetes rolling updates complete cleanly without message loss or hung pods.

**Architecture:** Update `pkg/shutdown.Wait` to accept a timeout duration, enforce a deadline on cleanup, and log timeout warnings. Update all JetStream consumer services to use `cctx.Drain()` (processes buffered messages) instead of `cctx.Stop()` (discards buffered messages). Use `cctx.Closed()` channel to confirm consumer is fully drained before proceeding to `nc.Drain()`.

**Tech Stack:** Go 1.24, NATS v1.41.1, nats.go/jetstream `ConsumeContext` interface

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `pkg/shutdown/shutdown.go` | Modify | Add `time.Duration` param, deadline context, goroutine + select pattern |
| `pkg/shutdown/shutdown_test.go` | Modify | Test timeout behavior and normal completion |
| `broadcast-worker/main.go` | Modify | `cctx.Drain()` + `cctx.Closed()` + pass timeout |
| `message-worker/main.go` | Modify | `cctx.Drain()` + `cctx.Closed()` + pass timeout |
| `notification-worker/main.go` | Modify | `cctx.Drain()` + `cctx.Closed()` + pass timeout |
| `inbox-worker/main.go` | Modify | `cctx.Drain()` + `cctx.Closed()` + pass timeout |
| `room-worker/main.go` | Modify | `cctx.Drain()` + `cctx.Closed()` + pass timeout |
| `room-service/main.go` | Modify | Pass timeout |
| `history-service/main.go` | Modify | Pass timeout |
| `auth-service/main.go` | Modify | Pass timeout |

---

### Task 1: Update `pkg/shutdown` with Timeout Support

**Files:**
- Modify: `pkg/shutdown/shutdown.go`
- Modify: `pkg/shutdown/shutdown_test.go`

- [ ] **Step 1: Write test for timeout behavior**

Add to `pkg/shutdown/shutdown_test.go`:

```go
func TestWaitTimesOutWhenCleanupHangs(t *testing.T) {
	hangingCleanup := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	start := time.Now()
	shutdown.Wait(context.Background(), 500*time.Millisecond, hangingCleanup)
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("expected shutdown to complete within ~500ms timeout, took %v", elapsed)
	}
}
```

- [ ] **Step 2: Write test for normal completion within timeout**

Add to `pkg/shutdown/shutdown_test.go`:

```go
func TestWaitCompletesBeforeTimeout(t *testing.T) {
	var order []string
	first := func(ctx context.Context) error {
		order = append(order, "first")
		return nil
	}
	second := func(ctx context.Context) error {
		order = append(order, "second")
		return nil
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	shutdown.Wait(context.Background(), 5*time.Second, first, second)

	assert.Equal(t, []string{"first", "second"}, order)
}
```

Note: Add `"github.com/stretchr/testify/assert"` to the import block.

- [ ] **Step 3: Update existing test to pass timeout parameter**

The existing `TestWaitCallsCleanup` test needs the new `time.Duration` argument:

```go
func TestWaitCallsCleanup(t *testing.T) {
	called := false
	cleanup := func(ctx context.Context) error {
		called = true
		return nil
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	shutdown.Wait(context.Background(), 5*time.Second, cleanup)

	if !called {
		t.Error("cleanup function was not called")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `make test SERVICE=pkg/shutdown`

Expected: Compilation error — `shutdown.Wait` doesn't accept `time.Duration` yet.

- [ ] **Step 5: Implement timeout-aware `shutdown.Wait`**

Replace the entire `pkg/shutdown/shutdown.go`:

```go
package shutdown

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Wait blocks until SIGINT or SIGTERM, then calls each shutdown function sequentially.
// If cleanup does not complete within the given timeout, Wait returns and logs a warning.
// The timeout context is passed to each shutdown function so they can respect the deadline.
func Wait(ctx context.Context, timeout time.Duration, shutdownFuncs ...func(context.Context) error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("shutting down...")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				slog.Error("shutdown error", "error", err)
			}
		}
	}()

	select {
	case <-done:
		slog.Info("shutdown complete")
	case <-ctx.Done():
		slog.Warn("shutdown timed out, forcing exit")
	}
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `make test SERVICE=pkg/shutdown`

Expected: All 3 tests PASS.

- [ ] **Step 7: Run lint**

Run: `make lint`

Expected: No new lint issues.

- [ ] **Step 8: Commit**

```bash
git add pkg/shutdown/shutdown.go pkg/shutdown/shutdown_test.go
git commit -m "feat(shutdown): add timeout support for Kubernetes graceful shutdown"
```

---

### Task 2: Update JetStream Consumer Workers — `cctx.Drain()` + `cctx.Closed()`

**Files:**
- Modify: `broadcast-worker/main.go:132-136`
- Modify: `message-worker/main.go:94-99`
- Modify: `notification-worker/main.go:118-122`
- Modify: `inbox-worker/main.go:121-125`
- Modify: `room-worker/main.go:81-85`

All five services follow the same pattern. For each, change the shutdown block from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { cctx.Stop(); return nil },
	func(ctx context.Context) error { return nc.Drain() },
	// ... database cleanups
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error {
		cctx.Drain()
		select {
		case <-cctx.Closed():
			return nil
		case <-ctx.Done():
			return fmt.Errorf("consumer drain timed out: %w", ctx.Err())
		}
	},
	func(ctx context.Context) error { return nc.Drain() },
	// ... database cleanups unchanged
)
```

Each service also needs `"time"` in its imports (verify it's already present; add if missing).

- [ ] **Step 1: Update `broadcast-worker/main.go`**

Change lines 132-136 from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { cctx.Stop(); return nil },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error {
		cctx.Drain()
		select {
		case <-cctx.Closed():
			return nil
		case <-ctx.Done():
			return fmt.Errorf("consumer drain timed out: %w", ctx.Err())
		}
	},
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

Ensure `"time"` and `"fmt"` are in the imports.

- [ ] **Step 2: Update `message-worker/main.go`**

Change lines 94-99 from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { cctx.Stop(); return nil },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error {
		cctx.Drain()
		select {
		case <-cctx.Closed():
			return nil
		case <-ctx.Done():
			return fmt.Errorf("consumer drain timed out: %w", ctx.Err())
		}
	},
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
)
```

Ensure `"time"` and `"fmt"` are in the imports.

- [ ] **Step 3: Update `notification-worker/main.go`**

Change lines 118-122 from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { cctx.Stop(); return nil },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error {
		cctx.Drain()
		select {
		case <-cctx.Closed():
			return nil
		case <-ctx.Done():
			return fmt.Errorf("consumer drain timed out: %w", ctx.Err())
		}
	},
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

Ensure `"time"` and `"fmt"` are in the imports.

- [ ] **Step 4: Update `inbox-worker/main.go`**

Change lines 121-125 from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { cctx.Stop(); return nil },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error {
		cctx.Drain()
		select {
		case <-cctx.Closed():
			return nil
		case <-ctx.Done():
			return fmt.Errorf("consumer drain timed out: %w", ctx.Err())
		}
	},
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

Ensure `"time"` and `"fmt"` are in the imports.

- [ ] **Step 5: Update `room-worker/main.go`**

Change lines 81-85 from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { cctx.Stop(); return nil },
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error {
		cctx.Drain()
		select {
		case <-cctx.Closed():
			return nil
		case <-ctx.Done():
			return fmt.Errorf("consumer drain timed out: %w", ctx.Err())
		}
	},
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

Ensure `"time"` and `"fmt"` are in the imports.

- [ ] **Step 6: Verify compilation**

Run: `make lint`

Expected: All services compile and pass lint.

- [ ] **Step 7: Commit**

```bash
git add broadcast-worker/main.go message-worker/main.go notification-worker/main.go inbox-worker/main.go room-worker/main.go
git commit -m "feat(workers): use cctx.Drain with Closed channel for graceful JetStream shutdown"
```

---

### Task 3: Update Non-Consumer Services — Pass Timeout

**Files:**
- Modify: `room-service/main.go:82-85`
- Modify: `history-service/main.go:67-71`
- Modify: `auth-service/main.go:69-75`

- [ ] **Step 1: Update `room-service/main.go`**

Change lines 82-85 from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
)
```

Ensure `"time"` is in the imports.

- [ ] **Step 2: Update `history-service/main.go`**

Change lines 67-71 from:

```go
shutdown.Wait(ctx,
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
)
```

To:

```go
shutdown.Wait(ctx, 25*time.Second,
	func(ctx context.Context) error { return nc.Drain() },
	func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
)
```

Ensure `"time"` is in the imports.

- [ ] **Step 3: Update `auth-service/main.go`**

Change lines 69-75 from:

```go
shutdown.Wait(context.Background(), func(ctx context.Context) error {
```

To:

```go
shutdown.Wait(context.Background(), 25*time.Second, func(ctx context.Context) error {
```

Ensure `"time"` is in the imports.

- [ ] **Step 4: Verify compilation and lint**

Run: `make lint`

Expected: All services compile and pass lint.

- [ ] **Step 5: Run all unit tests**

Run: `make test`

Expected: All tests PASS.

- [ ] **Step 6: Commit**

```bash
git add room-service/main.go history-service/main.go auth-service/main.go
git commit -m "feat(services): pass shutdown timeout for Kubernetes graceful termination"
```

---

## Why These Changes Matter

| Problem | Fix | Impact |
|---------|-----|--------|
| `cctx.Stop()` discards buffered messages | `cctx.Drain()` processes all buffered messages before stopping | No message loss during shutdown |
| No shutdown timeout — pod hangs if handler blocks | `context.WithTimeout` + `select` on done/timeout | Pod exits cleanly within K8s grace period |
| No confirmation consumer is fully drained | `cctx.Closed()` channel blocks until drain completes | `nc.Drain()` only runs after consumer is fully stopped |
| `shutdown.Wait` blocks indefinitely | Goroutine + select pattern with timeout | Guarantees exit within 25s (5s buffer before K8s SIGKILL at 30s) |

## Kubernetes Configuration Note

Ensure your Kubernetes deployment has `terminationGracePeriodSeconds: 30` (the default). The 25-second timeout in code leaves a 5-second buffer for SIGKILL. If you need longer handler processing, increase both values proportionally.
