//go:build e2e

// HTTP load tests against auth-service. Skipped under -short and
// E2E_REUSE_STACK (saturates Keycloak / competes with parallel tests).

package e2e

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Launches N goroutines doing the full Keycloak + auth-service + NATS
// handshake. Asserts no failures, bounded median, bounded p99.
func TestAuthLoad_ConcurrentAuthHandshakes(t *testing.T) {
	if testing.Short() {
		t.Skip("skip auth-load test under -short")
	}
	skipUnderReuse(t, "Auth-load test saturates Keycloak; would slow parallel tests")

	ctx := t.Context()
	site := stack.SiteA

	// Pre-mint one ephemeral user; we reuse it across all goroutines.
	uname := site.MintEphemeralUser(t, ctx)

	// Warm up Keycloak's token cache: one handshake serially so the
	// realm + client + user are JIT-loaded before the burst.
	if _, err := site.AuthenticateE(t, ctx, uname); err != nil {
		t.Fatalf("warmup handshake: %v", err)
	}

	const N = 200
	const concurrency = 32
	results := make(chan time.Duration, N)
	failures := atomic.Int64{}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	startBurst := time.Now()
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			t0 := time.Now()
			_, err := site.AuthenticateE(t, ctx, uname)
			if err != nil {
				failures.Add(1)
				t.Logf("handshake failed: %v", err)
				return
			}
			results <- time.Since(t0)
		}()
	}
	wg.Wait()
	close(results)
	totalElapsed := time.Since(startBurst)

	latencies := make([]time.Duration, 0, N)
	for d := range results {
		latencies = append(latencies, d)
	}
	require.Equal(t, int64(0), failures.Load(),
		"all %d handshakes must succeed; failures=%d", N, failures.Load())
	require.Len(t, latencies, N, "all goroutines must report a latency")

	median := percentile(latencies, 50)
	p99 := percentile(latencies, 99)
	t.Logf("auth-load N=%d concurrency=%d total=%s median=%s p99=%s",
		N, concurrency, totalElapsed, median, p99)

	// Calibrated to Keycloak bcrypt cost; thresholds catch a 5x regression.
	require.Less(t, median, 800*time.Millisecond,
		"median handshake latency must stay under 800ms under load")
	require.Less(t, p99, 3*time.Second,
		"p99 handshake latency must stay under 3s under load")
}

// percentile returns the p-th percentile of latencies (p in [0, 100]),
// using nearest-rank with 1-indexed semantics.
func percentile(latencies []time.Duration, p int) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if p <= 0 {
		return latencies[0]
	}
	if p >= 100 {
		return latencies[len(latencies)-1]
	}
	// Ceil((p/100) * N) - 1 for 0-indexed access. Equivalent to
	// (p*N + 99) / 100 - 1 in integer math.
	idx := (p*len(latencies)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	return latencies[idx]
}
