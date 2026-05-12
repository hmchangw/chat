package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// counterValue returns the value of a counter metric with the given name.
// It returns 0 if the metric doesn't exist or on gather error.
func counterValue(m *Metrics, name string) float64 {
	mfs, err := m.Registry.Gather()
	if err != nil {
		return 0
	}
	return gatheredCounterValue(mfs, name, "", "")
}

// counterValueLabeled returns the value of a counter metric with the given name
// and label filter. labelName and labelValue must match exactly.
// It returns 0 if the metric doesn't exist, no labels match, or on gather error.
func counterValueLabeled(m *Metrics, name, labelName, labelValue string) float64 {
	mfs, err := m.Registry.Gather()
	if err != nil {
		return 0
	}
	return gatheredCounterValue(mfs, name, labelName, labelValue)
}

// TestCounterValue_Empty verifies that counterValue returns 0 for a non-incremented counter.
func TestCounterValue_Empty(t *testing.T) {
	m := NewMetrics()
	assert.Equal(t, 0.0, counterValue(m, "nonexistent_metric"))
}

// TestCounterValue_Incremented verifies that counterValue returns the correct value
// after incrementing a counter.
func TestCounterValue_Incremented(t *testing.T) {
	m := NewMetrics()
	m.Published.WithLabelValues("small", "measured", "0", "100-200").Inc()
	m.Published.WithLabelValues("small", "measured", "0", "100-200").Inc()
	m.Published.WithLabelValues("medium", "measured", "0", "100-200").Inc()
	assert.Equal(t, 3.0, counterValue(m, "loadgen_published_total"))
}
