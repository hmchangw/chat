package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// TestSharedLockDBName_NotPerRun verifies SharedLockDBName is a static,
// non-per-run database name that satisfies the loadgen_ prefix guard.
func TestSharedLockDBName_NotPerRun(t *testing.T) {
	// Must be a static string, not derived from a runID.
	assert.Equal(t, "loadgen_shared", SharedLockDBName)
	// Must satisfy the loadgen_ prefix guard.
	assert.True(t, strings.HasPrefix(SharedLockDBName, "loadgen_"))
	// Must NOT collide with mongoDBName for any plausible runID.
	for _, id := range []string{"abcd123", "ffffeee", "0000000"} {
		assert.NotEqual(t, mongoDBName(id), SharedLockDBName)
	}
}

// TestConcurrentRunError_NamesExistingRun verifies ConcurrentRunError carries
// conflict details and satisfies errors.Is(ErrConcurrentRun).
func TestConcurrentRunError_NamesExistingRun(t *testing.T) {
	e := &ConcurrentRunError{
		ExistingRunID:     "run-A",
		ExistingHost:      "host-1",
		ExistingScenario:  "history-read",
		ExistingStartedAt: time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		SUTURL:            "nats://example:4222",
	}
	s := e.Error()
	assert.Contains(t, s, "run-A")
	assert.Contains(t, s, "host-1")
	assert.Contains(t, s, "history-read")
	assert.Contains(t, s, "nats://example:4222")
	assert.True(t, errors.Is(e, ErrConcurrentRun), "must satisfy errors.Is(ErrConcurrentRun)")
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
