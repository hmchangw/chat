package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingRequester captures every Request call. Returns reply bytes (canned
// or per-call) and a per-call error if seeded.
type recordingRequester struct {
	mu     sync.Mutex
	calls  []requestCall
	reply  []byte
	errSet atomic.Pointer[error]
}

type requestCall struct {
	subject string
	data    []byte
}

func (r *recordingRequester) Request(_ context.Context, subj string, data []byte, _ time.Duration) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, requestCall{subject: subj, data: append([]byte(nil), data...)})
	r.mu.Unlock()
	if ep := r.errSet.Load(); ep != nil {
		return nil, *ep
	}
	if r.reply == nil {
		return []byte("{}"), nil
	}
	return r.reply, nil
}

func (r *recordingRequester) snapshot() []requestCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]requestCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recordingRequester) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestHistoryReadGenerator_ProducesValidSubjects(t *testing.T) {
	p, ok := BuiltinPreset("history-read")
	require.True(t, ok)
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()

	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset:     &p,
		Fixtures:   f,
		SiteID:     "site-local",
		Rate:       200,
		Requester:  rr,
		Metrics:    m,
		MessageIDs: []string{"m-aaaaaa", "m-bbbbbb", "m-cccccc"},
		Timeout:    2 * time.Second,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	calls := rr.snapshot()
	require.NotEmpty(t, calls, "expected at least one history request to be issued")
	for _, c := range calls {
		assert.True(t,
			strings.Contains(c.subject, ".msg.history") ||
				strings.Contains(c.subject, ".msg.get") ||
				strings.Contains(c.subject, ".msg.surrounding") ||
				strings.Contains(c.subject, ".msg.thread"),
			"unexpected subject: %s", c.subject)
		assert.Contains(t, c.subject, "site-local")
		// Body must be valid JSON.
		var any map[string]any
		assert.NoError(t, json.Unmarshal(c.data, &any))
	}
}

func TestHistoryReadGenerator_RespectsHistoryMix(t *testing.T) {
	// Use a 100% LoadHistory mix to verify the picker is wired.
	p := Preset{
		Name:         "history-read-test",
		Users:        10,
		Rooms:        5,
		MentionRate:  0,
		ContentBytes: Range{Min: 1, Max: 1},
		HistoryMix: map[historyRequestKind]int{
			HistoryLoadHistory: 100,
		},
	}
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()

	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 500, Requester: rr, Metrics: m,
		MessageIDs: []string{"m-x"}, Timeout: 1 * time.Second,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)
	calls := rr.snapshot()
	require.NotEmpty(t, calls)
	for _, c := range calls {
		assert.Contains(t, c.subject, ".msg.history",
			"100%% LoadHistory mix should only emit msg.history; got %s", c.subject)
	}
}

func TestHistoryReadGenerator_NoSubscriptions_NoRequests(t *testing.T) {
	p, _ := BuiltinPreset("history-read")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
		Rate: 200, Requester: rr, Metrics: m,
		MessageIDs: []string{"m-x"}, Timeout: 1 * time.Second,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)
	assert.Equal(t, 0, rr.count())
}

func TestHistoryReadGenerator_RequestErrorIncrementsMetric(t *testing.T) {
	p, _ := BuiltinPreset("history-read")
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	myErr := errors.New("nats: timeout")
	rr.errSet.Store(&myErr)
	m := NewMetrics()

	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 200, Requester: rr, Metrics: m,
		MessageIDs: []string{"m-x"}, Timeout: 1 * time.Second,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	var got float64
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_request_errors_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			got += metric.GetCounter().GetValue()
		}
	}
	assert.Greater(t, got, float64(0), "expected loadgen_request_errors_total to increment on Request error")
}

func TestHistoryReadGenerator_ZeroRate_ReturnsError(t *testing.T) {
	p, _ := BuiltinPreset("history-read")
	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset:    &p,
		Fixtures:  Fixtures{},
		SiteID:    "site-local",
		Rate:      0,
		Requester: &recordingRequester{},
		Metrics:   NewMetrics(),
	}, 1)
	err := gen.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate must be > 0")
}

