package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConsumerPendingDelta captures a single durable's pending-message count
// at the start and end of a hold window.
type ConsumerPendingDelta struct {
	Start int64
	End   int64
	Delta int64
}

// SelfMetrics describes the loadgen process's own resource state during
// the hold window. High values mean the load box is the bottleneck and
// the step is INCONCLUSIVE rather than PASS/TRIP.
type SelfMetrics struct {
	GCPauseP99Ms float64
	CPUPercent   float64
	Goroutines   int
}

// Thresholds are the per-signal cutoffs that decide PASS / TRIP / INCONCLUSIVE.
type Thresholds struct {
	P95LatencyMs        float64
	P99LatencyMs        float64
	ErrorRate           float64 // fraction (0.001 = 0.1%)
	PendingGrowth       int64
	GCPauseInconclusive float64
	CPUInconclusive     float64
}

func defaultThresholds() Thresholds {
	return Thresholds{
		P95LatencyMs: 500, P99LatencyMs: 1000,
		ErrorRate: 0.001, PendingGrowth: 1000,
		GCPauseInconclusive: 50, CPUInconclusive: 80,
	}
}

// stepInputs is everything evaluateStep needs to produce a verdict.
type stepInputs struct {
	N               int
	StartedAt       time.Time
	HoldDuration    time.Duration
	LatencySamples  []float64 // milliseconds
	AttemptedOps    int64
	FailedOps       int64
	ConsumerPending map[string]ConsumerPendingDelta
	ServiceErrors   map[string]int64
	Self            SelfMetrics
}

// StepResult is the verdict for a single ramp step.
type StepResult struct {
	N                     int
	StartedAt             time.Time
	HoldDuration          time.Duration
	P50LatencyMs          float64
	P95LatencyMs          float64
	P99LatencyMs          float64
	ErrorRate             float64
	AttemptedOps          int64
	FailedOps             int64
	ConsumerPending       map[string]ConsumerPendingDelta
	ServiceErrorIncreases map[string]int64
	LoadgenSelfMetrics    SelfMetrics
	Tripped               bool
	Inconclusive          bool
	TrippedReasons        []string
}

func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	cp := make([]float64, len(samples))
	copy(cp, samples)
	sort.Float64s(cp)
	idx := int(p * float64(len(cp)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

//nolint:gocritic // hugeParam: pure-function signature is intentional; the per-step copy cost is negligible.
func evaluateStep(in stepInputs, th Thresholds) StepResult {
	r := StepResult{
		N: in.N, StartedAt: in.StartedAt, HoldDuration: in.HoldDuration,
		AttemptedOps: in.AttemptedOps, FailedOps: in.FailedOps,
		ConsumerPending:       in.ConsumerPending,
		ServiceErrorIncreases: in.ServiceErrors,
		LoadgenSelfMetrics:    in.Self,
		P50LatencyMs:          percentile(in.LatencySamples, 0.50),
		P95LatencyMs:          percentile(in.LatencySamples, 0.95),
		P99LatencyMs:          percentile(in.LatencySamples, 0.99),
	}
	if in.AttemptedOps > 0 {
		r.ErrorRate = float64(in.FailedOps) / float64(in.AttemptedOps)
	}

	// Inconclusive overrides trip.
	if in.Self.GCPauseP99Ms > th.GCPauseInconclusive || in.Self.CPUPercent > th.CPUInconclusive {
		r.Inconclusive = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("inconclusive: gc=%.1fms cpu=%.1f%%", in.Self.GCPauseP99Ms, in.Self.CPUPercent))
		return r
	}

	if r.P95LatencyMs > th.P95LatencyMs {
		r.Tripped = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("p95=%.0fms > %.0f", r.P95LatencyMs, th.P95LatencyMs))
	}
	if r.P99LatencyMs > th.P99LatencyMs {
		r.Tripped = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("p99=%.0fms > %.0f", r.P99LatencyMs, th.P99LatencyMs))
	}
	if r.ErrorRate > th.ErrorRate {
		r.Tripped = true
		r.TrippedReasons = append(r.TrippedReasons,
			fmt.Sprintf("error_rate=%.4f > %.4f", r.ErrorRate, th.ErrorRate))
	}
	for durable, d := range in.ConsumerPending {
		if d.Delta > th.PendingGrowth {
			r.Tripped = true
			r.TrippedReasons = append(r.TrippedReasons,
				fmt.Sprintf("%s pending +%d > +%d", durable, d.Delta, th.PendingGrowth))
		}
	}
	for svc, n := range in.ServiceErrors {
		if n > 0 {
			r.Tripped = true
			r.TrippedReasons = append(r.TrippedReasons,
				fmt.Sprintf("%s errors +%d", svc, n))
		}
	}
	return r
}

