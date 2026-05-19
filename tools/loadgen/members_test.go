package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseShape(t *testing.T) {
	cases := []struct {
		in   string
		want Shape
		err  bool
	}{
		{"users", ShapeUsers, false},
		{"orgs", ShapeOrgs, false},
		{"channels", ShapeChannels, false},
		{"mixed", ShapeMixed, false},
		{"", "", true},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseShape(tc.in)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateInjectShape(t *testing.T) {
	// v1 supports shape=users only. Other shapes are reserved values rejected
	// at validation time. Canonical+channels remains explicitly rejected with
	// a distinct message so the spec's "explicit error" guidance still applies
	// once shapes are widened in v2.
	cases := []struct {
		inject InjectMode
		shape  Shape
		errSub string // empty -> expect no error
	}{
		{InjectFrontdoor, ShapeUsers, ""},
		{InjectCanonical, ShapeUsers, ""},
		{InjectFrontdoor, ShapeOrgs, "shape=orgs not supported in v1"},
		{InjectFrontdoor, ShapeChannels, "shape=channels not supported in v1"},
		{InjectFrontdoor, ShapeMixed, "shape=mixed not supported in v1"},
		{InjectCanonical, ShapeChannels, "incompatible with --inject=canonical"},
	}
	for _, tc := range cases {
		t.Run(string(tc.inject)+"/"+string(tc.shape), func(t *testing.T) {
			err := ValidateInjectShape(tc.inject, tc.shape)
			if tc.errSub == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errSub)
		})
	}
}
