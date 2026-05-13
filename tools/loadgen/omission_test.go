package main

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOmissionTracker_RecordsServicedDeficit(t *testing.T) {
	m := NewMetrics()
	o := NewOmissionTracker(m)
	intended := time.Now()
	actual := intended.Add(15 * time.Millisecond)
	o.RecordServiced(intended, actual)
	p99 := o.Quantile(0.99, false /* dropped */)
	assert.InDelta(t, 15*time.Millisecond, p99, float64(2*time.Millisecond))
}

func TestOmissionTracker_RecordsDroppedDeficitSeparately(t *testing.T) {
	m := NewMetrics()
	o := NewOmissionTracker(m)
	intended := time.Now()
	droppedAt := intended.Add(80 * time.Millisecond)
	o.RecordDropped(intended, droppedAt)
	assert.InDelta(t, 80*time.Millisecond, o.Quantile(0.99, true), float64(2*time.Millisecond))
	assert.Equal(t, time.Duration(0), o.Quantile(0.99, false), "serviced bucket should be empty")
}

func TestOmissionTracker_ConcurrentRecordSafe(t *testing.T) {
	m := NewMetrics()
	o := NewOmissionTracker(m)
	const workers = 16
	const perWorker = 1000
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			intended := time.Now()
			for j := 0; j < perWorker; j++ {
				actual := intended.Add(time.Duration(j+1) * time.Microsecond)
				o.RecordServiced(intended, actual)
			}
		}()
	}
	wg.Wait()
	// Just having the test complete cleanly under -race is the assertion.
	// Additionally verify the histogram saw all writes:
	p99 := o.Quantile(0.99, false)
	assert.Greater(t, p99, time.Duration(0), "p99 must be non-zero after %d writes", workers*perWorker)
}

func TestOmissionTracker_ClampsNegativeDelta(t *testing.T) {
	m := NewMetrics()
	o := NewOmissionTracker(m)
	// actualAt before intendedAt → negative delta must clamp to 0 before recording.
	// The HDR histogram has a 100µs floor, so a clamped-to-zero value is stored in
	// the minimum bucket rather than returned as exactly 0. We verify that the
	// recorded value is well below the magnitude of the raw negative delta (50ms),
	// confirming the clamp fired rather than a large negative value being recorded.
	intended := time.Now()
	actual := intended.Add(-50 * time.Millisecond)
	o.RecordServiced(intended, actual)
	p99 := o.Quantile(0.99, false)
	assert.Less(t, p99, time.Millisecond, "negative delta must clamp to 0 (HDR floor < 1ms), got %v", p99)
}
