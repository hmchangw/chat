package msgcrypto

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/msgkeystore"
)

// testKey returns a deterministic 32-byte AES-256 key where key[i] = byte(i).
func testKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestEncryptor_RoundTrip(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()
	roomID := "room-1"
	key := testKey()
	content := "hello, world"

	// Encrypt: Get returns the current key.
	ks.EXPECT().Get(ctx, roomID).Return(&msgkeystore.VersionedKey{Version: 0, Key: key}, nil)
	// Decrypt: GetByVersion returns the same key.
	ks.EXPECT().GetByVersion(ctx, roomID, 0).Return(key, nil)

	encrypted, err := enc.Encrypt(ctx, roomID, content)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encrypted, "enc:v0:"), "expected enc:v0: prefix, got: %s", encrypted)

	decrypted, err := enc.Decrypt(ctx, roomID, encrypted)
	require.NoError(t, err)
	assert.Equal(t, content, decrypted)
}

func TestEncryptor_Decrypt_PlaintextPassthrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()

	// No key store calls should be made for plaintext content.
	result, err := enc.Decrypt(ctx, "room-1", "plain text message")
	require.NoError(t, err)
	assert.Equal(t, "plain text message", result)
}

func TestEncryptor_Decrypt_EmptyString(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()

	// Empty string passes through without key store calls.
	result, err := enc.Decrypt(ctx, "room-1", "")
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestEncryptor_Encrypt_PrefixFormat(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()
	roomID := "room-1"
	key := testKey()

	ks.EXPECT().Get(ctx, roomID).Return(&msgkeystore.VersionedKey{Version: 3, Key: key}, nil)

	encrypted, err := enc.Encrypt(ctx, roomID, "test content")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encrypted, "enc:v3:"), "expected enc:v3: prefix, got: %s", encrypted)

	// Verify the payload after the prefix is valid base64.
	payload := strings.TrimPrefix(encrypted, "enc:v3:")
	_, err = base64.StdEncoding.DecodeString(payload)
	require.NoError(t, err, "payload should be valid base64")
}

func TestEncryptor_Encrypt_NoKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()

	ks.EXPECT().Get(ctx, "room-1").Return(nil, nil)

	_, err := enc.Encrypt(ctx, "room-1", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no db encryption key")
}

