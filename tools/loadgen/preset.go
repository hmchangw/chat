package main

import (
	"fmt"
	"io"
	"math/rand"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

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

// DailyBands describes how many rooms of each size band a typical user
// belongs to in the daily-IM presets. Zero means the preset is not a
// daily-IM preset and BuildFixtures falls back to the legacy distribution.
type DailyBands struct {
	DMs    int // 2-member rooms
	Small  int // 5-20 members
	Medium int // 50-200 members
	Large  int // 500-2000 members
}

// IsZero reports whether bands are absent.
func (b DailyBands) IsZero() bool {
	return b.DMs == 0 && b.Small == 0 && b.Medium == 0 && b.Large == 0
}

// RoomsPerUser is the sum of all bands.
func (b DailyBands) RoomsPerUser() int { return b.DMs + b.Small + b.Medium + b.Large }

// Preset is a named, fully deterministic workload specification.
type Preset struct {
	Name         string
	Users        int
	Rooms        int
	RoomSizeDist Distribution
	SenderDist   Distribution
	ContentBytes Range
	MentionRate  float64
	ThreadRate   float64
	DailyBands   DailyBands
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
	},
	"daily-light": {
		Name: "daily-light", Users: 10000,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.05, ThreadRate: 0.30,
		DailyBands: DailyBands{DMs: 15, Small: 10, Medium: 5, Large: 2},
	},
	"daily-heavy": {
		Name: "daily-heavy", Users: 10000,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.05, ThreadRate: 0.30,
		DailyBands: DailyBands{DMs: 25, Small: 20, Medium: 8, Large: 3},
	},
	"daily-power": {
		Name: "daily-power", Users: 10000,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.05, ThreadRate: 0.30,
		DailyBands: DailyBands{DMs: 40, Small: 30, Medium: 10, Large: 3},
	},
}

// BuiltinPreset looks up a preset by name.
func BuiltinPreset(name string) (Preset, bool) {
	p, ok := builtinPresets[name]
	return p, ok
}

