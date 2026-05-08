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
