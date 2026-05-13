package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Unit tests — these test RunLock helper functions and naming conventions
// without requiring a real MongoDB.  Integration tests in integration_test.go
// exercise the full Mongo wire-protocol path via testcontainers.
// ---------------------------------------------------------------------------

// TestRunLock_ErrConcurrentRunWraps verifies the sentinel error survives wrapping.
func TestRunLock_ErrConcurrentRunWraps(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", ErrConcurrentRun)
	assert.True(t, errors.Is(wrapped, ErrConcurrentRun))
}

// TestShortRunID returns the first 7 chars of a run ID.
func TestShortRunID(t *testing.T) {
	cases := []struct {
		runID string
		want  string
	}{
		{"abcdef1234567890", "abcdef1"},
		{"abc", "abc"},
		{"", ""},
		{"1234567", "1234567"},
		{"12345678", "1234567"},
	}
	for _, tc := range cases {
		t.Run(tc.runID, func(t *testing.T) {
			assert.Equal(t, tc.want, shortRunID(tc.runID))
		})
	}
}

// TestMongoDBName verifies the loadgen_<short> naming pattern satisfies
// the guardMongoDB prefix invariant.
func TestMongoDBName(t *testing.T) {
	cases := []struct {
		runID string
		want  string
	}{
		{"abcdef1234567890", "loadgen_abcdef1"},
		{"abc", "loadgen_abc"},
		{"", "loadgen_"},
	}
	for _, tc := range cases {
		t.Run(tc.runID, func(t *testing.T) {
			got := mongoDBName(tc.runID)
			assert.Equal(t, tc.want, got)
			assert.True(t, strings.HasPrefix(got, "loadgen_"),
				"db name must carry loadgen prefix for guardMongoDB to accept it")
		})
	}
}

// TestConsumerName verifies the loadgen_<short>_<purpose> naming pattern.
func TestConsumerName(t *testing.T) {
	cases := []struct {
		runID   string
		purpose string
		want    string
	}{
		{"abcdef1234567890", "message-worker", "loadgen_abcdef1_message-worker"},
		{"abc", "broadcast-worker", "loadgen_abc_broadcast-worker"},
		{"1234567", "notification-worker", "loadgen_1234567_notification-worker"},
	}
	for _, tc := range cases {
		t.Run(tc.purpose, func(t *testing.T) {
			got := consumerName(tc.runID, tc.purpose)
			assert.Equal(t, tc.want, got)
			assert.True(t, strings.HasPrefix(got, "loadgen_"),
				"consumer name must start with loadgen_ prefix")
		})
	}
}
