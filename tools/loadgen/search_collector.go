package main

import (
	"sync"
	"time"
)

// searchSample captures one completed search request's per-call signal, keyed
// by benchmark arm.
type searchSample struct {
	Latency time.Duration
	At      time.Time
}

// SearchCollector aggregates per-arm latency samples and error tallies for the
// search workload, and feeds the Prometheus histogram the Grafana panel reads.
// All public methods are safe for concurrent use.
type SearchCollector struct {
	mu      sync.Mutex
	metrics *Metrics
	preset  string
	samples map[string][]searchSample
	errors  map[string]int
}

// NewSearchCollector returns an empty collector bound to the given metrics
// registry and preset label.
func NewSearchCollector(m *Metrics, preset string) *SearchCollector {
	return &SearchCollector{
		metrics: m,
		preset:  preset,
		samples: map[string][]searchSample{},
		errors:  map[string]int{},
	}
}

// Record stores one completed search request for the given arm. A non-nil err
// tallies an error for the arm and is NOT counted as a latency sample. A nil
// err records the latency and feeds the Prometheus histogram.
func (c *SearchCollector) Record(arm string, latency time.Duration, at time.Time, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.errors[arm]++
		return
	}
	c.samples[arm] = append(c.samples[arm], searchSample{Latency: latency, At: at})
	if c.metrics != nil && c.metrics.SearchLatency != nil {
		c.metrics.SearchLatency.WithLabelValues(c.preset, arm).Observe(latency.Seconds())
	}
}

// Count returns the number of successful latency samples recorded for the arm.
func (c *SearchCollector) Count(arm string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.samples[arm])
}

// TotalCount returns the number of successful latency samples across all arms.
func (c *SearchCollector) TotalCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, s := range c.samples {
		total += len(s)
	}
	return total
}

// Errors returns the error tally recorded for the arm.
func (c *SearchCollector) Errors(arm string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errors[arm]
}

// TotalErrors returns the error tally across all arms.
func (c *SearchCollector) TotalErrors() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, n := range c.errors {
		total += n
	}
	return total
}

// Samples returns a copy of the latency tape for the arm, in record order.
func (c *SearchCollector) Samples(arm string) []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	src := c.samples[arm]
	out := make([]time.Duration, len(src))
	for i := range src {
		out[i] = src[i].Latency
	}
	return out
}

// Arms returns the set of arms that have at least one recorded sample, in no
// particular order.
func (c *SearchCollector) Arms() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.samples))
	for arm := range c.samples {
		out = append(out, arm)
	}
	return out
}

// DiscardBefore drops samples whose At < cutoff, excluding the warmup window
// from the published statistics. Error tallies are not retroactively reclassified.
func (c *SearchCollector) DiscardBefore(cutoff time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for arm, src := range c.samples {
		kept := src[:0]
		for _, s := range src {
			if !s.At.Before(cutoff) {
				kept = append(kept, s)
			}
		}
		c.samples[arm] = kept
	}
}
