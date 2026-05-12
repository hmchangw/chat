package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCellHistogram_DefaultParams(t *testing.T) {
	h := NewCellHistogram()
	require.NotNil(t, h)
	// p99 of an empty histogram returns 0.
	assert.Equal(t, time.Duration(0), h.Quantile(0.99))
}

func TestCellHistogram_RecordAndQuantile(t *testing.T) {
	h := NewCellHistogram()
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Millisecond)
	}
	// 100µs floor / 3 sig digits gives accuracy ≤0.1% at p99.
	p99 := h.Quantile(0.99)
	assert.InDelta(t, 990*time.Millisecond, p99, float64(2*time.Millisecond))
}

func TestCellHistogram_SmallSampleNotDegenerate(t *testing.T) {
	// 200 samples: v1's slice-based percentile reported the 198th sorted value.
	// HDR returns a real percentile.
	h := NewCellHistogram()
	for i := 0; i < 200; i++ {
		h.Record(time.Duration(i+1) * time.Millisecond)
	}
	assert.Greater(t, h.Quantile(0.99), 195*time.Millisecond)
	assert.LessOrEqual(t, h.Quantile(0.99), 200*time.Millisecond)
}

func TestCellHistogram_Merge(t *testing.T) {
	a := NewCellHistogram()
	b := NewCellHistogram()
	for i := 0; i < 100; i++ {
		a.Record(10 * time.Millisecond)
	}
	for i := 0; i < 100; i++ {
		b.Record(20 * time.Millisecond)
	}
	require.NoError(t, a.Merge(b))
	// Median of {100x 10ms, 100x 20ms} is 10–20ms; p99 is ~20ms.
	// HDR bucket resolution at 20ms with 100µs floor (3 sig digits) is ~65µs wide,
	// so the highest-equivalent-value of the bucket is 20.054015ms.
	assert.InDelta(t, 20*time.Millisecond, a.Quantile(0.99), float64(100*time.Microsecond))
	assert.Equal(t, int64(200), a.Count())
}

func TestNewWindowHistogram_EmptyReturnsZero(t *testing.T) {
	h := NewWindowHistogram()
	require.NotNil(t, h)
	assert.Equal(t, time.Duration(0), h.Quantile(0.99),
		"empty WindowHistogram must return 0 to preserve abort-watcher 'no samples yet' semantics")
}

func TestNewWindowHistogram_TighterFloor(t *testing.T) {
	h := NewWindowHistogram() // for the abort watcher
	h.Record(50 * time.Microsecond)
	// 10µs floor / 3 digits accommodates 50µs without overflow.
	// The bucket containing 50µs has highest-equivalent-value ~57.343µs; allow
	// one full sub-bucket width (~8µs) of tolerance.
	assert.InDelta(t, 50*time.Microsecond, h.Quantile(0.5), float64(10*time.Microsecond))
}
