package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestGenerateRoomKeyPair_RoundTripWithRoomcrypto(t *testing.T) {
	pair, err := generateRoomKeyPair()
	require.NoError(t, err)
	encrypted, err := roomcrypto.Encode("hello", pair.PublicKey, 0)
	require.NoError(t, err)
	assert.Len(t, encrypted.EphemeralPublicKey, 65)
	assert.Len(t, encrypted.Nonce, 12)
	assert.NotEmpty(t, encrypted.Ciphertext)
}
