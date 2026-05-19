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
