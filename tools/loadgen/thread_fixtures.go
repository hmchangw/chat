package main

import (
	"math/rand"
)

// base62Alphabet mirrors the alphabet in pkg/idgen so IDs produced here pass
// idgen.IsValidMessageID — same character set, same length (20 chars).
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// messageIDLength is the canonical 20-char base62 message ID length (pkg/idgen.messageIDLength).
const messageIDLength = 20

// defaultParentsPerRoom is how many thread-parent messages BuildThreadFixtures
// mints per room when the caller does not override it. Several parents per room
// spread thread fan-out across distinct threads rather than one hot thread,
// matching realistic steady state.
const defaultParentsPerRoom = 8

// threadParent is one seeded parent message: the ID a thread reply references
// plus the subscriber that authored it (so the Cassandra row's sender is a real
// room member, mirroring production).
type threadParent struct {
	MessageID     string
	SenderID      string
	SenderAccount string
	SenderEngName string
}

// ThreadFixtures is the messages Fixtures plus the per-room thread parents the
// thread workload replies to. ParentsByRoom is keyed by room ID; every room
// that has subscriptions gets ParentsPerRoom entries.
type ThreadFixtures struct {
	Fixtures
	ParentsByRoom  map[string][]threadParent
	ParentsPerRoom int
}

// seededMessageID generates a 20-char base62 message ID from rng so that
// BuildThreadFixtures output is fully deterministic given the same seed.
// The character set matches pkg/idgen so IDs pass idgen.IsValidMessageID.
func seededMessageID(rng *rand.Rand) string {
	buf := make([]byte, messageIDLength)
	for i := range buf {
		buf[i] = base62Alphabet[rng.Intn(len(base62Alphabet))]
	}
	return string(buf)
}

// BuildThreadFixtures builds the base messages fixtures for the preset, then
// deterministically mints parentsPerRoom thread-parent messages per room, each
// authored by a random subscriber of that room. A room with no subscribers gets
// no parents. parentsPerRoom <= 0 falls back to defaultParentsPerRoom.
func BuildThreadFixtures(p *Preset, seed int64, parentsPerRoom int, siteID string) ThreadFixtures {
	if parentsPerRoom <= 0 {
		parentsPerRoom = defaultParentsPerRoom
	}
	base := BuildFixtures(p, seed, siteID)

	// Build a userID → EngName lookup so parent minting can stamp the sender's
	// full name without rescanning the Users slice on every pick.
	engNameByID := make(map[string]string, len(base.Users))
	for i := range base.Users {
		engNameByID[base.Users[i].ID] = base.Users[i].EngName
	}

	// Group subscriptions by room for O(1) author selection.
	subsByRoom := make(map[string][]int, len(base.Rooms))
	for i := range base.Subscriptions {
		rid := base.Subscriptions[i].RoomID
		subsByRoom[rid] = append(subsByRoom[rid], i)
	}

	// A dedicated RNG offset from the run seed keeps parent minting independent
	// of BuildFixtures' own RNG stream while staying reproducible.
	rng := rand.New(rand.NewSource(seed ^ 0x7e57_0001))
	parents := make(map[string][]threadParent, len(base.Rooms))
	for i := range base.Rooms {
		members := subsByRoom[base.Rooms[i].ID]
		if len(members) == 0 {
			continue
		}
		list := make([]threadParent, 0, parentsPerRoom)
		for n := 0; n < parentsPerRoom; n++ {
			sub := base.Subscriptions[members[rng.Intn(len(members))]]
			list = append(list, threadParent{
				MessageID:     seededMessageID(rng),
				SenderID:      sub.User.ID,
				SenderAccount: sub.User.Account,
				SenderEngName: engNameByID[sub.User.ID],
			})
		}
		parents[base.Rooms[i].ID] = list
	}

	return ThreadFixtures{Fixtures: base, ParentsByRoom: parents, ParentsPerRoom: parentsPerRoom}
}