// Fixtures is the full seed data for a preset run. RoomKeys are load-test
// fixtures derived deterministically from `seed`, not real secrets.
type Fixtures struct {
	Users         []model.User
	Rooms         []model.Room
	Subscriptions []model.Subscription
	RoomKeys      map[string]roomkeystore.RoomKeyPair
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

	if !p.DailyBands.IsZero() {
		return buildBandedFixtures(p, r, users, siteID, now)
	}

	rooms := make([]model.Room, p.Rooms)
	// realistic: last 10% of rooms are DMs
	dmStart := p.Rooms
	if p.RoomSizeDist == DistMixed {
		dmStart = p.Rooms - p.Rooms/10
	}
	for i := 0; i < p.Rooms; i++ {
		rtype := model.RoomTypeChannel
		if i >= dmStart {
			rtype = model.RoomTypeDM
		}
		rooms[i] = model.Room{
			ID:        fmt.Sprintf("room-%06d", i),
			Name:      fmt.Sprintf("room-%d", i),
			Type:      rtype,
			SiteID:    siteID,
			UserCount: 0, // filled after membership
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	var subs []model.Subscription
	for i := range rooms {
		members := pickMembers(r, p, i, p.Rooms, &rooms[i], users)
		rooms[i].UserCount = len(members)
		for j := range members {
			subs = append(subs, model.Subscription{
				ID:       fmt.Sprintf("sub-%s-%s", rooms[i].ID, members[j].ID),
				User:     model.SubscriptionUser{ID: members[j].ID, Account: members[j].Account},
				RoomID:   rooms[i].ID,
				SiteID:   siteID,
				Roles:    []model.Role{model.RoleMember},
				JoinedAt: now,
			})
		}
	}
	roomKeys := make(map[string]roomkeystore.RoomKeyPair, len(rooms))
	for i := range rooms {
		roomKeys[rooms[i].ID] = deterministicRoomKeyPair(r)
	}
	return Fixtures{Users: users, Rooms: rooms, Subscriptions: subs, RoomKeys: roomKeys}
}

// buildBandedFixtures generates rooms and subscriptions for a daily-IM
// preset where each user belongs to a fixed mix of DM/small/medium/large
// rooms per p.DailyBands. Rooms are pre-allocated band-by-band, then users
// are assigned rooms within each band round-robin so every user gets the
// configured per-band count and rooms stay within their band's size range.
func buildBandedFixtures(p *Preset, r *rand.Rand, users []model.User, siteID string, now time.Time) Fixtures {
	bands := p.DailyBands
	totalUsers := len(users)

	// Number of rooms per band, derived from per-user counts and band size targets.
	// Aim for the *average* band size to consume the per-user demand exactly.
	// Floor each band at `perUser` rooms so every user can find that many
	// distinct rooms in the band (otherwise the per-user count is unreachable).
	nDM := (totalUsers * bands.DMs) / 2 // each DM has 2 members
	nSmall := (totalUsers*bands.Small + 9) / 10
	nMed := (totalUsers*bands.Medium + 99) / 100
	nLarge := (totalUsers*bands.Large + 999) / 1000
	if nDM < bands.DMs {
		nDM = bands.DMs
	}
	if nSmall < bands.Small {
		nSmall = bands.Small
	}
	if nMed < bands.Medium {
		nMed = bands.Medium
	}
	if nLarge < bands.Large {
		nLarge = bands.Large
	}

	type bandSpec struct {
		name     string
		count    int
		sizeMin  int
		sizeMax  int
		roomType model.RoomType
		perUser  int
	}
	specs := []bandSpec{
		{"dm", nDM, 2, 2, model.RoomTypeDM, bands.DMs},
		{"small", nSmall, 5, 20, model.RoomTypeChannel, bands.Small},
		{"medium", nMed, 50, 200, model.RoomTypeChannel, bands.Medium},
		{"large", nLarge, 500, 2000, model.RoomTypeChannel, bands.Large},
	}

	var rooms []model.Room
	var subs []model.Subscription
	roomKeys := make(map[string]roomkeystore.RoomKeyPair)

	for _, spec := range specs {
		// Pre-create rooms in this band.
		bandRooms := make([]model.Room, spec.count)
		bandSizes := make([]int, spec.count)
		for i := 0; i < spec.count; i++ {
			id := fmt.Sprintf("room-%s-%06d", spec.name, i)
			size := spec.sizeMin
			if spec.sizeMax > spec.sizeMin {
				size = spec.sizeMin + r.Intn(spec.sizeMax-spec.sizeMin+1)
			}
			bandRooms[i] = model.Room{
				ID: id, Name: id, Type: spec.roomType, SiteID: siteID,
				CreatedAt: now, UpdatedAt: now,
			}
			bandSizes[i] = size
		}

		if spec.name == "dm" {
			// DM band: stub-pairing (configuration model). Each user
			// contributes spec.perUser stubs; shuffle the stub list and
			// pair consecutive stubs into DM rooms. This produces a
			// guaranteed perUser-regular bipartite graph in O(N×perUser)
			// instead of the O(N×perUser×R) weighted picker used by the
			// other bands (which would be quadratic in N here since
			// R = N×perUser/2 for DMs).
			stubs := make([]int, 0, totalUsers*spec.perUser)
			for ui := range users {
				for k := 0; k < spec.perUser; k++ {
					stubs = append(stubs, ui)
				}
			}
			r.Shuffle(len(stubs), func(a, b int) { stubs[a], stubs[b] = stubs[b], stubs[a] })
			if len(stubs)%2 != 0 {
				stubs = stubs[:len(stubs)-1] // drop one stub on odd totals (one user loses 1 DM)
			}
			// Self-loop fix: if a pair lands on the same user, swap the
			// second stub with a later position whose neighbours don't
			// create a new self-loop. Self-loops at random shuffle are
			// rare (~perUser expected over the whole stub list), so total
			// fix work is O(perUser).
			for k := 0; k+1 < len(stubs); k += 2 {
				if stubs[k] != stubs[k+1] {
					continue
				}
				x := stubs[k]
				for j := k + 2; j < len(stubs); j++ {
					partner := j ^ 1 // sibling in pair
					if stubs[j] != x && stubs[partner] != x {
						stubs[k+1], stubs[j] = stubs[j], stubs[k+1]
						break
					}
				}
				// If no swap target was found (vanishingly rare; would
				// require all remaining stubs to be `x`, impossible since
				// each user contributes only perUser stubs), the self-loop
				// remains and that DM has 1 distinct member instead of 2.
				// We still emit it; the test at N≥2 is satisfied.
			}

			// Emit subscriptions from each pair. Truncate bandRooms to the
			// actual pair count (rare divergence only at extreme small N).
			nActualDM := len(stubs) / 2
			if nActualDM < len(bandRooms) {
				bandRooms = bandRooms[:nActualDM]
				bandSizes = bandSizes[:nActualDM]
			}
			for k := 0; k < nActualDM; k++ {
				roomID := bandRooms[k].ID
				uA := &users[stubs[2*k]]
				uB := &users[stubs[2*k+1]]
				subs = append(subs, model.Subscription{
					ID:     fmt.Sprintf("sub-%s-%s", roomID, uA.ID),
					User:   model.SubscriptionUser{ID: uA.ID, Account: uA.Account},
					RoomID: roomID, SiteID: siteID,
					Roles:    []model.Role{model.RoleMember},
					JoinedAt: now,
				})
				if uA.ID != uB.ID { // skip duplicate sub on unfixable self-loop
					subs = append(subs, model.Subscription{
						ID:     fmt.Sprintf("sub-%s-%s", roomID, uB.ID),
						User:   model.SubscriptionUser{ID: uB.ID, Account: uB.Account},
						RoomID: roomID, SiteID: siteID,
						Roles:    []model.Role{model.RoleMember},
						JoinedAt: now,
					})
				}
			}

			// Finalise UserCount + keys and emit rooms.
			for i := range bandRooms {
				bandRooms[i].UserCount = bandSizes[i]
				roomKeys[bandRooms[i].ID] = deterministicRoomKeyPair(r)
			}
			rooms = append(rooms, bandRooms...)
			continue
		}

		// Assign memberships: for each user, pick `spec.perUser` distinct
		// rooms from this band, weighted by remaining capacity. If the
		// initially-randomised sizes can't satisfy demand at the tail
		// (because earlier users consumed the most-capacity rooms), we
		// expand a deficit room within sizeMax until the user is full.
		// This guarantees every user gets exactly `perUser` memberships
		// while keeping room sizes within their band range whenever
		// feasible.
		capacity := make([]int, len(bandRooms))
		memberCounts := make([]int, len(bandRooms))
		copy(capacity, bandSizes)

		for ui := range users {
			u := &users[ui]
			// Distinct room indices already picked for this user.
			picked := make(map[int]bool, spec.perUser)
			for k := 0; k < spec.perUser; k++ {
				// Build a weighted pool: indices with capacity>0 not yet picked.
				totalCap := 0
				for i, c := range capacity {
					if c > 0 && !picked[i] {
						totalCap += c
					}
				}
				if totalCap == 0 {
					// Every room is either full or already picked for this user.
					// Expand a not-yet-picked room within sizeMax to make room.
					grew := false
					// Walk indices deterministically so seed reproducibility holds.
					base := r.Intn(len(bandRooms))
					for off := 0; off < len(bandRooms); off++ {
						i := (base + off) % len(bandRooms)
						if !picked[i] && bandSizes[i] < spec.sizeMax {
							bandSizes[i]++
							capacity[i]++
							grew = true
							break
						}
					}
					if !grew {
						// Hard infeasibility (e.g. nRooms<perUser): skip the rest
						// of this user's quota. Should not happen with the floors
						// applied above.
						break
					}
					totalCap = 0
					for i, c := range capacity {
						if c > 0 && !picked[i] {
							totalCap += c
						}
					}
				}
				// Weighted pick.
				draw := r.Intn(totalCap)
				acc := 0
				chosen := -1
				for i, c := range capacity {
					if c == 0 || picked[i] {
						continue
					}
					acc += c
					if draw < acc {
						chosen = i
						break
					}
				}
				if chosen < 0 {
					break
				}
				picked[chosen] = true
				capacity[chosen]--
				memberCounts[chosen]++
				roomID := bandRooms[chosen].ID
				subs = append(subs, model.Subscription{
					ID:     fmt.Sprintf("sub-%s-%s", roomID, u.ID),
					User:   model.SubscriptionUser{ID: u.ID, Account: u.Account},
					RoomID: roomID, SiteID: siteID,
					Roles:    []model.Role{model.RoleMember},
					JoinedAt: now,
				})
			}
		}

		// Finalise UserCount and emit rooms + keys. UserCount records the
		// band's *target* size (what the room would look like in production)
		// rather than the count of test-pool subscriptions — large rooms have
		// hundreds-to-thousands of members in reality, while our test
		// population is a small sampled subset.
		//
		// Known limitation: large-band rooms will have UserCount > 500
		// (message-gatekeeper's default LargeRoomThreshold), which blocks
		// non-thread sends from member-role users. The daily-IM scenario
		// works around this by funneling sends to smaller rooms; large-band
		// rooms are exercised primarily for fan-out via receive-side
		// subscriptions.
		_ = memberCounts // counts available for future tuning; keep computed for clarity
		for i := range bandRooms {
			bandRooms[i].UserCount = bandSizes[i]
			roomKeys[bandRooms[i].ID] = deterministicRoomKeyPair(r)
		}
		rooms = append(rooms, bandRooms...)
	}

	return Fixtures{Users: users, Rooms: rooms, Subscriptions: subs, RoomKeys: roomKeys}
}

// deterministicRoomKeyPair generates a 32-byte room secret from bytes drawn
// from r. The secret is used directly as an AES-256-GCM key by roomcrypto; no
// key derivation step is needed. The name retains "KeyPair" for call-site compatibility.
func deterministicRoomKeyPair(r io.Reader) roomkeystore.RoomKeyPair {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(r, buf); err != nil {
		panic(fmt.Errorf("read deterministic key bytes: %w", err))
	}
	return roomkeystore.RoomKeyPair{PrivateKey: buf}
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