func TestEncryptor_Encrypt_KeyStoreError(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()

	ks.EXPECT().Get(ctx, "room-1").Return(nil, errors.New("connection refused"))

	_, err := enc.Encrypt(ctx, "room-1", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching db key")
}

func TestEncryptor_Decrypt_KeyVersionNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()

	ks.EXPECT().GetByVersion(ctx, "room-1", 5).Return(nil, nil)

	_, err := enc.Decrypt(ctx, "room-1", "enc:v5:AAAA")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestEncryptor_Decrypt_MalformedCiphertext(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()

	tests := []struct {
		name        string
		input       string
		errContains string
	}{
		{
			name:        "missing version",
			input:       "enc::somedata",
			errContains: "malformed encrypted content",
		},
		{
			name:        "missing data separator",
			input:       "enc:v1",
			errContains: "malformed encrypted content",
		},
		{
			name:        "invalid version",
			input:       "enc:vXYZ:somedata",
			errContains: "invalid version",
		},
		{
			name:        "invalid base64",
			input:       "enc:v1:not-valid-base64!!!",
			errContains: "decoding base64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For invalid base64 case, we need GetByVersion to return a key
			// so we get past the key lookup to the base64 decode step.
			if tt.name == "invalid base64" {
				ks.EXPECT().GetByVersion(ctx, "room-1", 1).Return(testKey(), nil)
			}

			_, err := enc.Decrypt(ctx, "room-1", tt.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestEncryptor_Decrypt_TamperedCiphertext(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()
	roomID := "room-1"
	key := testKey()

	// Encrypt.
	ks.EXPECT().Get(ctx, roomID).Return(&msgkeystore.VersionedKey{Version: 0, Key: key}, nil)
	encrypted, err := enc.Encrypt(ctx, roomID, "secret message")
	require.NoError(t, err)

	// Tamper with the last byte of the ciphertext.
	parts := strings.SplitN(encrypted, ":", 3)
	require.Len(t, parts, 3)
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0xFF // flip bits in the last byte
	tampered := parts[0] + ":" + parts[1] + ":" + base64.StdEncoding.EncodeToString(raw)

	// Decrypt tampered content should fail.
	ks.EXPECT().GetByVersion(ctx, roomID, 0).Return(key, nil)
	_, err = enc.Decrypt(ctx, roomID, tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypting content")
}

func TestEncryptor_Decrypt_TruncatedCiphertext(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()
	key := testKey()

	// Create a base64 payload shorter than 12 bytes (nonce size).
	shortPayload := base64.StdEncoding.EncodeToString([]byte("short"))

	ks.EXPECT().GetByVersion(ctx, "room-1", 0).Return(key, nil)

	_, err := enc.Decrypt(ctx, "room-1", "enc:v0:"+shortPayload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ciphertext too short")
}

func TestEncryptor_RoundTrip_EmptyContent(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()
	roomID := "room-1"
	key := testKey()

	ks.EXPECT().Get(ctx, roomID).Return(&msgkeystore.VersionedKey{Version: 0, Key: key}, nil)
	ks.EXPECT().GetByVersion(ctx, roomID, 0).Return(key, nil)

	encrypted, err := enc.Encrypt(ctx, roomID, "")
	require.NoError(t, err)

	decrypted, err := enc.Decrypt(ctx, roomID, encrypted)
	require.NoError(t, err)
	assert.Equal(t, "", decrypted)
}

func TestEncryptor_RoundTrip_UnicodeContent(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()
	roomID := "room-1"
	key := testKey()

	unicodeContent := "Hello 🌍🎉 こんにちは 안녕하세요 Привет"

	ks.EXPECT().Get(ctx, roomID).Return(&msgkeystore.VersionedKey{Version: 0, Key: key}, nil)
	ks.EXPECT().GetByVersion(ctx, roomID, 0).Return(key, nil)

	encrypted, err := enc.Encrypt(ctx, roomID, unicodeContent)
	require.NoError(t, err)

	decrypted, err := enc.Decrypt(ctx, roomID, encrypted)
	require.NoError(t, err)
	assert.Equal(t, unicodeContent, decrypted)
}

func TestEncryptor_Decrypt_KeyStoreError(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()

	ks.EXPECT().GetByVersion(ctx, "room-1", 2).Return(nil, errors.New("connection refused"))

	_, err := enc.Decrypt(ctx, "room-1", "enc:v2:AAAA")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching db key version 2")
}

func TestEncryptor_Encrypt_NonceGenerationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	// Inject a reader that always fails.
	enc.randReader = &failReader{}
	ctx := context.Background()
	key := testKey()

	ks.EXPECT().Get(ctx, "room-1").Return(&msgkeystore.VersionedKey{Version: 0, Key: key}, nil)

	_, err := enc.Encrypt(ctx, "room-1", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generating nonce")
}

func TestEncryptor_Encrypt_UniqueNonces(t *testing.T) {
	ctrl := gomock.NewController(t)
	ks := NewMockKeyStore(ctrl)
	enc := NewEncryptor(ks)
	ctx := context.Background()
	roomID := "room-1"
	key := testKey()

	ks.EXPECT().Get(ctx, roomID).Return(&msgkeystore.VersionedKey{Version: 0, Key: key}, nil).Times(2)

	encrypted1, err := enc.Encrypt(ctx, roomID, "same content")
	require.NoError(t, err)

	encrypted2, err := enc.Encrypt(ctx, roomID, "same content")
	require.NoError(t, err)

	assert.NotEqual(t, encrypted1, encrypted2, "two encryptions of the same content must produce different ciphertexts")
}

// failReader is an io.Reader that always returns an error.
type failReader struct{}

func (f *failReader) Read(_ []byte) (int, error) {
	return 0, errors.New("injected read failure")
}
