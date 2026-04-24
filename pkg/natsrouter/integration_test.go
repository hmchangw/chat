//go:build integration

package natsrouter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/natsrouter"
)

// setupNATS starts a real NATS container and returns a connected otelnats
// client. Required to surface timing races that in-process NATS cannot
// reproduce (real TCP, real server dispatch goroutines, real latency).
func setupNATS(t *testing.T) *otelnats.Conn {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2-alpine",
			ExposedPorts: []string{"4222/tcp"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err, "start NATS container")
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "4222")
	require.NoError(t, err)
	url := fmt.Sprintf("nats://%s:%s", host, port.Port())

	nc, err := otelnats.Connect(url)
	require.NoError(t, err, "connect to NATS")
	t.Cleanup(nc.Close)

	return nc
}

type echoReq struct {
	Name string `json:"name"`
	Seq  int    `json:"seq"`
}

type echoResp struct {
	Greeting string `json:"greeting"`
	Seq      int    `json:"seq"`
	ReqID    string `json:"reqId"`
}

// TestIntegration_ConcurrentRequestsWithCopy exercises the full hot path
// against a real NATS server under heavy concurrency: context pool reuse,
// middleware keys, and Copy() handed to an async goroutine that outlives
// the handler. With -race, this must stay clean.
func TestIntegration_ConcurrentRequestsWithCopy(t *testing.T) {
	nc := setupNATS(t)
	r := natsrouter.New(nc, "integration-concurrent")
	r.Use(natsrouter.RequestID())
	r.Use(natsrouter.Recovery())
	r.Use(natsrouter.Logging())

	// Async goroutines use Copy() — we count them to prove they all ran.
	var asyncCompleted atomic.Int64
	var asyncStarted sync.WaitGroup

	natsrouter.Register(r, "chat.user.{account}.echo.{room}",
		func(c *natsrouter.Context, req echoReq) (*echoResp, error) {
			c.Set("account", c.Param("account"))
			c.Set("room", c.Param("room"))

			reqID := c.MustGet("requestID").(string)

			// Hand a Copy() to a goroutine that definitely outlives the handler.
			cp := c.Copy()
			asyncStarted.Add(1)
			go func() {
				defer asyncStarted.Done()
				time.Sleep(5 * time.Millisecond)
				// Read everything a caller might read post-return.
				if cp.Param("account") == "" || cp.Param("room") == "" {
					t.Errorf("copy lost params")
				}
				if got := cp.MustGet("account"); got != cp.Param("account") {
					t.Errorf("copy keys mismatch: %v", got)
				}
				// Write on the copy to prove the mutex path is exercised.
				cp.Set("async-done", true)
				_, _ = cp.Get("async-done")
				asyncCompleted.Add(1)
			}()

			return &echoResp{
				Greeting: "hi " + c.Param("account"),
				Seq:      req.Seq,
				ReqID:    reqID,
			}, nil
		})

	const n = 300
	var clients sync.WaitGroup
	clients.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer clients.Done()
			data, _ := json.Marshal(echoReq{Name: "load", Seq: i})
			subj := fmt.Sprintf("chat.user.u%d.echo.r%d", i%10, i%5)
			resp, err := nc.Request(context.Background(), subj, data, 5*time.Second)
			if err != nil {
				errCh <- fmt.Errorf("seq %d: %w", i, err)
				return
			}
			var r echoResp
			if err := json.Unmarshal(resp.Data, &r); err != nil {
				errCh <- fmt.Errorf("seq %d unmarshal: %w", i, err)
				return
			}
			if r.Seq != i {
				errCh <- fmt.Errorf("seq %d got seq %d", i, r.Seq)
			}
			if r.ReqID == "" {
				errCh <- fmt.Errorf("seq %d got empty reqID", i)
			}
		}(i)
	}
	clients.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	asyncStarted.Wait()
	assert.Equal(t, int64(n), asyncCompleted.Load(), "every async goroutine must complete")
}

