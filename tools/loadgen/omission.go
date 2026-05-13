package main

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// OmissionTracker records coordinated-omission deficits — the gap between
// when a tick was intended (per the rate target) and when the generator
// actually picked it up. Two separate HDR-backed buckets track:
//   - serviced: ticks that were accepted by the semaphore (deficit = actualStart - intendedAt)
//   - dropped:  ticks that were rejected because the semaphore was full (deficit = dropTime - intendedAt)
//
// The difference between serviced and dropped deficit distributions reveals
// whether queuing delay is coming from scheduling jitter or pool saturation.
//
// OmissionTracker is safe for concurrent use. mu guards the CellHistogram
// fields because the underlying HDR library is not goroutine-safe. The cached
// prometheus.Observer fields (obsServiced, obsDropped) are written once at
// construction and are themselves goroutine-safe, so they are read without
// holding mu.
type OmissionTracker struct {
	mu          sync.Mutex
	serviced    *CellHistogram
	dropped     *CellHistogram
	obsServiced prometheus.Observer
	obsDropped  prometheus.Observer
}

// NewOmissionTracker returns an OmissionTracker wired to the given Metrics
// instance for Prometheus reporting.
func NewOmissionTracker(m *Metrics) *OmissionTracker {
	return &OmissionTracker{
		serviced:    NewCellHistogram(),
		dropped:     NewCellHistogram(),
		obsServiced: m.OmissionDeficit.WithLabelValues("false"),
		obsDropped:  m.OmissionDeficit.WithLabelValues("true"),
	}
}

// RecordServiced records the dispatch deficit for a tick that was accepted by
// the semaphore. intendedAt is when the tick was supposed to start (captured
// before the semaphore send); actualStartAt is when the goroutine actually
// began executing.
func (o *OmissionTracker) RecordServiced(intendedAt, actualStartAt time.Time) {
	d := actualStartAt.Sub(intendedAt)
	if d < 0 {
		// Clock skew or scheduler reordering can produce sub-zero deltas;
		// treat as zero deficit rather than a negative-deficit artifact.
		d = 0
	}
	o.mu.Lock()
	o.serviced.Record(d)
	o.mu.Unlock()
	o.obsServiced.Observe(d.Seconds())
}

// RecordDropped records the dispatch deficit for a tick that was rejected
// because the goroutine pool was saturated. intendedAt is when the tick was
// supposed to start; droppedAt is when the drop was recorded.
func (o *OmissionTracker) RecordDropped(intendedAt, droppedAt time.Time) {
	d := droppedAt.Sub(intendedAt)
	if d < 0 {
		d = 0
	}
	o.mu.Lock()
	o.dropped.Record(d)
	o.mu.Unlock()
	o.obsDropped.Observe(d.Seconds())
}

// Quantile returns the value at quantile q (in [0.0, 1.0]) for either the
// dropped or serviced bucket. Returns 0 on an empty bucket.
func (o *OmissionTracker) Quantile(q float64, dropped bool) time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	if dropped {
		return o.dropped.Quantile(q)
	}
	return o.serviced.Quantile(q)
}
