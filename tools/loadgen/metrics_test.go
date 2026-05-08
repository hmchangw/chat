package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetrics_RegistersRequestLatencyHistogram(t *testing.T) {
	m := NewMetrics()
	require.NotNil(t, m)
	require.NotNil(t, m.RequestLatency, "RequestLatency histogram must exist")

	// Force one observation so the histogram appears in the gathered output.
	m.RequestLatency.WithLabelValues("history-read", "history", "load_history").Observe(0.001)

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	var found bool
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_request_latency_seconds" {
			continue
		}
		found = true
		assert.Contains(t, mf.GetHelp(), "latency")
		// Histogram expects three labels: preset, scenario, kind.
		got := mf.GetMetric()[0]
		labelSet := map[string]string{}
		for _, lp := range got.GetLabel() {
			labelSet[lp.GetName()] = lp.GetValue()
		}
		assert.Equal(t, "history-read", labelSet["preset"])
		assert.Equal(t, "history", labelSet["scenario"])
		assert.Equal(t, "load_history", labelSet["kind"])

		// Histogram must use the standard bucket set the other histograms use.
		buckets := got.GetHistogram().GetBucket()
		require.NotEmpty(t, buckets, "histogram must have buckets configured")
		// Lowest bucket should be small (≤ 5ms) so sub-millisecond latencies bin correctly.
		assert.LessOrEqual(t, buckets[0].GetUpperBound(), 0.005)
	}
	assert.True(t, found, "loadgen_request_latency_seconds must be registered")
}

func TestNewMetrics_LatencyHistogramSharesBucketsWithE1(t *testing.T) {
	m := NewMetrics()
	m.E1Latency.WithLabelValues("test").Observe(0.010)
	m.RequestLatency.WithLabelValues("test", "history", "load_history").Observe(0.010)

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	var e1Buckets, reqBuckets []float64
	for _, mf := range mfs {
		switch mf.GetName() {
		case "loadgen_e1_latency_seconds":
			for _, b := range mf.GetMetric()[0].GetHistogram().GetBucket() {
				e1Buckets = append(e1Buckets, b.GetUpperBound())
			}
		case "loadgen_request_latency_seconds":
			for _, b := range mf.GetMetric()[0].GetHistogram().GetBucket() {
				reqBuckets = append(reqBuckets, b.GetUpperBound())
			}
		}
	}
	require.NotEmpty(t, e1Buckets)
	require.NotEmpty(t, reqBuckets)
	assert.Equal(t, e1Buckets, reqBuckets,
		"RequestLatency must share the bucket set with E1Latency for consistent dashboards")
}

func TestNewMetrics_LabelCardinalityIsBounded(t *testing.T) {
	// Sanity: enumerate the documented scenario+kind combinations to make
	// sure they all register without colliding on labels.
	m := NewMetrics()
	cases := []struct{ scenario, kind string }{
		{"history", "load_history"},
		{"history", "get_message_by_id"},
		{"history", "load_surrounding"},
		{"history", "get_thread_messages"},
		{"search", "search_messages"},
		{"search", "search_rooms"},
	}
	for _, c := range cases {
		m.RequestLatency.WithLabelValues("history-read", c.scenario, c.kind).Observe(0.001)
	}

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "loadgen_request_latency") {
			continue
		}
		// One Metric entry per unique label combination.
		assert.Len(t, mf.GetMetric(), len(cases))
	}
}
