package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactEnv_Comprehensive verifies the expanded three-tier redaction
// allow-list (exact keys, suffix patterns, prefix patterns).
func TestRedactEnv_Comprehensive(t *testing.T) {
	in := map[string]string{
		// Exact key matches.
		"AUTH_TOKEN":    "secret",
		"MONGO_URI":     "mongodb://user:pass@h",
		"DATABASE_URL":  "postgres://...",
		"REDIS_URL":     "redis://...",
		"KAFKA_BROKERS": "broker1:9092",
		"API_KEY":       "apikey123",
		"NATS_CREDS":    "/path/to/creds",

		// Suffix matches.
		"MY_API_KEY":       "k",
		"USER_PRIVATE_KEY": "pk",
		"DB_PASSWORD":      "pw",
		"SERVICE_SECRET":   "svc-secret",
		"APP_TOKEN":        "app-tok",
		"CONN_DSN":         "dsn-val",
		"SOME_NKEY":        "nkey-val",
		"AUTH_JWT":         "jwt-val",

		// Prefix matches.
		"FEDERATION_PEER_A_NKEY": "nkey...",
		"JWT_SIGNING_KEY":        "jwt-secret",
		"BEARER_TOKEN":           "bt",
		"NATS_JWT_CLUSTER":       "cluster-jwt",
		"NATS_NKEY_USER":         "user-nkey",
		"PEER_SITE_B_KEY":        "site-b-key",

		// Safe pass-through values.
		"FOO":     "bar",
		"BAZ":     "qux",
		"SITE_ID": "site-local",
		"MAX_RPS": "500",
	}

	out := RedactEnv(in)

	redacted := []string{
		// Exact matches.
		"AUTH_TOKEN", "MONGO_URI", "DATABASE_URL", "REDIS_URL", "KAFKA_BROKERS",
		"API_KEY", "NATS_CREDS",
		// Suffix matches.
		"MY_API_KEY", "USER_PRIVATE_KEY", "DB_PASSWORD", "SERVICE_SECRET",
		"APP_TOKEN", "CONN_DSN", "SOME_NKEY", "AUTH_JWT",
		// Prefix matches.
		"FEDERATION_PEER_A_NKEY", "JWT_SIGNING_KEY", "BEARER_TOKEN",
		"NATS_JWT_CLUSTER", "NATS_NKEY_USER", "PEER_SITE_B_KEY",
	}
	passthrough := []string{"FOO", "BAZ", "SITE_ID", "MAX_RPS"}

	for _, k := range redacted {
		assert.Equal(t, "***", out[k], "key %s must be redacted", k)
	}
	for _, k := range passthrough {
		assert.Equal(t, in[k], out[k], "key %s must pass through unchanged", k)
	}
}

// TestRedactEnv_CaseInsensitiveKeys ensures that keys are upper-cased before
// matching so mixed-case env var names (unusual but valid) are still caught.
func TestRedactEnv_CaseInsensitiveKeys(t *testing.T) {
	in := map[string]string{
		"auth_token": "lower-secret",
		"Auth_Token": "mixed-secret",
		"AUTH_TOKEN": "upper-secret",
		"safe_var":   "safe",
	}
	out := RedactEnv(in)
	assert.Equal(t, "***", out["auth_token"])
	assert.Equal(t, "***", out["Auth_Token"])
	assert.Equal(t, "***", out["AUTH_TOKEN"])
	assert.Equal(t, "safe", out["safe_var"])
}

// TestRedactEnv_AlreadyRedacted verifies that values already set to "***" are
// preserved without any further processing (idempotent re-redaction).
func TestRedactEnv_AlreadyRedacted(t *testing.T) {
	in := map[string]string{
		"AUTH_TOKEN": "***",
		"SAFE_VAR":   "still-safe",
	}
	out := RedactEnv(in)
	assert.Equal(t, "***", out["AUTH_TOKEN"])
	assert.Equal(t, "still-safe", out["SAFE_VAR"])
}

// TestMintFixtureJWTs_CreatesPlaceholders verifies that the stub creates the
// creds directory (mode 0700) and a placeholder .jwt file per user (mode 0600).
func TestMintFixtureJWTs_CreatesPlaceholders(t *testing.T) {
	dir := t.TempDir()
	runID := "test-run-123"
	users := []string{"alice", "bob"}

	credsDir, err := MintFixtureJWTs(context.Background(), dir, runID, users)
	require.NoError(t, err)
	assert.Equal(t, CredsDir(dir, runID), credsDir)

	// Creds directory must exist with mode 0700.
	info, err := os.Stat(credsDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "creds dir must be 0700")

	// Each user must have a placeholder .jwt file with mode 0600.
	for _, user := range users {
		path := filepath.Join(credsDir, user+".jwt")
		fi, statErr := os.Stat(path)
		require.NoError(t, statErr, "missing JWT file for %s", user)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "JWT file for %s must be 0600", user)
	}
}

// TestMintFixtureJWTs_EmptyUsers verifies that calling with an empty user list
// only creates the directory (no files written, no error).
func TestMintFixtureJWTs_EmptyUsers(t *testing.T) {
	dir := t.TempDir()
	credsDir, err := MintFixtureJWTs(context.Background(), dir, "empty-run", nil)
	require.NoError(t, err)
	entries, err := os.ReadDir(credsDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestProvisionFederationNKey_CreatesPlaceholder verifies that the stub creates
// the creds directory and a placeholder .nkey file (mode 0600).
func TestProvisionFederationNKey_CreatesPlaceholder(t *testing.T) {
	dir := t.TempDir()
	runID := "fed-run-abc"
	peer := "site-a-peer"

	path, err := ProvisionFederationNKey(context.Background(), dir, runID, peer)
	require.NoError(t, err)

	expected := filepath.Join(CredsDir(dir, runID), peer+".nkey")
	assert.Equal(t, expected, path)

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), ".nkey file must be 0600")
}

// TestCleanupCredsDir_RemovesAllFiles verifies that CleanupCredsDir removes the
// entire creds subtree under the run directory.
func TestCleanupCredsDir_RemovesAllFiles(t *testing.T) {
	dir := t.TempDir()
	runID := "test-run-456"

	_, err := MintFixtureJWTs(context.Background(), dir, runID, []string{"alice", "bob"})
	require.NoError(t, err)

	require.NoError(t, CleanupCredsDir(dir, runID))

	_, err = os.Stat(filepath.Join(dir, runID, "creds"))
	assert.True(t, os.IsNotExist(err), "creds dir must not exist after cleanup")
}

// TestCleanupCredsDir_Idempotent verifies that calling CleanupCredsDir when the
// directory does not exist returns no error (os.RemoveAll is a no-op for missing paths).
func TestCleanupCredsDir_Idempotent(t *testing.T) {
	dir := t.TempDir()
	err := CleanupCredsDir(dir, "nonexistent-run")
	assert.NoError(t, err)
}

// TestCredsDir_ReturnsExpectedPath verifies the path construction helper.
func TestCredsDir_ReturnsExpectedPath(t *testing.T) {
	got := CredsDir("/var/loadgen/runs", "abc-123")
	assert.Equal(t, "/var/loadgen/runs/abc-123/creds", got)
}
