//go:build e2e

// Package harness provides the e2e stack lifecycle, per-site clients, and
// per-test resource minting.
//
// Helper pairs:
//   - Authenticate / AuthenticateE: E variant for non-test goroutines.
//   - MintEphemeralUser / MintEphemeralUserAs: -As for cross-site same-name.
//   - awaitMessageOnSite / awaitCanonicalAcked: prefer …OnSite under t.Parallel.
//   - registerRoomCleanup: use asSiteDB(t, site); bare literals skip backends.
//
// SeedRemoteUser/SeedUserRoom/SeedRoomKey bypass production replication.
package harness
