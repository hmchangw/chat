package main

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestBuiltinPresets_ContainsAllFour(t *testing.T) {
	names := []string{"small", "medium", "large", "realistic"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p, ok := BuiltinPreset(name)
			require.True(t, ok, "preset %q must exist", name)
			assert.Equal(t, name, p.Name)
			assert.Greater(t, p.Users, 0)
			assert.Greater(t, p.Rooms, 0)
		})
	}
}

func TestBuiltinPresets_UnknownReturnsFalse(t *testing.T) {
	_, ok := BuiltinPreset("nonexistent")
	assert.False(t, ok)
}

func TestBuiltinPresets_UniformShape(t *testing.T) {
	for _, name := range []string{"small", "medium", "large"} {
		t.Run(name, func(t *testing.T) {
			p, ok := BuiltinPreset(name)
			require.True(t, ok)
			assert.Equal(t, DistUniform, p.RoomSizeDist)
			assert.Equal(t, DistUniform, p.SenderDist)
			assert.InDelta(t, 0.0, p.MentionRate, 1e-9)
			assert.InDelta(t, 0.0, p.ThreadRate, 1e-9)
		})
	}
}

func TestBuiltinPresets_RealisticShape(t *testing.T) {
	p, ok := BuiltinPreset("realistic")
	require.True(t, ok)
	assert.Equal(t, DistMixed, p.RoomSizeDist)
	assert.Equal(t, DistZipf, p.SenderDist)
	assert.Greater(t, p.MentionRate, 0.0)
	assert.Greater(t, p.ThreadRate, 0.0)
	assert.Greater(t, p.ContentBytes.Max, p.ContentBytes.Min)
}

func TestBuildFixtures_DeterministicAcrossCalls(t *testing.T) {
	p, _ := BuiltinPreset("small")
	a := BuildFixtures(&p, 42, "site-local")
	b := BuildFixtures(&p, 42, "site-local")
	assert.Equal(t, a.Users, b.Users)
	assert.Equal(t, a.Rooms, b.Rooms)
	assert.Equal(t, a.Subscriptions, b.Subscriptions)
}

func TestBuildFixtures_SmallCountsAndShape(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	assert.Len(t, f.Users, 10)
	assert.Len(t, f.Rooms, 5)
	// uniform: every user is in at least one room
	users := make(map[string]bool)
	for _, s := range f.Subscriptions {
		users[s.User.ID] = true
		assert.Equal(t, "site-local", s.SiteID)
	}
	assert.Len(t, users, 10)
	for _, r := range f.Rooms {
		assert.Equal(t, "channel", string(r.Type))
		assert.Equal(t, "site-local", r.SiteID)
	}
}

func TestBuildFixtures_RealisticMixesChannelAndDM(t *testing.T) {
	p, _ := BuiltinPreset("realistic")
	f := BuildFixtures(&p, 42, "site-local")
	var channels, dms int
	for _, r := range f.Rooms {
		switch r.Type { //nolint:exhaustive
		case "channel":
			channels++
		case "dm":
			dms++
		}
	}
	assert.Greater(t, channels, 0)
	assert.Greater(t, dms, 0)
	// DM rooms must have exactly 2 members
	dmMembers := make(map[string]int)
	for _, s := range f.Subscriptions {
		for _, r := range f.Rooms {
			if r.ID == s.RoomID && r.Type == "dm" {
				dmMembers[r.ID]++
			}
		}
	}
	for id, n := range dmMembers {
		assert.Equal(t, 2, n, "dm room %s must have 2 members", id)
	}
}

func TestBuildFixtures_FewerUsersThanRooms_PadsToTwoMembers(t *testing.T) {
	// Synthetic preset: 3 users, 5 rooms — round-robin alone leaves rooms 3
	// and 4 with fewer than 2 members, exercising the padding branch.
	p := &Preset{
		Name: "tiny", Users: 3, Rooms: 5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	}
	f := BuildFixtures(p, 42, "site-local")
	require.Len(t, f.Rooms, 5)
	for i := range f.Rooms {
		assert.GreaterOrEqual(t, f.Rooms[i].UserCount, 2,
			"room %s must have at least 2 members after padding", f.Rooms[i].ID)
	}
}

