package main

import (
	"crypto/ecdh"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	randv2 "math/rand/v2"
	"sort"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// churnFixturePrefix is the ID prefix used for the dedicated subscription-churn
// user/room pool. Using a distinct prefix ensures these fixtures are trivially
// distinguishable from the main preset pool and are not selected by other
// scenarios.
const churnFixturePrefix = "loadgen-churn-"

// churnFixtureUsers is the number of users added to the churn pool.
const churnFixtureUsers = 50

// churnFixtureRooms is the number of rooms added to the churn pool.
const churnFixtureRooms = 20

// augmentWithChurnFixtures appends a dedicated user/room pool prefixed with
// "loadgen-churn-" to f for the subscription-churn scenario. The added users
// and rooms are append-only — existing fixtures are preserved. No Subscriptions
// are added; the scenario builds them dynamically via the state machine.
//
// The seed and preset parameters are accepted for API symmetry with BuildFixtures
// callers; the churn pool is fixed-size and does not vary by preset.
func augmentWithChurnFixtures(f *Fixtures, _ *Preset, _ int64) Fixtures {
	for i := 0; i < churnFixtureUsers; i++ {
		id := churnFixturePrefix + "user-" + padInt(i)
		f.Users = append(f.Users, model.User{
			ID:      id,
			Account: id,
		})
	}
	for i := 0; i < churnFixtureRooms; i++ {
		id := churnFixturePrefix + "room-" + padInt(i)
		f.Rooms = append(f.Rooms, model.Room{
			ID:   id,
			Type: model.RoomTypeChannel,
		})
	}
	// Note: NO Subscriptions added — the scenario builds them dynamically.
	return *f
}

// padInt formats i as a zero-padded 7-digit string, matching the fixture ID
// convention used in BuildFixtures (e.g. "0000001").
func padInt(i int) string {
	return fmt.Sprintf("%07d", i)
}

// augmentWithFirstDMFixtures adds nPairs user pairs that have NEVER messaged
// each other. Users are prefixed with "loadgen-firstdm-". No rooms or
// subscriptions are added — the scenario creates DM rooms on-the-fly via
// idgen.BuildDMRoomID at first publish.
//
// The preset parameter is accepted for API symmetry with augmentWithChurnFixtures;
// the first-DM pool size is controlled exclusively by nPairs.
func augmentWithFirstDMFixtures(f *Fixtures, _ *Preset, nPairs int) {
	const prefix = "loadgen-firstdm-"
	for i := 0; i < nPairs; i++ {
		userA := model.User{
			ID:      prefix + "user-" + padInt(2*i+1),
			Account: prefix + "user-" + padInt(2*i+1),
		}
		userB := model.User{
			ID:      prefix + "user-" + padInt(2*i+2),
			Account: prefix + "user-" + padInt(2*i+2),
		}
		f.Users = append(f.Users, userA, userB)
	}
}

// pickDMPairs picks n distinct unordered user pairs deterministically by
// iterating in (i, j) order where i < j. If n exceeds C(len(users), 2), the
// available pairs are returned and a warning is logged. The rng parameter is
// accepted for API symmetry with callers that own a seeded source; the pair
// selection itself is deterministic (first-n in i<j order) so two calls with
// the same user slice and n always return the same pairs.
func pickDMPairs(users []model.User, n int, _ *rand.Rand) [][2]model.User {
	pairs := make([][2]model.User, 0, n)
	for i := 0; i < len(users) && len(pairs) < n; i++ {
		for j := i + 1; j < len(users) && len(pairs) < n; j++ {
			pairs = append(pairs, [2]model.User{users[i], users[j]})
		}
	}
	if len(pairs) < n {
		slog.Warn("requested more DM rooms than user pairs allow",
			"requested", n, "available", len(pairs), "users", len(users))
	}
	return pairs
}

// Distribution names the shape of a per-preset random selection.
type Distribution string

const (
	DistUniform Distribution = "uniform"
	DistMixed   Distribution = "mixed"
	DistZipf    Distribution = "zipf"
)

// Range holds an inclusive min/max for integer quantities like content size.
type Range struct {
	Min int
	Max int
}

// historyRequestKind enumerates the history-service request handlers
// exercised by the `history-read` preset.
type historyRequestKind int

const (
	HistoryLoadHistory historyRequestKind = iota
	HistoryGetMessageByID
	HistoryLoadSurrounding
	HistoryGetThreadMessages
)

// searchRequestKind enumerates the search-service request handlers
// exercised by the `search-read` preset.
type searchRequestKind int

const (
	SearchMessagesKind searchRequestKind = iota
	SearchRoomsKind
)

// roomRequestKind enumerates the room-service RPCs exercised by the
// `room-rpc` preset.
type roomRequestKind int

const (
	RoomsListKind roomRequestKind = iota
	RoomsGetKind
	MemberListKind
	RoomCreateKind
	MemberAddKind
)

// builtinSearchTokens is the deterministic 10-token query bag used by the
// `search-read` preset. Drawn from generic English so seeded message
// content (Lorem-style) yields realistic hit rates.
var builtinSearchTokens = []string{
	"hello", "thanks", "team", "review", "deploy",
	"meeting", "update", "today", "release", "status",
}

// Preset is a named, fully deterministic workload specification.
//
// Fixture-shaping fields (Users / Rooms / *Dist / ContentBytes / *Rate)
// drive seeding. Read-scenario fields (HistoryMix) are populated only for
// presets that exercise natsrouter request/reply handlers; they are nil
// for the messaging-pipeline presets.
type Preset struct {
	Name         string
	Users        int
	Rooms        int
	RoomSizeDist Distribution
	SenderDist   Distribution
	ContentBytes Range
	MentionRate  float64
	ThreadRate   float64

	// HistoryMix is the integer-weighted request-type mix for the
	// `history-read` scenario. Keys are exhaustive over historyRequestKind;
	// values are non-negative and conventionally sum to 100.
	HistoryMix map[historyRequestKind]int

	// SearchMix is the integer-weighted request-type mix for the
	// `search-read` scenario, with the same conventions as HistoryMix.
	SearchMix map[searchRequestKind]int

	// SearchTokens is the deterministic bag of query strings the
	// `search-read` scenario draws from when building requests.
	SearchTokens []string

	// SearchScope is the room-scope filter applied to SearchRooms requests.
	// Empty string means "no scope filter" (all room types).
	SearchScope string

	// SearchSize is the requested page size for both SearchMessages and
	// SearchRooms requests. Zero defaults to 20 at the call site (preset
	// is the source of truth, but a zero default avoids breaking older
	// presets that haven't set it).
	SearchSize int

	// RoomMix is the integer-weighted request-type mix for the
	// `room-rpc` scenario. Keys are exhaustive over roomRequestKind;
	// values are non-negative and conventionally sum to 100.
	RoomMix map[roomRequestKind]int

	// WriteIDPrefix is prepended to any IDs / names generated by
	// state-mutating ticks (RoomCreate, MemberAdd) so they're trivially
	// identifiable in forensic checks of the loadgen Mongo database.
	// Zero value = no prefix.
	WriteIDPrefix string

	// DMRatio is the fraction of messages published to DM rooms vs channel
	// rooms by the messaging-pipeline scenario. 0.0 = all channel, 1.0 = all
	// DM. The seed step provisions DM rooms in proportion: round(Rooms×DMRatio)
	// DM rooms + remainder channel rooms. Zero value = all channel (no DM rooms).
	DMRatio float64

	// OfflineUserFraction is the fraction of fixture users treated as offline
	// when the notification-fanout scenario (Phase 3 §3.4b) is active. Offline
	// users do not receive in-app notifications; the scenario instead expects
	// push/email delivery. Zero value = all users online (no push/email traffic).
	// Only consumed when built with the notif_routing_ready build tag.
	OfflineUserFraction float64

	// DNDUserFraction is the fraction of fixture users in Do-Not-Disturb mode
	// when the notification-fanout scenario (Phase 3 §3.4b) is active. DND users
	// suppress push/email delivery entirely. Zero value = no DND users.
	// Only consumed when built with the notif_routing_ready build tag.
	DNDUserFraction float64

	// Burst envelope: periodically multiply the effective rate by BurstRatio
	// for BurstDuration every BurstPeriod. Models incident/spike traffic shapes
	// (e.g. a support flood after an outage). Zero values disable the envelope
	// — the preset runs at a steady Rate.
	//
	// BaselineRate is the quiet-period rate. BurstRatio is the integer
	// multiplier applied during the burst window (e.g. 40 = 40× baseline).
	// Burst-aware presets set BaselineRate instead of relying on Rate;
	// non-burst presets leave all four fields zero.
	BaselineRate  int
	BurstPeriod   time.Duration
	BurstRatio    int
	BurstDuration time.Duration

	// EditRate + DeleteRate: relative weights for the message-mutate scenario.
	// EditRate/(EditRate+DeleteRate) = fraction of mutations that are edits;
	// the remainder are deletes. E.g., EditRate=0.7, DeleteRate=0.3 means 70%
	// of mutations are edits. Zero values in both fields default to edit-only.
	// Non-mutate scenarios leave both at zero (no behavior change).
	EditRate   float64
	DeleteRate float64

	// AuthIdleConnections is the number of idle NATS connections to spin up
	// for the auth-reconnect-storm preset. M connections are dropped at
	// T+AuthStormPeriod and recovery time is measured via
	// loadgen_auth_reconnect_seconds + loadgen_auth_reconnects_completed_total.
	// Only consumed by the auth-load scenario when the storm preset is active.
	AuthIdleConnections int

	// AuthStormPeriod is the interval between reconnect-storm events for the
	// auth-reconnect-storm preset. 0 means a single one-shot drop at T+30s.
	// Can be overridden per-run by --auth-storm-period.
	AuthStormPeriod time.Duration
}

var builtinPresets = map[string]Preset{
	"small": {
		Name: "small", Users: 10, Rooms: 5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"medium": {
		Name: "medium", Users: 1000, Rooms: 100,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"large": {
		Name: "large", Users: 10000, Rooms: 1000,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"realistic": {
		Name: "realistic", Users: 1000, Rooms: 100,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.10,
		ThreadRate:   0.05,
		DMRatio:      0.6,
		EditRate:     0.05,
		DeleteRate:   0.02,
	},
	"channel-heavy": {
		Name: "channel-heavy", Users: 1000, Rooms: 100,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.10,
		ThreadRate:   0.05,
		DMRatio:      0.1,
	},
	"dm-heavy": {
		Name: "dm-heavy", Users: 200, Rooms: 100,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.10,
		ThreadRate:   0.05,
		DMRatio:      0.85,
	},
	"history-read": {
		Name: "history-read", Users: 10, Rooms: 5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
		HistoryMix: map[historyRequestKind]int{
			HistoryLoadHistory:       60,
			HistoryGetMessageByID:    20,
			HistoryLoadSurrounding:   10,
			HistoryGetThreadMessages: 10,
		},
	},
	"search-read": {
		Name: "search-read", Users: 10, Rooms: 5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
		SearchMix: map[searchRequestKind]int{
			SearchMessagesKind: 50,
			SearchRoomsKind:    50,
		},
		SearchTokens: builtinSearchTokens,
		SearchScope:  "channel",
		SearchSize:   20,
	},
	"room-rpc": {
		Name: "room-rpc", Users: 1000, Rooms: 100,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
		MentionRate:  0.10,
		RoomMix: map[roomRequestKind]int{
			RoomsListKind:  60,
			RoomsGetKind:   20,
			MemberListKind: 10,
			RoomCreateKind: 8,
			MemberAddKind:  2,
		},
		WriteIDPrefix: "loadgen-",
	},
	// incident-burst models the "support flood after an outage" scenario:
	// 100 users across 10 rooms with a quiet baseline of 5 msg/s that spikes
	// to 200 msg/s (40×) for 10s every 30s. DMRatio 0.2 keeps most traffic
	// in channel rooms (realistic for a support/incident channel pattern).
	"incident-burst": {
		Name:          "incident-burst",
		Users:         100,
		Rooms:         10,
		RoomSizeDist:  DistUniform,
		SenderDist:    DistUniform,
		ContentBytes:  Range{Min: 100, Max: 1000},
		DMRatio:       0.2,
		BaselineRate:  5,
		BurstPeriod:   30 * time.Second,
		BurstRatio:    40,
		BurstDuration: 10 * time.Second,
	},

	// announce-room: 10k members in a single channel room, very low write rate.
	// Spec requests 1 write/min; Rate is a per-second field so Rate=1 is the
	// closest granularity. Operators may pass --rate=1 and rely on the slow
	// tick interval; sub-1 rates (e.g. 1/min) are out of scope for this task.
	// All users are members of the single room — DistUniform round-robin
	// assigns every user to the one room (totalRooms=1, every i%1==0).
	// TODO Phase 3.2 follow-up: introduce DistAll constant for explicit "all
	// members in one room" semantics when Preset gains a subscription-distribution
	// field.
	"announce-room": {
		Name:         "announce-room",
		Users:        10000,
		Rooms:        1,
		RoomSizeDist: DistUniform,
		SenderDist:   DistUniform,
		ContentBytes: Range{Min: 100, Max: 500},
		DMRatio:      0,
	},

	// firehose-room: 1k members, 50 writes/s (moderate, balanced fanout).
	// Default preset for the large-room-broadcast scenario.
	"firehose-room": {
		Name:         "firehose-room",
		Users:        1000,
		Rooms:        1,
		RoomSizeDist: DistUniform,
		SenderDist:   DistUniform,
		ContentBytes: Range{Min: 50, Max: 200},
		DMRatio:      0,
	},

	// bot-room: 100 members, 200 writes/s (high write rate from few senders).
	"bot-room": {
		Name:         "bot-room",
		Users:        100,
		Rooms:        1,
		RoomSizeDist: DistUniform,
		SenderDist:   DistUniform,
		ContentBytes: Range{Min: 20, Max: 100},
		DMRatio:      0,
	},

	// auth-reconnect-storm: spins up M=1000 idle NATS connections, drops them
	// at T+30s, and measures recovery time. Used by the auth-load scenario to
	// benchmark auth-service under a client reconnect flood.
	// Users/Rooms are placeholders — the storm preset doesn't exercise message
	// routing; fixture seeding is a no-op for this workload.
	"auth-reconnect-storm": {
		Name:                "auth-reconnect-storm",
		Users:               100,
		Rooms:               1,
		RoomSizeDist:        DistUniform,
		SenderDist:          DistUniform,
		ContentBytes:        Range{Min: 200, Max: 200},
		AuthIdleConnections: 1000,
		AuthStormPeriod:     30 * time.Second,
	},
}

// BuiltinPreset looks up a preset by name.
func BuiltinPreset(name string) (Preset, bool) {
	p, ok := builtinPresets[name]
	return p, ok
}

// AllBuiltinPresets returns a slice of all built-in presets. The slice is a
// copy — mutations do not affect the registry.
func AllBuiltinPresets() []Preset {
	out := make([]Preset, 0, len(builtinPresets))
	for name := range builtinPresets {
		p := builtinPresets[name]
		out = append(out, p)
	}
	return out
}

// pickWeighted draws a key from the weights map with probability proportional
// to its weight. Iteration order is sorted by key for stable enumeration.
// Total weight must be > 0; the caller owns that invariant since both callers
// derive weights from the preset registry where the invariant is enforced.
//
// S4: uses math/rand/v2 globals (lock-free ChaCha8 internally) instead of
// the old caller-supplied *math/rand.Rand. Determinism trade-off:
// per-tick picks are no longer reproducible across runs, but fixture
// seeding (in BuildFixtures) still uses the seeded v1 Rand for
// deterministic users/rooms/subscriptions. For capacity testing the
// per-tick non-determinism is irrelevant; for unit tests the picker's
// distribution properties (asserted via large-sample chi-squared bounds)
// don't depend on a specific seed.
func pickWeighted[K ~int](weights map[K]int) K {
	keys := make([]K, 0, len(weights))
	for k := range weights {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	total := 0
	for _, k := range keys {
		total += weights[k]
	}
	if total <= 0 {
		// Defensive: zero-weight map (e.g. a hand-crafted preset with all
		// weights set to 0) used to panic in r.Intn(0). Return the
		// zero-value K so callers can branch on it; conventional callers
		// already early-return when len(weights)==0 above the call.
		var zero K
		return zero
	}
	pick := randv2.IntN(total)
	cum := 0
	for _, k := range keys {
		cum += weights[k]
		if pick < cum {
			return k
		}
	}
	return keys[len(keys)-1] // unreachable when total>0
}

func pickHistoryKind(weights map[historyRequestKind]int) historyRequestKind {
	return pickWeighted(weights)
}

func pickSearchKind(weights map[searchRequestKind]int) searchRequestKind {
	return pickWeighted(weights)
}

func pickRoomKind(weights map[roomRequestKind]int) roomRequestKind {
	return pickWeighted(weights)
}

// Fixtures is the full seed data for a preset run. RoomKeys are load-test
// fixtures derived deterministically from `seed`, not real secrets.
type Fixtures struct {
	Users         []model.User
	Rooms         []model.Room
	Subscriptions []model.Subscription
	RoomKeys      map[string]roomkeystore.RoomKeyPair

	// RoomSubs maps RoomID to indices into Subscriptions[] for fast room-based
	// subscription picking. Used by publishOne when Preset.DMRatio > 0 to honor
	// the DM/channel split without an O(N) linear scan per publish.
	RoomSubs map[string][]int
}

var (
	engNameBank     = []string{"Alice Wang", "Bob Chen", "Carol Lee", "Dave Liu", "Eve Zhang"}
	chineseNameBank = []string{"愛麗絲", "鮑勃", "卡蘿", "戴夫", "伊芙"}
)

// BuildFixtures is a pure function of (preset, seed, siteID) producing the
// full fixture set. Two calls with equal inputs produce equal outputs.
func BuildFixtures(p *Preset, seed int64, siteID string) Fixtures {
	r := rand.New(rand.NewSource(seed))
	now := time.Unix(0, 0).UTC() // fixed so output is deterministic

	users := make([]model.User, p.Users)
	for i := 0; i < p.Users; i++ {
		users[i] = model.User{
			ID:          fmt.Sprintf("u-%06d", i),
			Account:     fmt.Sprintf("user-%d", i),
			SiteID:      siteID,
			EngName:     engNameBank[i%len(engNameBank)],
			ChineseName: chineseNameBank[i%len(chineseNameBank)],
		}
	}

	// Split rooms by DMRatio: dmCount = round(Rooms × DMRatio), channels = remainder.
	dmCount := int(math.Round(float64(p.Rooms) * p.DMRatio))
	channelCount := p.Rooms - dmCount

	rooms := make([]model.Room, 0, p.Rooms)

	// Build channel rooms first.
	for i := 0; i < channelCount; i++ {
		rooms = append(rooms, model.Room{
			ID:        fmt.Sprintf("room-%06d", i),
			Name:      fmt.Sprintf("room-%d", i),
			Type:      model.RoomTypeChannel,
			SiteID:    siteID,
			UserCount: 0, // filled after membership
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	// Build DM rooms using deterministic sorted-pair IDs from idgen.
	dmPairs := pickDMPairs(users, dmCount, r)
	for k := range dmPairs {
		rooms = append(rooms, model.Room{
			ID:        idgen.BuildDMRoomID(dmPairs[k][0].ID, dmPairs[k][1].ID),
			Name:      fmt.Sprintf("dm-%s-%s", dmPairs[k][0].ID, dmPairs[k][1].ID),
			Type:      model.RoomTypeDM,
			SiteID:    siteID,
			UserCount: 2,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	var subs []model.Subscription
	for i := range rooms {
		var members []model.User
		if rooms[i].Type == model.RoomTypeDM {
			// DM rooms: members are the pair stored in dmPairs; reconstruct from index.
			pairIdx := i - channelCount
			members = []model.User{dmPairs[pairIdx][0], dmPairs[pairIdx][1]}
		} else {
			members = pickMembers(r, p, i, channelCount, &rooms[i], users)
		}
		rooms[i].UserCount = len(members)
		for j := range members {
			subs = append(subs, model.Subscription{
				ID:       fmt.Sprintf("sub-%s-%s", rooms[i].ID, members[j].ID),
				User:     model.SubscriptionUser{ID: members[j].ID, Account: members[j].Account},
				RoomID:   rooms[i].ID,
				RoomType: rooms[i].Type,
				SiteID:   siteID,
				Roles:    []model.Role{model.RoleMember},
				JoinedAt: now,
			})
		}
	}

	// Build RoomSubs index: RoomID -> []int of subscription indices.
	// Used by publishOne when DMRatio>0 to pick a sub for a given room in O(1).
	roomSubs := make(map[string][]int, len(rooms))
	for i := range subs {
		roomSubs[subs[i].RoomID] = append(roomSubs[subs[i].RoomID], i)
	}

	roomKeys := make(map[string]roomkeystore.RoomKeyPair, len(rooms))
	for i := range rooms {
		roomKeys[rooms[i].ID] = deterministicRoomKeyPair(r)
	}

	return Fixtures{
		Users:         users,
		Rooms:         rooms,
		Subscriptions: subs,
		RoomSubs:      roomSubs,
		RoomKeys:      roomKeys,
	}
}

// deterministicRoomKeyPair builds a P-256 keypair from bytes drawn from r.
// Reads 32-byte scalars directly via NewPrivateKey instead of GenerateKey
// because the stdlib's GenerateKey calls randutil.MaybeReadByte, which draws
// 0 or 1 byte from its internal non-deterministic source and breaks
// reproducibility across calls with the same math/rand seed. The retry loop
// covers the astronomically rare case of a zero or out-of-range scalar; it
// consumes only deterministic bytes from r so the output stays a function of
// the seed alone.
func deterministicRoomKeyPair(r io.Reader) roomkeystore.RoomKeyPair {
	buf := make([]byte, 32)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			panic(fmt.Errorf("read deterministic key bytes: %w", err))
		}
		priv, err := ecdh.P256().NewPrivateKey(buf)
		if err == nil {
			return roomkeystore.RoomKeyPair{
				PublicKey:  priv.PublicKey().Bytes(),
				PrivateKey: priv.Bytes(),
			}
		}
	}
}

func pickMembers(r *rand.Rand, p *Preset, roomIdx, totalRooms int, room *model.Room, users []model.User) []model.User {
	if room.Type == model.RoomTypeDM {
		// Two distinct users.
		i := r.Intn(len(users))
		j := r.Intn(len(users) - 1)
		if j >= i {
			j++
		}
		return []model.User{users[i], users[j]}
	}
	switch p.RoomSizeDist {
	case DistMixed:
		// 10% of rooms get up to 500 members; rest get 2-20.
		size := 2 + r.Intn(19)
		if r.Intn(10) == 0 {
			size = 2 + r.Intn(499)
		}
		return sampleWithoutReplacement(r, users, size)
	default:
		// Assign each user to exactly one room via round-robin so that every
		// user appears in at least one room.
		var members []model.User
		for i := range users {
			if i%totalRooms == roomIdx {
				members = append(members, users[i])
			}
		}
		if len(members) < 2 {
			// Pad with random extras to ensure at least 2 members.
			extra := sampleWithoutReplacement(r, users, 2)
			seen := make(map[string]bool)
			for i := range members {
				seen[members[i].ID] = true
			}
			for i := range extra {
				if !seen[extra[i].ID] {
					members = append(members, extra[i])
					seen[extra[i].ID] = true
				}
				if len(members) >= 2 {
					break
				}
			}
		}
		return members
	}
}

func sampleWithoutReplacement(r *rand.Rand, users []model.User, n int) []model.User {
	if n > len(users) {
		n = len(users)
	}
	idx := r.Perm(len(users))[:n]
	out := make([]model.User, n)
	for i, k := range idx {
		out[i] = users[k]
	}
	return out
}
