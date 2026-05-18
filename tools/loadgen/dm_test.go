package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestPublishOne_HonorsDMRatioAtRuntime verifies that publishOne picks rooms
// in the correct DM/channel proportion when Preset.DMRatio > 0. Pre-fix, the
// subscription pool bias (DM rooms have 2 members, channel rooms 2-500) caused
// ~10% DM even at DMRatio=0.6; post-fix, the DMRatio path is wired in and
// the runtime distribution must fall within ±10% of the configured ratio.
func TestPublishOne_HonorsDMRatioAtRuntime(t *testing.T) {
	p := &Preset{
		Name:         "dm-ratio-runtime",
		Users:        100,
		Rooms:        100,
		DMRatio:      0.6,
		RoomSizeDist: DistMixed, // mixed: channels can have up to 500 members
		SenderDist:   DistUniform,
		ContentBytes: Range{Min: 50, Max: 50},
	}
	f := BuildFixtures(p, 42, "site-local")

	// Build a room-ID → type lookup for classifying captured publishes.
	roomType := make(map[string]model.RoomType, len(f.Rooms))
	for i := range f.Rooms {
		roomType[f.Rooms[i].ID] = f.Rooms[i].Type
	}

	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset:    p,
		Fixtures:  f,
		SiteID:    "site-local",
		Rate:      500,
		Inject:    InjectFrontdoor,
		Publisher: rp,
		Metrics:   m,
		Collector: c,
	}, 42)

	const iterations = 1000
	ctx := context.Background()
	for i := 0; i < iterations; i++ {
		g.publishOne(ctx)
	}

	calls := rp.snapshot()
	require.Greater(t, len(calls), 800, "publishOne must produce at least 800 identifiable room-type publishes")

	dmCount := 0
	channelCount := 0
	for _, call := range calls {
		// publishOne uses the frontdoor subject: chat.user.{account}.room.{roomID}.{siteID}.msg.send
		// Extract RoomID by unmarshalling the request body.
		var req model.SendMessageRequest
		if err := json.Unmarshal(call.data, &req); err != nil {
			continue
		}
		// The subject contains the roomID: derive room type from the RoomSubs index.
		// We use the subscription's RoomID captured in the subject tokens.
		// Simpler: look up which subscription was used via Fixtures.
		// Since we only have the payload and subject, we classify by roomID in the subject.
		// Subject format: chat.user.{account}.room.{roomID}.{siteID}.msg.send
		// Parse roomID from subject:
		subj := call.subject
		rt := roomTypeFromSubject(subj, roomType, &f)
		switch rt {
		case model.RoomTypeDM:
			dmCount++
		case model.RoomTypeChannel:
			channelCount++
		default:
			// BotDM, Discussion, or unclassified — excluded from the ratio check.
		}
	}

	total := dmCount + channelCount
	require.Greater(t, total, 800,
		"published messages must be classifiable to DM or channel rooms; got dm=%d channel=%d total-calls=%d",
		dmCount, channelCount, len(calls))

	dmFraction := float64(dmCount) / float64(total)
	assert.InDelta(t, 0.6, dmFraction, 0.1,
		"DMRatio=0.6 must produce ~60%% DM publishes at runtime; got dm=%d channel=%d (%.1f%%)",
		dmCount, channelCount, dmFraction*100)

	// Also verify PublishedByRoomType counter was incremented correctly.
	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	counterDM := int64(gatheredCounterLabelPair(mfs,
		"loadgen_published_by_room_type_total", "preset", p.Name, "room_type", "dm"))
	counterChannel := int64(gatheredCounterLabelPair(mfs,
		"loadgen_published_by_room_type_total", "preset", p.Name, "room_type", "channel"))
	assert.Equal(t, int64(dmCount), counterDM,
		"PublishedByRoomType{dm} counter must match classified dm publishes")
	assert.Equal(t, int64(channelCount), counterChannel,
		"PublishedByRoomType{channel} counter must match classified channel publishes")
}

// roomTypeFromSubject extracts the room ID from a frontdoor publish subject and
// looks up its type in the roomType map. Returns empty string when the subject
// cannot be parsed or the room is unknown.
//
// Subject format: chat.user.{account}.room.{roomID}.{siteID}.msg.send
// Token indices (0-based): 0=chat 1=user 2={account} 3=room 4={roomID} 5={siteID} 6=msg 7=send
func roomTypeFromSubject(subj string, byID map[string]model.RoomType, f *Fixtures) model.RoomType {
	// Check each room's ID to find which one matches the subject.
	// Use index-based range to avoid a per-element copy of the large Room struct.
	for i := range f.Rooms {
		if len(f.Rooms[i].ID) > 0 && containsToken(subj, f.Rooms[i].ID) {
			return byID[f.Rooms[i].ID]
		}
	}
	return ""
}

// containsToken reports whether s contains token as a dot-delimited segment.
func containsToken(s, token string) bool {
	if len(s) == 0 || len(token) == 0 {
		return false
	}
	// Subject is dot-delimited; token may appear as .{token}. or at start/end.
	needle := "." + token + "."
	if indexOfStr(s, needle) >= 0 {
		return true
	}
	// Edge: token at end of subject.
	suffix := "." + token
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return true
	}
	// Edge: token at start of subject (unlikely for our subjects but defensive).
	prefix := token + "."
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func indexOfStr(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
