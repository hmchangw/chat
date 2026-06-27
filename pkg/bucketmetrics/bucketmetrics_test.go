package bucketmetrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// gaugeValue returns the value of the named Int64 gauge's single datapoint, or
// -1 if the metric is absent.
func gaugeValue(t *testing.T, rm metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "metric %s is not Gauge[int64]", name)
			require.Len(t, g.DataPoints, 1, "expected exactly one datapoint")
			return g.DataPoints[0].Value
		}
	}
	return -1
}

func collect(t *testing.T, reader sdkmetric.Reader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	return rm
}

func newTestReader(t *testing.T) (sdkmetric.Reader, *sdkmetric.MeterProvider) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return reader, mp
}

func TestRegister_ObservesConfiguredHours(t *testing.T) {
	reader, mp := newTestReader(t)

	require.NoError(t, Register(mp.Meter("test"), 72))

	rm := collect(t, reader)
	assert.Equal(t, int64(72), gaugeValue(t, rm, "message_bucket_hours"))
}

// The gauge re-observes on every scrape, so a later collection still reports
// the configured value (no decay to zero).
func TestRegister_ReObservesOnEachCollect(t *testing.T) {
	reader, mp := newTestReader(t)

	require.NoError(t, Register(mp.Meter("test"), 48))

	_ = collect(t, reader)
	rm := collect(t, reader)
	assert.Equal(t, int64(48), gaugeValue(t, rm, "message_bucket_hours"))
}

// Two services on different windows produce different gauge values — the
// divergence an ops alert keys on (max != min across scrape targets).
func TestRegister_DistinctValuesPerService(t *testing.T) {
	readerA, mpA := newTestReader(t)
	readerB, mpB := newTestReader(t)

	require.NoError(t, Register(mpA.Meter("message-worker"), 72))
	require.NoError(t, Register(mpB.Meter("history-service"), 48))

	assert.Equal(t, int64(72), gaugeValue(t, collect(t, readerA), "message_bucket_hours"))
	assert.Equal(t, int64(48), gaugeValue(t, collect(t, readerB), "message_bucket_hours"))
}

func TestRegister_RejectsNonPositiveHours(t *testing.T) {
	_, mp := newTestReader(t)

	assert.Error(t, Register(mp.Meter("test"), 0))
	assert.Error(t, Register(mp.Meter("test"), -1))
}
