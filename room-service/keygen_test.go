package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"

	"github.com/hmchangw/chat/pkg/roomcrypto"
)

func TestGenerateRoomKeyPair_Shape(t *testing.T) {
	pair, err := generateRoomKeyPair()
	require.NoError(t, err)
	assert.Len(t, pair.PublicKey, 65)
	assert.Len(t, pair.PrivateKey, 32)
}

func TestGenerateRoomKeyPair_Distinct(t *testing.T) {
	a, err := generateRoomKeyPair()
	require.NoError(t, err)
	b, err := generateRoomKeyPair()
	require.NoError(t, err)
	assert.False(t, bytes.Equal(a.PublicKey, b.PublicKey))
	assert.False(t, bytes.Equal(a.PrivateKey, b.PrivateKey))
}

// TestGenerateRoomKeyPair_RoundTripWithRoomcrypto exercises the full
// encrypt-then-decrypt path so a generator returning mismatched public/private
// halves would actually fail the test (just asserting the encoded shape did
// not). The decrypt routine mirrors roomcrypto.Encode's ECDH+HKDF+AES-GCM
// construction inverted — kept here in test code because the production
// roomcrypto package is encode-only (clients decrypt).
func TestGenerateRoomKeyPair_RoundTripWithRoomcrypto(t *testing.T) {
	pair, err := generateRoomKeyPair()
	require.NoError(t, err)

	const plaintext = "hello"
	encrypted, err := roomcrypto.Encode(plaintext, pair.PublicKey, 0)
	require.NoError(t, err)

	got := decryptForTest(t, encrypted, pair.PrivateKey)
	assert.Equal(t, plaintext, got, "round-trip must succeed when private and public halves match")
}

func decryptForTest(t *testing.T, em *roomcrypto.EncryptedMessage, roomPriv []byte) string {
	t.Helper()
	priv, err := ecdh.P256().NewPrivateKey(roomPriv)
	require.NoError(t, err)
	ephPub, err := ecdh.P256().NewPublicKey(em.EphemeralPublicKey)
	require.NoError(t, err)
	shared, err := priv.ECDH(ephPub)
	require.NoError(t, err)

	aesKey := make([]byte, 32)
	_, err = io.ReadFull(hkdf.New(sha256.New, shared, nil, []byte("room-message-encryption")), aesKey)
	require.NoError(t, err)

	block, err := aes.NewCipher(aesKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	plain, err := gcm.Open(nil, em.Nonce, em.Ciphertext, nil)
	require.NoError(t, err)
	return string(plain)
}
