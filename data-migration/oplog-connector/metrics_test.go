package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewMetrics(t *testing.T) {
	m, err := newMetrics()
	require.NoError(t, err)
	require.NotNil(t, m)
	// Recording must not panic with a real (no-op exporter) meter.
	m.onPublished(context.Background(), "rocketchat_message", 42)
	m.onPublishError(context.Background(), "rocketchat_message")
	m.onSkipped(context.Background(), "rocketchat_message")
}

func TestMetrics_NilSafe(t *testing.T) {
	var m *metrics // the unit-test case: watcher.metrics is nil
	require.NotPanics(t, func() {
		m.onPublished(context.Background(), "c", 1)
		m.onPublishError(context.Background(), "c")
		m.onSkipped(context.Background(), "c")
	})
}