// TestIntegration_ShutdownUnderLoad regression-guards the "Add at zero after
// Wait" race we fixed in Shutdown. Fires requests continuously, calls
// Shutdown mid-flight, and re-runs the cycle many times to catch any
// timing-sensitive leak. Must stay clean under -race.
func TestIntegration_ShutdownUnderLoad(t *testing.T) {
	const cycles = 5
	for cycle := 0; cycle < cycles; cycle++ {
		t.Run(fmt.Sprintf("cycle-%d", cycle), func(t *testing.T) {
			nc := setupNATS(t)
			r := natsrouter.New(nc, "integration-shutdown")

			var completed atomic.Int64
			natsrouter.Register(r, "load.{id}",
				func(c *natsrouter.Context, req echoReq) (*echoResp, error) {
					time.Sleep(time.Duration(1+req.Seq%7) * time.Millisecond)
					completed.Add(1)
					return &echoResp{Seq: req.Seq}, nil
				})

			const inflight = 150
			var clientsWG sync.WaitGroup
			clientsWG.Add(inflight)
			for i := 0; i < inflight; i++ {
				go func(i int) {
					defer clientsWG.Done()
					data, _ := json.Marshal(echoReq{Seq: i})
					_, _ = nc.Request(context.Background(), fmt.Sprintf("load.%d", i), data, 5*time.Second)
				}(i)
			}

			// Let some requests start before calling Shutdown.
			time.Sleep(3 * time.Millisecond)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, r.Shutdown(ctx))

			clientsWG.Wait()
			t.Logf("cycle %d completed %d/%d handlers", cycle, completed.Load(), inflight)
			assert.Greater(t, completed.Load(), int64(0), "at least some handlers must run")
		})
	}
}

// TestIntegration_MultipleRouterInstances simulates multiple service pods
// sharing a queue group. Ensures:
//   - requests load-balance across instances (queue group semantics),
//   - shutting down one instance leaves the others serving,
//   - Shutdown on one instance does not disturb the others.
func TestIntegration_MultipleRouterInstances(t *testing.T) {
	nc := setupNATS(t)

	const queue = "integration-queue-group"
	const instances = 3

	routers := make([]*natsrouter.Router, instances)
	hits := make([]atomic.Int64, instances)
	for idx := 0; idx < instances; idx++ {
		idx := idx
		r := natsrouter.New(nc, queue)
		natsrouter.Register(r, "qg.work.{id}",
			func(c *natsrouter.Context, req echoReq) (*echoResp, error) {
				hits[idx].Add(1)
				return &echoResp{Seq: req.Seq}, nil
			})
		routers[idx] = r
	}

	// Warm up: fire enough requests that each instance should get some work.
	const warmup = 300
	for i := 0; i < warmup; i++ {
		data, _ := json.Marshal(echoReq{Seq: i})
		_, err := nc.Request(context.Background(), fmt.Sprintf("qg.work.%d", i), data, 5*time.Second)
		require.NoError(t, err)
	}
	for idx := 0; idx < instances; idx++ {
		assert.Greater(t, hits[idx].Load(), int64(0),
			"queue group must distribute work to instance %d", idx)
	}
	totalAfterWarmup := int64(0)
	for idx := 0; idx < instances; idx++ {
		totalAfterWarmup += hits[idx].Load()
	}
	require.Equal(t, int64(warmup), totalAfterWarmup, "every request must be answered exactly once")

	// Shutdown the first instance; the others must continue serving cleanly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, routers[0].Shutdown(shutdownCtx))

	const postShutdown = 100
	for i := 0; i < postShutdown; i++ {
		data, _ := json.Marshal(echoReq{Seq: i})
		_, err := nc.Request(context.Background(), fmt.Sprintf("qg.work.%d", warmup+i), data, 5*time.Second)
		require.NoError(t, err, "remaining instances must keep serving after one shuts down")
	}

	// The shutdown instance must not gain new hits.
	hitsAt0AfterShutdown := hits[0].Load()
	time.Sleep(50 * time.Millisecond) // settle
	assert.Equal(t, hitsAt0AfterShutdown, hits[0].Load(),
		"shutdown instance must not receive any more traffic")

	// Clean up the rest.
	for idx := 1; idx < instances; idx++ {
		require.NoError(t, routers[idx].Shutdown(shutdownCtx))
	}
}
