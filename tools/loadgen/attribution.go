package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	// cpuIdleFloorCores: a container using less than this over the trip window
	// is treated as idle and never blamed.
	cpuIdleFloorCores = 0.05
)

// promQuerier is the consumer-defined seam over Prometheus, so unit tests can
// inject a fake without a live server.
type promQuerier interface {
	RangeQuery(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]promSeries, error)
}

// bottleneckVerdict is the engine's output, rendered as the BOTTLENECK: block.
type bottleneckVerdict struct {
	Component  string   // culprit component, "" when undetermined
	Resource   string   // "CPU", a dependency display name, or "unknown"
	Confidence string   // "high" | "medium" | "low"
	Reasons    []string // human-readable causal lines
	Determined bool     // false -> render "undetermined (<reason>)"
}

// bottleneckEngine fuses loadgen signals with cAdvisor CPU trends.
type bottleneckEngine struct {
	q     promQuerier
	ident identityResolver
	knee  float64       // max relative CPU rise still counted as a plateau
	step  time.Duration // PromQL query step
}

func newBottleneckEngine(q promQuerier, ident identityResolver, knee float64, step time.Duration) *bottleneckEngine {
	return &bottleneckEngine{q: q, ident: ident, knee: knee, step: step}
}

// cpuCores returns mean cores used by service over [start,end], derived from
// the CPU usage counter. reset=true when the counter dropped (container
// restart) — callers treat that as a memory/restart signal, not a CPU rate.
func (e *bottleneckEngine) cpuCores(ctx context.Context, service string, start, end time.Time) (cores float64, reset bool, ok bool) {
	query := fmt.Sprintf(`container_cpu_usage_seconds_total{%s}`, e.ident.selector(service))
	series, err := e.q.RangeQuery(ctx, query, start, end, e.step)
	if err != nil {
		slog.Warn("cpu query failed", "service", service, "error", err)
		return 0, false, false
	}
	// Sum across any matching cgroup series (cAdvisor may emit several).
	var first, last float64
	var t0, t1 time.Time
	var have bool
	for _, s := range series {
		if len(s.Samples) < 2 {
			continue
		}
		first += s.Samples[0].V
		last += s.Samples[len(s.Samples)-1].V
		t0 = s.Samples[0].T
		t1 = s.Samples[len(s.Samples)-1].T
		have = true
	}
	if !have || !t1.After(t0) {
		return 0, false, false
	}
	if last < first {
		return 0, true, true
	}
	return (last - first) / t1.Sub(t0).Seconds(), false, true
}
