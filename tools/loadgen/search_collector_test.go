package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchCollector_RecordsPerArm(t *testing.T) {
	m := NewMetrics()
	c := NewSearchCollector(m, "search-small")
	now := time.Unix(100, 0)

	c.Record("C", 5*time.Millisecond, now, nil)
	c.Record("C", 7*time.Millisecond, now, nil)
	c.Record("A", 9*time.Millisecond, now, nil)

	assert.Equal(t, 2, c.Count("C"))
	assert.Equal(t, 1, c.Count("A"))
	assert.Equal(t, 0, c.Count("B"))

	cSamples := c.Samples("C")
	require.Len(t, cSamples, 2)
	assert.Equal(t, 5*time.Millisecond, cSamples[0])
	assert.Equal(t, 7*time.Millisecond, cSamples[1])
}

func TestSearchCollector_RecordsErrorsPerArm(t *testing.T) {
	m := NewMetrics()
	c := NewSearchCollector(m, "search-small")
	now := time.Unix(100, 0)

	c.Record("A", 0, now, assert.AnError)
	c.Record("A", 0, now, assert.AnError)
	c.Record("B", 0, now, assert.AnError)

	assert.Equal(t, 2, c.Errors("A"))
	assert.Equal(t, 1, c.Errors("B"))
	assert.Equal(t, 0, c.Errors("C"))
	// Errored samples are not counted as latency samples.
	assert.Equal(t, 0, c.Count("A"))
}

func TestSearchCollector_DiscardBefore_DropsWarmup(t *testing.T) {
	m := NewMetrics()
	c := NewSearchCollector(m, "search-small")
	base := time.Unix(1000, 0)

	c.Record("C", 1*time.Millisecond, base, nil)                  // warmup
	c.Record("C", 2*time.Millisecond, base.Add(time.Second), nil) // kept
	c.Record("A", 3*time.Millisecond, base, nil)                  // warmup

	cutoff := base.Add(500 * time.Millisecond)
	c.DiscardBefore(cutoff)

	assert.Equal(t, 1, c.Count("C"))
	assert.Equal(t, 0, c.Count("A"))
	require.Len(t, c.Samples("C"), 1)
	assert.Equal(t, 2*time.Millisecond, c.Samples("C")[0])
}

func TestSearchCollector_TotalCount(t *testing.T) {
	m := NewMetrics()
	c := NewSearchCollector(m, "search-small")
	now := time.Unix(100, 0)
	c.Record("C", time.Millisecond, now, nil)
	c.Record("A", time.Millisecond, now, nil)
	c.Record("B", time.Millisecond, now, nil)
	assert.Equal(t, 3, c.TotalCount())
}

func TestSearchCollector_FeedsHistogram(t *testing.T) {
	m := NewMetrics()
	c := NewSearchCollector(m, "search-small")
	now := time.Unix(100, 0)
	c.Record("A", 10*time.Millisecond, now, nil)

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	var found bool
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_search_latency_seconds" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			armMatch := false
			for _, l := range metric.GetLabel() {
				if l.GetName() == "arm" && l.GetValue() == "A" {
					armMatch = true
				}
			}
			if armMatch && metric.GetHistogram().GetSampleCount() == 1 {
				found = true
			}
		}
	}
	assert.True(t, found, "expected one observation on loadgen_search_latency_seconds{arm=A}")
}

func TestNewMetrics_RegistersSearchLatency(t *testing.T) {
	m := NewMetrics()
	require.NotNil(t, m.SearchLatency)
	m.SearchLatency.WithLabelValues("search-small", "C").Observe(0.001)

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	got := map[string]bool{}
	for _, mf := range mfs {
		got[mf.GetName()] = true
	}
	assert.True(t, got["loadgen_search_latency_seconds"], "loadgen_search_latency_seconds not registered")
}
