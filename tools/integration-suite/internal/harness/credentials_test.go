package harness

import (
	"strings"
	"testing"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateUserNkey_ProducesValidNkeyPair(t *testing.T) {
	pub, seed, err := GenerateUserNkey()
	require.NoError(t, err)

	// Public key starts with 'U' (user) per nkey convention.
	assert.True(t, strings.HasPrefix(pub, "U"), "public key should start with U, got %q", pub)
	assert.True(t, nkeys.IsValidPublicUserKey(pub), "public key not recognised by nkeys")

	// Seed starts with 'SU' (user seed).
	assert.True(t, strings.HasPrefix(seed, "SU"), "seed should start with SU, got %q", seed)

	// Round-trip: from the seed we can reconstruct a keypair that yields the same public key.
	kp, err := nkeys.FromSeed([]byte(seed))
	require.NoError(t, err)
	roundTripped, err := kp.PublicKey()
	require.NoError(t, err)
	assert.Equal(t, pub, roundTripped)
}

func TestGenerateUserNkey_UniqueAcrossCalls(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pub, _, err := GenerateUserNkey()
		require.NoError(t, err)
		assert.False(t, seen[pub], "duplicate public key on iteration %d", i)
		seen[pub] = true
	}
}
