package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomcrypto"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// hkdfInfo is the HKDF info string used by the Encoder (HKDF-only scheme).
const hkdfInfo = "room-message-encryption-v2"

func testRoomKey(t *testing.T) *roomkeystore.VersionedKeyPair {
	t.Helper()
	buf := make([]byte, 32)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return &roomkeystore.VersionedKeyPair{
		Version: 3,
		KeyPair: roomkeystore.RoomKeyPair{
			PrivateKey: buf,
		},
	}
}

func decryptForTest(env *roomcrypto.EncryptedMessage, roomPrivateKey []byte) (string, error) {
	// New HKDF-only scheme: derive AES key directly from the room private key.
	aesKey := make([]byte, 32)
	hkdfReader := hkdf.New(sha256.New, roomPrivateKey, nil, []byte(hkdfInfo))
	if _, err := io.ReadFull(hkdfReader, aesKey); err != nil {
		return "", fmt.Errorf("hkdf: %w", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("gcm open: %w", err)
	}
	return string(plaintext), nil
}

func decryptClientMessage(t *testing.T, data []byte, key *roomkeystore.VersionedKeyPair) (model.RoomEvent, *model.ClientMessage) {
	t.Helper()
	var evt model.RoomEvent
	require.NoError(t, json.Unmarshal(data, &evt))
	require.Nil(t, evt.Message, "Message must be nil when EncryptedMessage is set")
	require.NotEmpty(t, evt.EncryptedMessage, "EncryptedMessage must be populated")
	var env roomcrypto.EncryptedMessage
	require.NoError(t, json.Unmarshal(evt.EncryptedMessage, &env))
	require.Equal(t, key.Version, env.Version, "EncryptedMessage.Version must match the key version")
	plaintext, err := decryptForTest(&env, key.KeyPair.PrivateKey)
	require.NoError(t, err)
	var msg model.ClientMessage
	require.NoError(t, json.Unmarshal([]byte(plaintext), &msg))
	return evt, &msg
}
