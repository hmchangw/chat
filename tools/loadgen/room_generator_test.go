package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoomRPCGenerator_ProducesValidSubjects(t *testing.T) {
	p, ok := BuiltinPreset("room-rpc")
	require.True(t, ok)
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewRoomRPCGenerator(&RoomRPCConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 200, Requester: rr, Metrics: m, Timeout: 1 * time.Second,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	calls := rr.snapshot()
	require.NotEmpty(t, calls)
	for _, c := range calls {
		valid := strings.Contains(c.subject, ".rooms.list") ||
			strings.Contains(c.subject, ".rooms.get.") ||
			strings.Contains(c.subject, ".member.list") ||
			strings.Contains(c.subject, ".create") ||
			strings.Contains(c.subject, ".member.add")
		assert.True(t, valid, "unexpected subject: %s", c.subject)
	}
}

func TestRoomRPCGenerator_RespectsRoomMix(t *testing.T) {
	p := Preset{
		Name: "room-rpc-test", Users: 10, Rooms: 5,
		ContentBytes: Range{Min: 1, Max: 1},
		RoomMix:      map[roomRequestKind]int{RoomsListKind: 100},
	}
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewRoomRPCGenerator(&RoomRPCConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 500, Requester: rr, Metrics: m, Timeout: 1 * time.Second,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)
	calls := rr.snapshot()
	require.NotEmpty(t, calls)
	for _, c := range calls {
		assert.Contains(t, c.subject, ".rooms.list",
			"100%% RoomsList mix should only emit rooms.list; got %s", c.subject)
	}
}

func TestRoomRPCGenerator_NoFixtures_NoRequests(t *testing.T) {
	p, _ := BuiltinPreset("room-rpc")
	rr := &recordingRequester{}
	gen := NewRoomRPCGenerator(&RoomRPCConfig{
		Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
		Rate: 200, Requester: rr, Metrics: NewMetrics(), Timeout: 1 * time.Second,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)
	assert.Equal(t, 0, rr.count())
}

func TestRoomRPCGenerator_RampWithZeroRate_RunsInsteadOfErroring(t *testing.T) {
	// Bug 1: ramp-only configuration (Rate=0, Ramp != nil) used to fail
	// the Rate <= 0 guard and exit immediately with ErrInvalidRate.
	p, _ := BuiltinPreset("room-rpc")
	f := BuildFixtures(&p, 42, "site-local")
	rr := &recordingRequester{}
	m := NewMetrics()
	gen := NewRoomRPCGenerator(&RoomRPCConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 0, Requester: rr, Metrics: m, Timeout: 1 * time.Second,
		Ramp: &Ramp{From: 100, To: 200, Duration: 200 * time.Millisecond},
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	assert.Greater(t, rr.count(), 0, "ramped room scenario should issue some requests")
}

func TestRoomRPCGenerator_ZeroRate_ReturnsErrInvalidRate(t *testing.T) {
	p, _ := BuiltinPreset("room-rpc")
	gen := NewRoomRPCGenerator(&RoomRPCConfig{
		Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
		Rate: 0, Requester: &recordingRequester{}, Metrics: NewMetrics(),
	}, 1)
	err := gen.Run(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRate)
}

func TestRoomRPCGenerator_AppErrorCountsAsErrored(t *testing.T) {
	p, ok := BuiltinPreset("room-rpc")
	require.True(t, ok)
	f := BuildFixtures(&p, 42, "site-local")
	m := NewMetrics()
	col := NewCollector(m, p.Name)

	// Return a model.ErrorResponse JSON from the requester.
	appErrReply := []byte(`{"error":"forbidden"}`)
	rr := &recordingRequester{}
	req := &replyOverrideRequester{inner: rr, reply: appErrReply}

	// Use 100% RoomsList so all calls hit the same kind.
	p.RoomMix = map[roomRequestKind]int{RoomsListKind: 100}

	gen := NewRoomRPCGenerator(&RoomRPCConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 200, Requester: req, Metrics: m,
		Collector: col, Timeout: 1 * time.Second,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = gen.Run(ctx)

	require.Greater(t, rr.count(), 0, "expected at least one request")

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

	stats := col.RequestStats()
	var totalErrors int
	for _, s := range stats {
		totalErrors += s.Errors
	}
	assert.Greater(t, totalErrors, 0, "Collector.RequestStats should report errors for app-error replies")
}
