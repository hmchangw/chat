package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

// makeMixedRooms returns a slice of rooms: channelCount channels followed by dmCount DMs.
func makeMixedRooms(channelCount, dmCount int) []model.Room {
	rooms := make([]model.Room, 0, channelCount+dmCount)
	for i := 0; i < channelCount; i++ {
		rooms = append(rooms, model.Room{
			ID:   fmt.Sprintf("channel-room-%d", i),
			Type: model.RoomTypeChannel,
		})
	}
	for i := 0; i < dmCount; i++ {
		rooms = append(rooms, model.Room{
			ID:   fmt.Sprintf("dm-room-%d", i),
			Type: model.RoomTypeDM,
		})
	}
	return rooms
}

func TestPreset_DMRatioPicksProportionally(t *testing.T) {
	p := &Preset{
		Name:    "test-dm",
		DMRatio: 0.6,
	}
	f := &Fixtures{
		Rooms: makeMixedRooms(50, 50),
	}
	counts := map[model.RoomType]int{model.RoomTypeChannel: 0, model.RoomTypeDM: 0}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 10000; i++ {
		room := pickRoomByDMRatio(p, f, rng)
		counts[room.Type]++
	}
	assert.InDelta(t, 6000, counts[model.RoomTypeDM], 200,
		"got dm=%d channel=%d", counts[model.RoomTypeDM], counts[model.RoomTypeChannel])
}

func TestPreset_DMRatio_AllChannel(t *testing.T) {
	p := &Preset{
		Name:    "all-channel",
		DMRatio: 0.0,
	}
	f := &Fixtures{
		Rooms: makeMixedRooms(50, 50),
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		room := pickRoomByDMRatio(p, f, rng)
		assert.Equal(t, model.RoomTypeChannel, room.Type, "DMRatio=0 must always pick channel")
	}
}

func TestPreset_DMRatio_AllDM(t *testing.T) {
	p := &Preset{
		Name:    "all-dm",
		DMRatio: 1.0,
	}
	f := &Fixtures{
		Rooms: makeMixedRooms(50, 50),
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		room := pickRoomByDMRatio(p, f, rng)
		assert.Equal(t, model.RoomTypeDM, room.Type, "DMRatio=1 must always pick DM")
	}
}

func TestPreset_DMRatio_FallsBackWhenNoDMRooms(t *testing.T) {
	p := &Preset{
		Name:    "dm-heavy-no-dms",
		DMRatio: 0.9,
	}
	// Only channel rooms — fallback must return a room without panicking.
	f := &Fixtures{
		Rooms: makeMixedRooms(10, 0),
	}
	rng := rand.New(rand.NewSource(1))
	room := pickRoomByDMRatio(p, f, rng)
	require.NotNil(t, room)
	assert.Equal(t, model.RoomTypeChannel, room.Type, "fallback must return a channel room")
}

func TestSeed_CreatesDMRoomsAccordingToRatio(t *testing.T) {
	p := &Preset{Name: "test-seed", Users: 100, Rooms: 50, DMRatio: 0.6,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	}
	f := BuildFixtures(p, 42, "site-local")
	var dmCount, channelCount int
	for _, r := range f.Rooms {
		if r.Type == model.RoomTypeDM {
			dmCount++
		} else {
			channelCount++
		}
	}
	// With DMRatio=0.6 of 50 rooms: 30 DM + 20 channel.
	assert.Equal(t, 30, dmCount)
	assert.Equal(t, 20, channelCount)
}

func TestSeed_DMRoomsUseIdgenBuildDMRoomID(t *testing.T) {
	p := &Preset{
		Name: "test-dm-ids", Users: 20, Rooms: 10, DMRatio: 0.5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	}
	f := BuildFixtures(p, 1, "site-local")
	// DM rooms should have IDs built from user pairs (sorted concat, ~34 chars).
	for _, r := range f.Rooms {
		if r.Type != model.RoomTypeDM {
			continue
		}
		// DM room IDs are sorted concats of two user IDs: "u-000000u-000001" style.
		// They are NOT the "room-NNNNNN" format used for channels.
		assert.NotContains(t, r.ID, "room-",
			"DM room ID must not be the channel room format; got %q", r.ID)
		assert.Greater(t, len(r.ID), 10,
			"DM room ID must be non-trivially long; got %q", r.ID)
	}
}

func TestSeed_DMRoomsHaveExactlyTwoMembers(t *testing.T) {
	p := &Preset{
		Name: "test-dm-members", Users: 20, Rooms: 10, DMRatio: 0.5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	}
	f := BuildFixtures(p, 2, "site-local")
	subsByRoom := make(map[string]int)
	for _, s := range f.Subscriptions {
		subsByRoom[s.RoomID]++
	}
	for _, r := range f.Rooms {
		if r.Type != model.RoomTypeDM {
			continue
		}
		assert.Equal(t, 2, subsByRoom[r.ID],
			"DM room %s must have exactly 2 subscriptions", r.ID)
	}
}

func TestSeed_ZeroDMRatio_AllChannels(t *testing.T) {
	p := &Preset{
		Name: "zero-dm", Users: 10, Rooms: 5, DMRatio: 0.0,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	}
	f := BuildFixtures(p, 3, "site-local")
	for _, r := range f.Rooms {
		assert.Equal(t, model.RoomTypeChannel, r.Type,
			"DMRatio=0 must produce only channel rooms")
	}
}

func TestBuiltinPresets_DMRatioValues(t *testing.T) {
	cases := []struct {
		name    string
		wantDMR float64
	}{
		{"realistic", 0.6},
		{"channel-heavy", 0.1},
		{"dm-heavy", 0.85},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := BuiltinPreset(tc.name)
			require.True(t, ok, "preset %q must exist", tc.name)
			assert.InDelta(t, tc.wantDMR, p.DMRatio, 1e-9,
				"preset %q DMRatio want %.2f got %.2f", tc.name, tc.wantDMR, p.DMRatio)
		})
	}
}

func TestReport_BreaksDownByRoomType(t *testing.T) {
	s := &Summary{
		Preset:         "test",
		Seed:           42,
		Site:           "site-local",
		TargetRate:     500,
		ActualRate:     498.0,
		Duration:       60e9, // 60s in nanoseconds
		Inject:         "frontdoor",
		Sent:           30000,
		SentByRoomType: map[string]int64{"channel": 12000, "dm": 18000},
	}
	var buf bytes.Buffer
	require.NoError(t, PrintSummary(&buf, s))
	out := buf.String()
	assert.Contains(t, out, "channel=12000")
	assert.Contains(t, out, "dm=18000")
}

func TestReport_NoBreakdownWhenMapEmpty(t *testing.T) {
	s := &Summary{
		Preset:     "small",
		Seed:       1,
		Site:       "site-local",
		TargetRate: 100,
		ActualRate: 99.0,
		Duration:   30e9,
		Inject:     "frontdoor",
		// SentByRoomType not set
	}
	var buf bytes.Buffer
	require.NoError(t, PrintSummary(&buf, s))
	out := buf.String()
	assert.NotContains(t, out, "Sent breakdown:")
}