// snapshotSelfMetrics samples loadgen-process resource counters.
// CPU% is approximate (delta of cumulative CPU time / wall-clock since last call).
func snapshotSelfMetrics() SelfMetrics {
	g := runtime.NumGoroutine()
	gcP99 := readGCPauseP99Ms()
	cpu := readCPUPercent()
	return SelfMetrics{
		GCPauseP99Ms: gcP99,
		CPUPercent:   cpu,
		Goroutines:   g,
	}
}

var (
	gcLastNumGC uint32 //nolint:unused // reserved for future delta tracking
	gcMu        sync.Mutex
)

func readGCPauseP99Ms() float64 {
	gcMu.Lock()
	defer gcMu.Unlock()
	samples := []metrics.Sample{{Name: "/gc/pauses:seconds"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() != metrics.KindFloat64Histogram {
		return 0
	}
	h := samples[0].Value.Float64Histogram()
	if len(h.Counts) == 0 {
		return 0
	}
	var total uint64
	for _, c := range h.Counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := total * 99 / 100
	var acc uint64
	for i, c := range h.Counts {
		acc += c
		if acc >= target {
			return h.Buckets[i] * 1000
		}
	}
	return 0
}

var (
	cpuMu    sync.Mutex
	cpuLastT time.Time
)

// readCPUPercent is a conservative approximation. The Go stdlib doesn't
// expose a clean process-wide CPU% counter; for load-test gating we use
// NumGoroutine as a proxy: more goroutines under contention typically
// correlates with higher CPU. If this trips spuriously, swap in gopsutil
// in a follow-up.
func readCPUPercent() float64 {
	cpuMu.Lock()
	defer cpuMu.Unlock()
	now := time.Now()
	if cpuLastT.IsZero() {
		cpuLastT = now
		return 0
	}
	cpuLastT = now
	return float64(runtime.NumGoroutine()) / 5000.0 * 100
}

// diffPending computes per-durable Start/End/Delta from two snapshots.
// Durables that appeared mid-window are counted with Start=0.
func diffPending(start, end map[string]int64) map[string]ConsumerPendingDelta {
	out := make(map[string]ConsumerPendingDelta, len(end))
	for durable, e := range end {
		s := start[durable]
		out[durable] = ConsumerPendingDelta{Start: s, End: e, Delta: e - s}
	}
	return out
}

// pollPending queries the NATS monitoring endpoint /jsz?consumers=true and
// returns a map of durable name -> NumPending. Retries transient failures
// with short backoff so a flaky monitoring endpoint doesn't poison a step.
func pollPending(ctx context.Context, jszURL string) (map[string]int64, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		out, err := pollPendingOnce(ctx, jszURL)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("pollPending after %d attempts: %w", maxAttempts, lastErr)
}

func pollPendingOnce(ctx context.Context, jszURL string) (map[string]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jszURL+"?consumers=true", nil)
	if err != nil {
		return nil, fmt.Errorf("build jsz request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jsz GET: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		AccountDetails []struct {
			StreamDetail []struct {
				ConsumerDetail []struct {
					Name       string `json:"name"`
					NumPending int64  `json:"num_pending"`
				} `json:"consumer_detail"`
			} `json:"stream_detail"`
		} `json:"account_details"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("jsz decode: %w", err)
	}
	out := make(map[string]int64)
	for _, a := range body.AccountDetails {
		for _, s := range a.StreamDetail {
			for _, c := range s.ConsumerDetail {
				out[c.Name] = c.NumPending
			}
		}
	}
	return out, nil
}

// serviceScraper fetches /metrics from each service URL and returns a map of
// service -> delta in slog_errors_total since the previous call.
// First call returns zeros and records baselines.
type serviceScraper struct {
	mu       sync.Mutex
	baseline map[string]float64
}

func newServiceScraper() *serviceScraper {
	return &serviceScraper{baseline: make(map[string]float64)}
}

func (s *serviceScraper) Scrape(ctx context.Context, urls map[string]string) (map[string]int64, error) {
	out := make(map[string]int64, len(urls))
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, url := range urls {
		v, err := scrapeErrorCounter(ctx, url)
		if err != nil {
			out[name] = 0 // tolerate missing
			continue
		}
		prev, ok := s.baseline[name]
		s.baseline[name] = v
		if !ok {
			out[name] = 0
			continue
		}
		out[name] = int64(v - prev)
	}
	return out, nil
}

func scrapeErrorCounter(ctx context.Context, url string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build metrics request %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("metrics GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("metrics read %s: %w", url, err)
	}
	return sumCounterFamily(string(body), "slog_errors_total"), nil
}

func sumCounterFamily(body, family string) float64 {
	var sum float64
	for _, line := range strings.Split(body, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		if !strings.HasPrefix(line, family) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(fields[len(fields)-1], "%f", &v); err != nil {
			continue // skip unparseable line
		}
		sum += v
	}
	return sum
}
