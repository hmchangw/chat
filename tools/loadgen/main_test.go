package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLastToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"chat.user.alice.response.abc-123", "abc-123"},
		{"abc", "abc"},       // no dot
		{"", ""},             // empty
		{"a.b.c.d.e.f", "f"}, // many dots
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, lastToken(c.in))
		})
	}
}

func TestCounterValue(t *testing.T) {
	m := NewMetrics()
	m.Published.WithLabelValues("small").Inc()
	m.Published.WithLabelValues("small").Inc()
	m.Published.WithLabelValues("medium").Inc()
	assert.Equal(t, float64(3), counterValue(m, "loadgen_published_total"))
	assert.Equal(t, float64(0), counterValue(m, "nonexistent_metric"))
}

func TestCounterValueLabeled(t *testing.T) {
	m := NewMetrics()
	m.PublishErrors.WithLabelValues("small", "publish").Inc()
	m.PublishErrors.WithLabelValues("small", "publish").Inc()
	m.PublishErrors.WithLabelValues("small", "gatekeeper").Inc()
	m.PublishErrors.WithLabelValues("large", "publish").Inc()
	// By reason=publish: two "small" + one "large" = 3
	assert.Equal(t, float64(3), counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "publish"))
	// By reason=gatekeeper: one
	assert.Equal(t, float64(1), counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "gatekeeper"))
	// Unknown label value
	assert.Equal(t, float64(0), counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "nope"))
}

func TestWriteCSVFile_RoundTrip(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublish("r-1", "m-1", now)
	c.RecordReply("r-1", now.Add(5*time.Millisecond))
	c.RecordBroadcast("m-1", now.Add(8*time.Millisecond))

	path := filepath.Join(t.TempDir(), "out.csv")
	require.NoError(t, writeCSVFile(path, c))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	// Header present
	require.True(t, strings.HasPrefix(out, "timestamp_ns,request_id,metric,latency_ns"))
	// At least one E1 row and one E2 row
	require.Contains(t, out, ",E1,")
	require.Contains(t, out, ",E2,")
}

func TestWriteCSVFile_EmptyCollector(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")

	path := filepath.Join(t.TempDir(), "empty.csv")
	require.NoError(t, writeCSVFile(path, c))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	// Header still present, no data rows
	require.True(t, strings.HasPrefix(out, "timestamp_ns,request_id,metric,latency_ns"))
	require.NotContains(t, out, ",E1,")
	require.NotContains(t, out, ",E2,")
}

func TestNewNatsCorePublisher_CanonicalSetsUseJetStream(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectCanonical, nil)
	require.True(t, p.useJetStream)
}

func TestNewNatsCorePublisher_FrontdoorDoesNotSetUseJetStream(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectFrontdoor, nil)
	require.False(t, p.useJetStream)
}

func TestNewNatsCorePublisher_FieldWiring(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectCanonical, nil)
	assert.Nil(t, p.nc)
	assert.Nil(t, p.js)
	assert.True(t, p.useJetStream)

	p2 := newNatsCorePublisher(nil, InjectFrontdoor, nil)
	assert.Nil(t, p2.nc)
	assert.Nil(t, p2.js)
	assert.False(t, p2.useJetStream)
}

func TestMetricsHandler_ServesOpenMetrics(t *testing.T) {
	m := NewMetrics()
	m.Published.WithLabelValues("small").Inc()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "loadgen_published_total")
}

func TestMetricsHandler_ContentType(t *testing.T) {
	m := NewMetrics()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	ct := rec.Header().Get("Content-Type")
	require.NotEmpty(t, ct)
	// Prometheus text format
	require.Contains(t, ct, "text/plain")
}
