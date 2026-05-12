//go:build e2e

// HTTP load tests against auth-service. Companion to auth_http_test.go's
// correctness coverage -- this one exercises CONCURRENCY behaviours that
// single-request tests can't surface:
//
//   - Keycloak token endpoint backpressure under sustained load.
//   - auth-service's nkey + JWT minting throughput.
//   - HTTP connection pool / file-descriptor exhaustion.
//
// Gated on -short being unset (`go test -short` skips). Skipped under
// E2E_REUSE_STACK because Keycloak gets hammered and parallel tests
// compete on token-endpoint capacity.

package e2e

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAuthLoad_ConcurrentAuthHandshakes: launch N goroutines, each
// performing the full Keycloak password-grant + auth-service /auth +
// NATS connect chain. Asserts (a) no failures, (b) median latency
// stays under a threshold, (c) p99 stays under a bigger threshold.
//
// Uses AuthenticateE (error-returning) so failures are counted via
// failures.Add(1) rather than (incorrectly) caught via recover() --
// require.NoError calls runtime.Goexit, not panic, and recover() can't
// catch Goexit. The original recover() guard was a no-op; this version
// genuinely surfaces handshake errors.
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

	// Thresholds calibrated to Keycloak's bcrypt-per-grant cost (~50-100ms
	// CPU-bound, single-threaded per Wildfly worker; default ~16 task
	// threads). At concurrency=32 with N=200, expected median ~160ms and
	// p99 ~500ms. Catch 5x regression: median <800ms, p99 <3s.
	require.Less(t, median, 800*time.Millisecond,
		"median handshake latency must stay under 800ms under load")
	require.Less(t, p99, 3*time.Second,
		"p99 handshake latency must stay under 3s under load")
}

// percentile returns the p-th percentile of latencies. p is in [0, 100].
// Uses nearest-rank with 1-indexed semantics: percentile(s, p) returns
// the ceil(p/100 * N)-th smallest element (1-indexed) for p in (0, 100].
// percentile(s, 50) is the median; percentile(s, 99) of 200 samples is
// the 198th element, NOT the 199th (which was the prior buggy result =
// p99.5 rather than p99).
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
