//go:build e2e

package e2e

import (
	"testing"
)

// TestEncryption_OnSmoke exercises the ENCRYPTION_ENABLED=true code path
// in broadcast-worker (handler.go:102-128 — fetch room key, ECDH+HKDF+AES-GCM
// via pkg/roomcrypto, swap evt.Message for evt.EncryptedMessage on the wire).
//
// SKIPPED — Item 2 of the R3 follow-up plan. Re-assessed in the R3 reviewer
// pass; the architectural block is in broadcast-worker/main.go:119, where the
// JetStream durable consumer name is hard-coded to "broadcast-worker". Two
// broadcast-workers attached to the same MESSAGES_CANONICAL stream with the
// same durable form a single competing queue group, so they split each
// broadcast 50/50 -- meaning the encryption-on variant would receive only
// half the messages, and the OTHER half would skip encryption entirely.
//
// Options considered (and why they were not pursued in this PR):
//
//  1. Add a second broadcast-worker-a-enc container alongside the existing
//     one. BLOCKED by the shared durable: doesn't work without a code change.
//  2. Make the durable name configurable via env var. PRODUCTION CODE CHANGE
//     to broadcast-worker/main.go to read DURABLE_NAME — small but invasive
//     enough to merit its own PR + ops review (durable rename has migration
//     implications on a live cluster).
//  3. Toggle the existing broadcast-worker-a to ENCRYPTION_ENABLED=true
//     just for this test via docker compose stop/start with env override.
//     Docker requires container RECREATE (not restart) for env changes, and
//     all the OTHER e2e tests would then see encrypted broadcasts -- their
//     assertions (request_id_test, message_send_test) decode plaintext
//     evt.Message and would silently break.
//  4. A separate end-to-end test that uses a per-test channel-room + adds
//     ENCRYPTION_ENABLED-aware decryption to those existing assertions.
//     Largest scope (~1 day of test refactoring); deferred.
//
// What IS covered today: pkg/roomcrypto/integration_test.go,
// pkg/roomkeystore/integration_test.go, and
// broadcast-worker/handler_test.go all unit-test the crypto path with real
// keys + Valkey via testcontainers. The MISSING coverage is the e2e wire
// assertion that a *production-shaped* RoomEvent emerges with
// EncryptedMessage populated and Message nilled out -- which only matters
// once option (2) above unblocks a separate broadcast-worker-a-enc.
//
// All the test infrastructure to seed the room key in Valkey is now
// available: pkg/roomkeystore.NewValkeyStore + .Set() takes a P-256
// RoomKeyPair. The harness's existing redis.Client import (from
// FlushSearchRestrictedCache) means a seedRoomKey helper is ~15 lines.
// When (2) lands, this test fills in.
func TestEncryption_OnSmoke(t *testing.T) {
	t.Skip("encryption-on smoke blocked by hard-coded broadcast-worker durable name; " +
		"see file-level comment for the 4 options considered and the deferral rationale")
}
