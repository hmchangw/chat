package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingPublisher struct {
	mu    sync.Mutex
	calls []publishCall
}

type publishCall struct {
	subject string
	data    []byte
}

func (r *recordingPublisher) Publish(_ context.Context, subject string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, publishCall{subject: subject, data: append([]byte(nil), data...)})
	return nil
}

func (r *recordingPublisher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingPublisher) snapshot() []publishCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]publishCall, len(r.calls))
	copy(out, r.calls)
	return out
}

type errorPublisher struct{}

func (e *errorPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return fmt.Errorf("publish error")
}

func TestGenerator_SendsExpectedCount(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset:    &p,
		Fixtures:  f,
		SiteID:    "site-local",
		Rate:      200,
		Inject:    InjectFrontdoor,
		Publisher: rp,
		Metrics:   m,
		Collector: c,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))

	count := rp.count()
	// 200 msg/s for ~250ms: expect 30-70 publishes (wide tolerance for scheduler).
	assert.GreaterOrEqual(t, count, 30)
	assert.LessOrEqual(t, count, 70)
}

func TestGenerator_UsesFrontdoorSubject(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	calls := rp.snapshot()
	require.NotEmpty(t, calls)
	for i := range calls {
		assert.Contains(t, calls[i].subject, ".msg.send")
		assert.Contains(t, calls[i].subject, "site-local")
	}
}

func TestGenerator_UsesCanonicalSubjectWhenInjectCanonical(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectCanonical,
		Publisher: rp, Metrics: m,
		Collector: c,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	calls := rp.snapshot()
	require.NotEmpty(t, calls)
	for i := range calls {
		assert.Contains(t, calls[i].subject, "chat.msg.canonical.site-local.created")
	}

	// In canonical mode, the Generator should NOT populate byReqID because
	// canonical injection bypasses the gatekeeper (no reply is expected).
	// Consequently Finalize should report zero missing replies even though
	// no replies ever arrived.
	missingReplies, _ := c.Finalize()
	assert.Equal(t, 0, missingReplies)
}

func TestGenerator_IncrementsPublishedMetric(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)

	var got int64
	metrics, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, mf := range metrics {
		if mf.GetName() == "loadgen_published_total" {
			for _, metric := range mf.GetMetric() {
				got += int64(metric.GetCounter().GetValue())
			}
		}
	}
	assert.Greater(t, got, int64(0))
}

func TestGenerator_Run_ReturnsErrorForZeroRate(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 0, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	err := g.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate must be > 0")
}

func TestGenerator_PublishError_IncrementsErrorMetric(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	ep := &errorPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: ep, Metrics: m,
		Collector: c,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)

	var publishErrors int64
	metrics, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, mf := range metrics {
		if mf.GetName() == "loadgen_publish_errors_total" {
			for _, metric := range mf.GetMetric() {
				publishErrors += int64(metric.GetCounter().GetValue())
			}
		}
	}
	assert.Greater(t, publishErrors, int64(0))

	// Publish errors should have cleaned up the pending entries, so Finalize
	// reports no "missing replies" or "missing broadcasts" attributable to
	// publish-side failures.
	missingReplies, missingBroadcasts := c.Finalize()
	assert.Equal(t, 0, missingReplies)
	assert.Equal(t, 0, missingBroadcasts)
}

func TestGenerator_Content_WithMentionRate(t *testing.T) {
	p, _ := BuiltinPreset("realistic")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	// Run long enough to statistically hit the 10% mention rate.
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 500, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 99)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	calls := rp.snapshot()
	require.NotEmpty(t, calls)
	// With 10% mention rate and ~100 messages, at least one should contain "@user-".
	foundMention := false
	for i := range calls {
		if strings.Contains(string(calls[i].data), "@user-") {
			foundMention = true
			break
		}
	}
	assert.True(t, foundMention, "expected at least one message with a mention")
}

func TestGenerator_EmptySubscriptions_NoPublish(t *testing.T) {
	p, _ := BuiltinPreset("small")
	rp := &recordingPublisher{}
	m := NewMetrics()
	// Use empty fixtures — no subscriptions.
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
		Rate: 200, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	assert.Equal(t, 0, rp.count())
}
