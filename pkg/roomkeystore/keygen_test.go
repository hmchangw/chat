package roomkeystore_test

import (
	"bytes"
	"crypto/ecdh"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

func TestGenerateKeyPair_Shape(t *testing.T) {
	pair, err := roomkeystore.GenerateKeyPair()
	require.NoError(t, err)
	assert.Len(t, pair.PublicKey, 65)
	assert.Len(t, pair.PrivateKey, 32)
}

func TestGenerateKeyPair_Distinct(t *testing.T) {
	a, err := roomkeystore.GenerateKeyPair()
	require.NoError(t, err)
	b, err := roomkeystore.GenerateKeyPair()
	require.NoError(t, err)
	assert.False(t, bytes.Equal(a.PublicKey, b.PublicKey))
	assert.False(t, bytes.Equal(a.PrivateKey, b.PrivateKey))
}

// TestGenerateKeyPair_HalvesMatch verifies that the public and private halves
// returned by GenerateKeyPair are a true P-256 key pair — deriving the public
// key from the private scalar yields the stored public key bytes.
//
// The previous version of this test exercised roomcrypto's ECIES round trip,
// which is no longer applicable: roomcrypto's HKDF-only scheme does not use
// the public key at all. The direct curve-derivation check is both stronger
// (it would catch any mismatch, not just round-trip failures) and free of
// roomcrypto coupling.
func TestGenerateKeyPair_HalvesMatch(t *testing.T) {
	pair, err := roomkeystore.GenerateKeyPair()
	require.NoError(t, err)

	priv, err := ecdh.P256().NewPrivateKey(pair.PrivateKey)
	require.NoError(t, err)

	derived := priv.PublicKey().Bytes()
	assert.Equal(t, pair.PublicKey, derived, "stored public key must equal the one derived from the private scalar")
}
