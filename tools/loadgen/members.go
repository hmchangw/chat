package main

import "fmt"

// Shape selects what an add-member request carries: individual users, orgs,
// channel-refs, or a mix. v1 implements ShapeUsers; the other values exist so
// the flag is forward-compatible.
type Shape string

const (
	ShapeUsers    Shape = "users"
	ShapeOrgs     Shape = "orgs"
	ShapeChannels Shape = "channels"
	ShapeMixed    Shape = "mixed"
)

// ParseShape converts a CLI flag value to a Shape.
func ParseShape(s string) (Shape, error) {
	switch Shape(s) {
	case ShapeUsers, ShapeOrgs, ShapeChannels, ShapeMixed:
		return Shape(s), nil
	default:
		return "", fmt.Errorf("unknown shape %q (want users|orgs|channels|mixed)", s)
	}
}

// MembersPreset is a fully-deterministic spec for the members workload.
type MembersPreset struct {
	Name          string
	Users         int // global user pool
	Rooms         int // rooms to seed
	BaselineSize  int // members per room at seed time (incl. owner)
	CandidatePool int // unused-but-eligible users tagged per room
}

var builtinMembersPresets = map[string]MembersPreset{
	"members-small": {
		Name: "members-small", Users: 200, Rooms: 5,
		BaselineSize: 10, CandidatePool: 50,
	},
	"members-medium": {
		Name: "members-medium", Users: 5000, Rooms: 100,
		BaselineSize: 100, CandidatePool: 500,
	},
	"members-capacity": {
		Name: "members-capacity", Users: 12000, Rooms: 5,
		BaselineSize: 1, CandidatePool: 990, // fits under MAX_ROOM_SIZE=1000
	},
}

// BuiltinMembersPreset looks up a preset by name.
func BuiltinMembersPreset(name string) (MembersPreset, bool) {
	p, ok := builtinMembersPresets[name]
	return p, ok
}

// ValidateInjectShape enforces compatibility between --inject and --shape.
// canonical+channels is explicitly rejected (room-service owns channel
// expansion); v1 also rejects everything except shape=users until the
// org/channel pre-resolution work in v2.
func ValidateInjectShape(inject InjectMode, shape Shape) error {
	if inject == InjectCanonical && shape == ShapeChannels {
		return fmt.Errorf("--shape=channels incompatible with --inject=canonical (channel expansion lives in room-service)")
	}
	if shape != ShapeUsers {
		return fmt.Errorf("--shape=%s not supported in v1 (only shape=users is implemented; see docs/superpowers/specs/2026-05-19-load-test-room-members-design.md)", shape)
	}
	return nil
}
