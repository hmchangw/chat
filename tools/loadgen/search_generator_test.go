package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestBuiltinSearchPreset(t *testing.T) {
	for _, name := range []string{"search-small", "search-medium", "search-large"} {
		t.Run(name, func(t *testing.T) {
			p, ok := BuiltinSearchPreset(name)
			require.True(t, ok)
			assert.Equal(t, name, p.Name)
			assert.Greater(t, p.Rate, 0)
			assert.Greater(t, p.Duration, time.Duration(0))
			assert.Greater(t, p.MaxInFlight, 0)
			require.NotEmpty(t, p.Queries, "query pool must be populated")
		})
	}
	_, ok := BuiltinSearchPreset("nope")
	assert.False(t, ok)
}

func TestBuiltinSearchPreset_HasCJKQuery(t *testing.T) {
	p, ok := BuiltinSearchPreset("search-small")
	require.True(t, ok)
	found := false
	for _, q := range p.Queries {
		for _, r := range q {
			if r > 0x2E7F { // CJK and beyond
				found = true
			}
		}
	}
	assert.True(t, found, "multilingual query pool must include a CJK query")
}

// fakeSearchRequester records every request and replies with a fixed payload.
type fakeSearchRequester struct {
	mu       sync.Mutex
	requests []fakeRequest
	reply    []byte
	latency  time.Duration
	err      error
}

func (f *fakeSearchRequester) Request(ctx context.Context, subj string, data []byte, timeout time.Duration) ([]byte, error) {
	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{Subject: subj, Data: append([]byte(nil), data...)})
	f.mu.Unlock()
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if f.latency > 0 {
		select {
		case <-time.After(f.latency):
		case <-reqCtx.Done():
			return nil, reqCtx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.reply == nil {
		return []byte(`{"messages":[],"total":0}`), nil
	}
	return f.reply, nil
}

func (f *fakeSearchRequester) snapshot() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func makeSearchGenCfg(t *testing.T, req *fakeSearchRequester, arm string) (*SearchGeneratorConfig, *SearchCollector) {
	t.Helper()
	p, ok := BuiltinSearchPreset("search-small")
	require.True(t, ok)
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	hp, ok := BuiltinHistoryPreset("history-small")
	require.True(t, ok)
	res := BuildHistoryFixtures(&hp, 42, "site-a", now)
	m := NewMetrics()
	collector := NewSearchCollector(m, p.Name)
	return &SearchGeneratorConfig{
		Preset:         &p,
		Fixtures:       &res,
		SiteID:         "site-a",
		Arm:            arm,
		Rate:           50,
		RequestTimeout: 1 * time.Second,
		Requester:      req,
		Collector:      collector,
		MaxInFlight:    8,
	}, collector
}

func TestSearchGenerator_RequestOne_BuildsRequest(t *testing.T) {
	req := &fakeSearchRequester{}
	cfg, collector := makeSearchGenCfg(t, req, "A")
	gen := NewSearchGenerator(cfg, 7)

	gen.requestOne(context.Background())

	reqs := req.snapshot()
	require.Len(t, reqs, 1)

	// Subject is a search.messages subject for site-a.
	assert.True(t, strings.Contains(reqs[0].Subject, ".request.search.site-a.messages"),
		"unexpected subject %q", reqs[0].Subject)
	assert.True(t, strings.HasPrefix(reqs[0].Subject, "chat.user."))

	var body model.SearchMessagesRequest
	require.NoError(t, json.Unmarshal(reqs[0].Data, &body))
	assert.Equal(t, "A", body.Variant, "arm must be driven via req.Variant")
	assert.NotEmpty(t, body.Query)

	// Query must come from the preset pool.
	inPool := false
	for _, q := range cfg.Preset.Queries {
		if q == body.Query {
			inPool = true
		}
	}
	assert.True(t, inPool, "query %q not in preset pool", body.Query)

	// Latency recorded into the collector under the arm.
	assert.Equal(t, 1, collector.Count("A"))
}

func TestSearchGenerator_RecordsError(t *testing.T) {
	req := &fakeSearchRequester{err: assert.AnError}
	cfg, collector := makeSearchGenCfg(t, req, "B")
	gen := NewSearchGenerator(cfg, 7)

	gen.requestOne(context.Background())

	assert.Equal(t, 0, collector.Count("B"))
	assert.Equal(t, 1, collector.Errors("B"))
}

func TestSearchGenerator_Run_RespectsArm(t *testing.T) {
	req := &fakeSearchRequester{}
	cfg, _ := makeSearchGenCfg(t, req, "C")
	cfg.Rate = 2000
	gen := NewSearchGenerator(cfg, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)

	reqs := req.snapshot()
	require.NotEmpty(t, reqs)
	for _, r := range reqs {
		var body model.SearchMessagesRequest
		require.NoError(t, json.Unmarshal(r.Data, &body))
		assert.Equal(t, "C", body.Variant)
	}
}
