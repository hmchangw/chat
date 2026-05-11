//go:build e2e

package e2e

import (
	"testing"
)

// TestEncryption_OnSmoke exercises the ENCRYPTION_ENABLED=true code path
// in broadcast-worker.
//
// SKIPPED for now: the current stack has all broadcast-workers running
// with ENCRYPTION_ENABLED=false. A meaningful encryption-on smoke needs:
//
//  1. A SEPARATE broadcast-worker-{a,b} instance running with
//     ENCRYPTION_ENABLED=true OR a per-test env toggle on the worker
//     (would need a worker restart).
//  2. A room key pre-seeded in valkey (via room-key-sender or direct
//     PUT). Requires understanding the key format in pkg/roomcrypto +
//     pkg/roomkeystore.
//  3. Assertion that the broadcast RoomEvent carries `EncryptedMessage`
//     (json.RawMessage) instead of `Message *ClientMessage`.
//
// The skipped test exists so the gap is enumerated rather than silently
// missing from the suite. Implementing it cleanly is a follow-up plan in
// the ~100-150 line range plus a new harness helper for room-key seeding.
//
// Without this coverage, broadcast-worker handler.go lines ~102-129 (the
// encrypt branch) are dead weight in the e2e suite, despite being the
// only path that exercises valkey + roomkeystore in production.
func TestEncryption_OnSmoke(t *testing.T) {
	t.Skip("encryption-on smoke deferred to follow-up plan; needs room-key seeding harness helper")
}
