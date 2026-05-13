package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// secretExactKeys: full-name matches for env vars known to embed credentials.
// Use a map for O(1) lookup. Add new entries here when new connection strings
// or fixed-name secrets are introduced.
//
//nolint:gochecknoglobals
var secretExactKeys = map[string]struct{}{
	"AUTH_TOKEN":      {},
	"NATS_CREDS_FILE": {},
	"NATS_CREDS":      {},
	"MONGO_URI":       {},
	"NATS_URL":        {},
	"DATABASE_URL":    {},
	"REDIS_URL":       {},
	"KAFKA_BROKERS":   {},
	"API_KEY":         {},
}

// secretSuffixes: suffix substring matches (upper-cased). A key whose suffix
// matches any entry is redacted. Keep entries alphabetically sorted for ease
// of review.
//
//nolint:gochecknoglobals
var secretSuffixes = []string{
	"_API_KEY",
	"_CREDS",
	"_CREDS_FILE",
	"_DSN",
	"_JWT",
	"_KEY",
	"_NKEY",
	"_PASSWORD",
	"_PRIVATE_KEY",
	"_SECRET",
	"_TOKEN",
}

// secretPrefixes: prefix substring matches (upper-cased). A key whose prefix
// matches any entry is redacted. Covers federation peer keys and JWT signing
// material that may appear under arbitrary sub-names.
//
//nolint:gochecknoglobals
var secretPrefixes = []string{
	"BEARER_",
	"FEDERATION_",
	"JWT_",
	"NATS_JWT_",
	"NATS_NKEY_",
	"PEER_",
}

// RedactEnv returns a copy of env with secret values replaced by "***".
// A value is redacted when its key (after upper-casing):
//   - matches one of the exact keys in secretExactKeys, or
//   - ends with one of the secretSuffixes, or
//   - starts with one of the secretPrefixes.
//
// All other values are passed through unchanged. The caller may pass an
// already-redacted map (e.g. from a previous RedactEnv call); "***" values
// are preserved without double-redaction.
func RedactEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		upper := strings.ToUpper(k)
		if shouldRedact(upper) {
			out[k] = "***"
		} else {
			out[k] = v
		}
	}
	return out
}

func shouldRedact(upperKey string) bool {
	if _, ok := secretExactKeys[upperKey]; ok {
		return true
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(upperKey, suffix) {
			return true
		}
	}
	for _, prefix := range secretPrefixes {
		if strings.HasPrefix(upperKey, prefix) {
			return true
		}
	}
	return false
}

// CredsDir returns the per-run credentials directory: runsDir/runID/creds.
// Mode 0700; only the loadgen process should read it.
func CredsDir(runsDir, runID string) string {
	return filepath.Join(runsDir, runID, "creds")
}

// MintFixtureJWTs is a STUB for Phase 1b.6. Once auth-service exposes an
// admin RPC for JWT signing (Phase 3.8 auth-load scenario), this will
// invoke that RPC for each fixture user and write the real signed JWT.
//
// For now it creates the directory (mode 0700) and writes empty placeholder
// JWT files (mode 0600) so the file layout is correct for downstream
// consumers and teardown logic. The placeholder content is not cryptographic
// material — it is safe to create.
//
// TODO(Phase 3.8): replace placeholder bodies with real signed JWTs from the
// auth-service admin RPC. Reference: Phase 3.8 auth-load scenario design doc.
//
// Returns the path to the creds directory.
func MintFixtureJWTs(ctx context.Context, runsDir, runID string, fixtureUsers []string) (string, error) {
	dir := CredsDir(runsDir, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create creds dir %s: %w", dir, err)
	}
	for _, user := range fixtureUsers {
		path := filepath.Join(dir, user+".jwt")
		// Placeholder content — Phase 3.8 will write the real signed JWT.
		if err := os.WriteFile(path, []byte("# placeholder JWT for "+user+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("write placeholder JWT %s: %w", path, err)
		}
	}
	slog.InfoContext(ctx, "minted placeholder JWTs",
		"dir", dir,
		"users", len(fixtureUsers),
		"note", "Phase 3.8 will replace with real signed JWTs via auth-service admin RPC")
	return dir, nil
}

// ProvisionFederationNKey is a STUB for Phase 1b.6. Phase 3.9 (federation)
// will replace this with real NKey generation + registration with both sites'
// auth-services.
//
// For now it creates the directory (mode 0700) and writes an empty placeholder
// NKey file (mode 0600). The placeholder content is not cryptographic material.
//
// TODO(Phase 3.9): replace with real NKey generation and auth-service RPC
// registration for federation peer credentials.
//
// Returns the path to the written .nkey file.
func ProvisionFederationNKey(ctx context.Context, runsDir, runID, peerName string) (string, error) {
	dir := CredsDir(runsDir, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create creds dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, peerName+".nkey")
	if err := os.WriteFile(path, []byte("# placeholder NKey for "+peerName+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write placeholder NKey %s: %w", path, err)
	}
	slog.InfoContext(ctx, "provisioned placeholder federation NKey",
		"path", path,
		"note", "Phase 3.9 will replace with real NKey via auth-service admin RPC")
	return path, nil
}

// CleanupCredsDir removes the per-run creds directory. Called from teardown.
// It is safe to call when the directory does not exist (no-op).
func CleanupCredsDir(runsDir, runID string) error {
	dir := CredsDir(runsDir, runID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove creds dir %s: %w", dir, err)
	}
	return nil
}