func TestBuiltinPreset_HistoryRead(t *testing.T) {
	p, ok := BuiltinPreset("history-read")
	require.True(t, ok, "preset \"history-read\" must exist")
	assert.Equal(t, "history-read", p.Name)

	small, _ := BuiltinPreset("small")
	assert.Equal(t, small.Users, p.Users, "history-read seeds the small population")
	assert.Equal(t, small.Rooms, p.Rooms, "history-read seeds the small population")

	require.NotNil(t, p.HistoryMix, "history-read carries a request-type weight map")
	assert.Equal(t, 60, p.HistoryMix[HistoryLoadHistory])
	assert.Equal(t, 20, p.HistoryMix[HistoryGetMessageByID])
	assert.Equal(t, 10, p.HistoryMix[HistoryLoadSurrounding])
	assert.Equal(t, 10, p.HistoryMix[HistoryGetThreadMessages])

	var sum int
	for _, w := range p.HistoryMix {
		sum += w
	}
	assert.Equal(t, 100, sum, "history-read weights sum to 100")
}

func TestBuiltinPreset_SearchRead(t *testing.T) {
	p, ok := BuiltinPreset("search-read")
	require.True(t, ok, "preset \"search-read\" must exist")
	assert.Equal(t, "search-read", p.Name)

	small, _ := BuiltinPreset("small")
	assert.Equal(t, small.Users, p.Users)
	assert.Equal(t, small.Rooms, p.Rooms)

	require.NotNil(t, p.SearchMix, "search-read carries a request-type weight map")
	assert.Equal(t, 50, p.SearchMix[SearchMessagesKind])
	assert.Equal(t, 50, p.SearchMix[SearchRoomsKind])

	var sum int
	for _, w := range p.SearchMix {
		sum += w
	}
	assert.Equal(t, 100, sum)

	require.Len(t, p.SearchTokens, 10, "search-read carries 10 deterministic query tokens")
	for _, tok := range p.SearchTokens {
		assert.NotEmpty(t, tok)
	}
	// determinism: BuiltinPreset must return equal values across calls.
	q, _ := BuiltinPreset("search-read")
	assert.Equal(t, p.SearchTokens, q.SearchTokens)
}

func TestPickHistoryKind_HonoursWeights(t *testing.T) {
	p, _ := BuiltinPreset("history-read")
	r := rand.New(rand.NewSource(42))
	const iters = 10_000

	counts := map[historyRequestKind]int{}
	for i := 0; i < iters; i++ {
		counts[pickHistoryKind(r, p.HistoryMix)]++
	}
	for kind, weight := range p.HistoryMix {
		want := float64(iters) * float64(weight) / 100.0
		assert.InDelta(t, want, float64(counts[kind]), float64(iters)*0.02,
			"kind %d count %d off from weighted expectation %.0f (±2%%)",
			kind, counts[kind], want)
	}
}

func TestPickSearchKind_HonoursWeights(t *testing.T) {
	p, _ := BuiltinPreset("search-read")
	r := rand.New(rand.NewSource(42))
	const iters = 10_000

	counts := map[searchRequestKind]int{}
	for i := 0; i < iters; i++ {
		counts[pickSearchKind(r, p.SearchMix)]++
	}
	for kind, weight := range p.SearchMix {
		want := float64(iters) * float64(weight) / 100.0
		assert.InDelta(t, want, float64(counts[kind]), float64(iters)*0.02,
			"kind %d count %d off from weighted expectation %.0f (±2%%)",
			kind, counts[kind], want)
	}
}

func TestSampleWithoutReplacement_CapsAtUserCount(t *testing.T) {
	// Requesting more samples than users available silently caps at len(users).
	r := rand.New(rand.NewSource(1))
	users := []model.User{{ID: "u-0"}, {ID: "u-1"}}
	out := sampleWithoutReplacement(r, users, 99)
	assert.Len(t, out, 2)
}
