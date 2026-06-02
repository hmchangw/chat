package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProm returns canned counter samples per service name. The key is matched
// as a substring of the query (the query embeds the service selector).
type fakeProm struct {
	// fn maps a query + window to samples; nil samples => empty result.
	fn  func(query string, start, end time.Time) []promSample
	err error
}

func (f fakeProm) RangeQuery(_ context.Context, query string, start, end time.Time, _ time.Duration) ([]promSeries, error) {
	if f.err != nil {
		return nil, f.err
	}
	samples := f.fn(query, start, end)
	if samples == nil {
		return nil, nil
	}
	return []promSeries{{Labels: map[string]string{}, Samples: samples}}, nil
}

func counterSamples(start time.Time, startVal, cores float64, windowSec int) []promSample {
	// Linear counter: startVal at t0, rising `cores` per second for windowSec.
	return []promSample{
		{T: start, V: startVal},
		{T: start.Add(time.Duration(windowSec) * time.Second), V: startVal + cores*float64(windowSec)},
	}
}

func TestEngine_cpuCores(t *testing.T) {
	start := time.Unix(1000, 0)
	q := fakeProm{fn: func(_ string, s, _ time.Time) []promSample {
		return counterSamples(s, 100, 2.5, 30) // 2.5 cores over 30s
	}}
	eng := newBottleneckEngine(q, identityResolver{}, 0.10, 5*time.Second)
	cores, reset, ok := eng.cpuCores(context.Background(), "message-worker", start, start.Add(30*time.Second))
	require.True(t, ok)
	assert.False(t, reset)
	assert.InDelta(t, 2.5, cores, 0.001)
}

func TestEngine_cpuCores_CounterReset(t *testing.T) {
	start := time.Unix(1000, 0)
	q := fakeProm{fn: func(_ string, s, _ time.Time) []promSample {
		return []promSample{{T: s, V: 500}, {T: s.Add(30 * time.Second), V: 3}} // dropped -> restart
	}}
	eng := newBottleneckEngine(q, identityResolver{}, 0.10, 5*time.Second)
	_, reset, ok := eng.cpuCores(context.Background(), "x", start, start.Add(30*time.Second))
	require.True(t, ok)
	assert.True(t, reset)
}
