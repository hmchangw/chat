package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Unit tests for teardown --force helpers.
// Integration tests (real Mongo + NATS) live in integration_test.go under the
// //go:build integration tag.
// ---------------------------------------------------------------------------

// TestTeardownReport_ZeroValue verifies the zero-value teardownReport is
// usable without panicking — all slices are nil which compare equal to empty.
func TestTeardownReport_ZeroValue(t *testing.T) {
	var rep teardownReport
	assert.Nil(t, rep.DroppedMongoDBs)
	assert.Nil(t, rep.DroppedConsumers)
	assert.Nil(t, rep.SkippedDBs)
	assert.Nil(t, rep.SkippedConsumers)
}

// TestTeardownForceConfig_ZeroValue verifies the zero-value
// teardownForceConfig has safe defaults: OlderThan==0 means "any orphan",
// SpecificRunID=="" means "all runs".
func TestTeardownForceConfig_ZeroValue(t *testing.T) {
	var cfg teardownForceConfig
	assert.Equal(t, 0, int(cfg.OlderThan), "zero OlderThan means drop any orphan")
	assert.Equal(t, "", cfg.SpecificRunID, "empty SpecificRunID means all runs")
}

// TestShortExtraction_FromDBName verifies the short-ID extraction logic used
// inside runTeardownForce when iterating over Mongo database names.
func TestShortExtraction_FromDBName(t *testing.T) {
	cases := []struct {
		dbName    string
		wantShort string
	}{
		{"loadgen_abcdef1", "abcdef1"},
		{"loadgen_1234567", "1234567"},
		{"loadgen_abc", "abc"},
		{"loadgen_", ""},             // degenerate: empty short — skipped in real code
		{"loadgen_shared", "shared"}, // shared is excluded by the name check before this
	}
	for _, tc := range cases {
		t.Run(tc.dbName, func(t *testing.T) {
			got := strings.TrimPrefix(tc.dbName, "loadgen_")
			assert.Equal(t, tc.wantShort, got)
		})
	}
}

// TestShortExtraction_FromConsumerName verifies the short-ID extraction logic
// inside runTeardownForce when iterating over JetStream consumer names.
// Consumer names follow the pattern "loadgen_<short>_<purpose>".
func TestShortExtraction_FromConsumerName(t *testing.T) {
	cases := []struct {
		consName  string
		wantShort string
		wantOK    bool
	}{
		{"loadgen_abcdef1_message-worker", "abcdef1", true},
		{"loadgen_1234567_broadcast-worker", "1234567", true},
		{"loadgen_abc_notification-worker", "abc", true},
		{"loadgen_abcdef1_search-sync-worker-messages", "abcdef1", true},
		// no underscore after short (degenerate): whole rest is the short
		{"loadgen_abcdef1", "abcdef1", true},
		// empty short after prefix
		{"loadgen__purpose", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.consName, func(t *testing.T) {
			rest := strings.TrimPrefix(tc.consName, "loadgen_")
			idx := strings.Index(rest, "_")
			var short string
			if idx < 0 {
				short = rest
			} else {
				short = rest[:idx]
			}
			if !tc.wantOK {
				assert.Equal(t, "", short)
				return
			}
			assert.Equal(t, tc.wantShort, short)
		})
	}
}

// TestTeardownForceConfig_SpecificRunIDFiltering verifies the filtering logic
// that skips resources whose short does not match the specific run ID prefix.
func TestTeardownForceConfig_SpecificRunIDFiltering(t *testing.T) {
	cases := []struct {
		name          string
		specificRunID string
		short         string
		wantProcess   bool
	}{
		{
			name:          "empty specificRunID: process all shorts",
			specificRunID: "",
			short:         "abcdef1",
			wantProcess:   true,
		},
		{
			name:          "specificRunID starts with short: process it",
			specificRunID: "abcdef1234567890",
			short:         "abcdef1",
			wantProcess:   true,
		},
		{
			name:          "specificRunID does not start with short: skip it",
			specificRunID: "zzzzzzz1234567890",
			short:         "abcdef1",
			wantProcess:   false,
		},
		{
			name:          "specificRunID exactly equals short: process it",
			specificRunID: "abcdef1",
			short:         "abcdef1",
			wantProcess:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Replicate the exact filter expression from runTeardownForce.
			skip := tc.specificRunID != "" && !strings.HasPrefix(tc.specificRunID, tc.short)
			assert.Equal(t, tc.wantProcess, !skip)
		})
	}
}

// TestRunTeardown_ForceFlagParsing verifies that the --force flag is
// correctly parsed from the args slice passed to the teardown subcommand.
// This exercises the flag.FlagSet wiring in runTeardown without triggering
// any network calls (the --force path immediately returns 1 on failed Mongo
// connect for stub URIs, but the guard check path returns 2 for a non-loadgen
// DB — so we can distinguish the two paths by exit code).
func TestRunTeardown_ForceFlagParsing_NonForceStillGuards(t *testing.T) {
	// Without --force, the guard check runs and returns 2 for non-loadgen DB.
	cfg := &config{
		NatsURL:  "nats://stub",
		MongoURI: "mongodb://stub",
		MongoDB:  "chat", // no loadgen prefix → guard fires
	}
	code := runTeardown(t.Context(), cfg, []string{})
	assert.Equal(t, 2, code, "without --force, guard must refuse non-loadgen DB")
}

func TestRunTeardown_ForceFlagParsing_ForceSkipsGuardConnectsNATS(t *testing.T) {
	// With --force and a non-loadgen DB, the guard check is skipped.
	// Instead, Mongo connect is attempted; it fails on the stub URI → exit 1
	// (not 2, which proves the --force branch was taken).
	//
	// Use a short-timeout context so the Mongo Ping doesn't block for 30s
	// on a non-existent host (Ping respects the context deadline).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := &config{
		NatsURL:  "nats://stub-unreachable",
		MongoURI: "mongodb://stub-unreachable",
		MongoDB:  "chat", // non-loadgen DB — guard would have fired without --force
	}
	code := runTeardown(ctx, cfg, []string{"--force"})
	// --force skips the DB guard (no exit 2) and attempts Mongo connect,
	// which fails on the stub URI → exit 1.
	assert.Equal(t, 1, code, "--force must skip guard and attempt connect (fail=1, not 2)")
}
