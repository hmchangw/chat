package main

import (
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