func TestHistoryReadGenerator_RampWithZeroRate_RunsInsteadOfErroring(t *testing.T) {
	// Bug 1: ramp-only configuration (Rate=0, Ramp != nil) used to fail
	// the Rate <= 0 guard and exit immediately with ErrInvalidRate.
	p, _ := BuiltinPreset("history-read")
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 0, Requester: rr, Metrics: m,
		MessageIDs: []string{"m-x"}, Timeout: 1 * time.Second,
		Ramp: &Ramp{From: 100, To: 200, Duration: 200 * time.Millisecond},
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	assert.Greater(t, rr.count(), 0, "ramped read scenario should issue some requests")
}

func TestSearchReadGenerator_RampWithZeroRate_RunsInsteadOfErroring(t *testing.T) {
	p, _ := BuiltinPreset("search-read")
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewSearchReadGenerator(&SearchReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 0, Requester: rr, Metrics: m, Timeout: 1 * time.Second,
		Ramp: &Ramp{From: 100, To: 200, Duration: 200 * time.Millisecond},
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	assert.Greater(t, rr.count(), 0, "ramped search scenario should issue some requests")
}

func TestSearchReadGenerator_ProducesValidSubjects(t *testing.T) {
	p, ok := BuiltinPreset("search-read")
	require.True(t, ok)
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()

	gen := NewSearchReadGenerator(&SearchReadConfig{
		Preset:    &p,
		Fixtures:  f,
		SiteID:    "site-local",
		Rate:      200,
		Requester: rr,
		Metrics:   m,
		Timeout:   2 * time.Second,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	calls := rr.snapshot()
	require.NotEmpty(t, calls)
	for _, c := range calls {
		assert.True(t,
			strings.Contains(c.subject, "request.search.messages") ||
				strings.Contains(c.subject, "request.search.rooms"),
			"unexpected subject: %s", c.subject)
	}
}

func TestSearchReadGenerator_NoUsers_NoRequests(t *testing.T) {
	p, _ := BuiltinPreset("search-read")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewSearchReadGenerator(&SearchReadConfig{
		Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
		Rate: 200, Requester: rr, Metrics: m, Timeout: 1 * time.Second,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)
	assert.Equal(t, 0, rr.count())
}

func TestSearchReadGenerator_QueryFromTokens(t *testing.T) {
	p, _ := BuiltinPreset("search-read")
	require.NotEmpty(t, p.SearchTokens, "search-read preset must define SearchTokens")
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewSearchReadGenerator(&SearchReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 500, Requester: rr, Metrics: m, Timeout: 1 * time.Second,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)
	calls := rr.snapshot()
	require.NotEmpty(t, calls)
	tokenSet := map[string]bool{}
	for _, tok := range p.SearchTokens {
		tokenSet[tok] = true
	}
	for _, c := range calls {
		var got map[string]any
		require.NoError(t, json.Unmarshal(c.data, &got))
		q, _ := got["searchText"].(string)
		assert.True(t, tokenSet[q], "query %q should be drawn from preset.SearchTokens", q)
	}
}

// replyOverrideRequester wraps recordingRequester but returns a fixed reply
// for every call, ignoring any preset reply on recordingRequester.
type replyOverrideRequester struct {
	inner *recordingRequester
	reply []byte
}

func (r *replyOverrideRequester) Request(ctx context.Context, subj string, data []byte, timeout time.Duration) ([]byte, error) {
	// delegate recording but override the reply
	_, _ = r.inner.Request(ctx, subj, data, timeout)
	return r.reply, nil
}

func TestHistoryReadGenerator_AppErrorCountsAsErrored(t *testing.T) {
	p, ok := BuiltinPreset("history-read")
	require.True(t, ok)
	f := BuildFixtures(&p, 42, "site-local")
	m := NewMetrics()
	col := NewCollector(m, p.Name)

	// Return a model.ErrorResponse JSON from the requester.
	appErrReply := []byte(`{"error":"not found"}`)
	rr := &recordingRequester{}
	req := &replyOverrideRequester{inner: rr, reply: appErrReply}

	// Use 100% LoadHistory so all calls hit the same kind.
	p.HistoryMix = map[historyRequestKind]int{HistoryLoadHistory: 100}

	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 200, Requester: req, Metrics: m,
		Collector: col, MessageIDs: []string{"m-x"}, Timeout: 1 * time.Second,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)

	require.Greater(t, rr.count(), 0, "expected at least one request")

	// loadgen_request_errors_total{reason="app_error"} must have been incremented.
	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	var appErrCount float64
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_request_errors_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == "app_error" {
					appErrCount += metric.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Greater(t, appErrCount, float64(0), "app-error reply should increment loadgen_request_errors_total{reason=app_error}")

	// The collector must record the request as errored.
	stats := col.RequestStats()
	var totalErrors int
	for _, s := range stats {
		totalErrors += s.Errors
	}
	assert.Greater(t, totalErrors, 0, "Collector.RequestStats should report errors for app-error replies")
}

func TestSearchReadGenerator_AppErrorCountsAsErrored(t *testing.T) {
	p, ok := BuiltinPreset("search-read")
	require.True(t, ok)
	f := BuildFixtures(&p, 42, "site-local")
	m := NewMetrics()
	col := NewCollector(m, p.Name)

	// Return a model.ErrorResponse JSON from the requester.
	appErrReply := []byte(`{"error":"not found"}`)
	rr := &recordingRequester{}
	req := &replyOverrideRequester{inner: rr, reply: appErrReply}

	gen := NewSearchReadGenerator(&SearchReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 200, Requester: req, Metrics: m,
		Collector: col, Timeout: 1 * time.Second,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)

	require.Greater(t, rr.count(), 0, "expected at least one request")

	// loadgen_request_errors_total{reason="app_error"} must have been incremented.
	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	var appErrCount float64
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_request_errors_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == "app_error" {
					appErrCount += metric.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Greater(t, appErrCount, float64(0), "app-error reply should increment loadgen_request_errors_total{reason=app_error}")

	// The collector must record the request as errored.
	stats := col.RequestStats()
	var totalErrors int
	for _, s := range stats {
		totalErrors += s.Errors
	}
	assert.Greater(t, totalErrors, 0, "Collector.RequestStats should report errors for app-error replies")
}

func TestHistoryReadGenerator_NonErrorJSONNotFlagged(t *testing.T) {
	p, ok := BuiltinPreset("history-read")
	require.True(t, ok)
	f := BuildFixtures(&p, 42, "site-local")
	m := NewMetrics()
	col := NewCollector(m, p.Name)

	// Return a valid non-error JSON (e.g. empty messages list).
	validReply := []byte(`{"messages":[]}`)
	rr := &recordingRequester{}
	req := &replyOverrideRequester{inner: rr, reply: validReply}
	p.HistoryMix = map[historyRequestKind]int{HistoryLoadHistory: 100}

	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 200, Requester: req, Metrics: m,
		Collector: col, MessageIDs: []string{"m-x"}, Timeout: 1 * time.Second,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)

	require.Greater(t, rr.count(), 0)

	// No app_error counter should be set.
	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	var appErrCount float64
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_request_errors_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == "app_error" {
					appErrCount += metric.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Equal(t, float64(0), appErrCount, "valid JSON reply should not be flagged as app_error")

	// Collector should have zero errors.
	stats := col.RequestStats()
	for _, s := range stats {
		assert.Equal(t, 0, s.Errors, "no errors expected for valid non-error replies")
	}
}

func TestIsAppError(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{"error response", []byte(`{"error":"not found"}`), true},
		{"error with room", []byte(`{"error":"forbidden","roomId":"r1"}`), true},
		{"empty error field", []byte(`{"error":""}`), false},
		{"non-error json", []byte(`{"messages":[]}`), false},
		{"empty body", []byte{}, false},
		{"nil body", nil, false},
		{"non-json binary", []byte{0x01, 0x02, 0x03}, false},
		{"empty json object", []byte(`{}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantErr, isAppError(tc.data))
		})
	}
}
