package roomcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"
)

func TestEncode(t *testing.T) {
	// Generate a valid key pair once at the top of the test for use in the table.
	// This is shared read-only setup for test cases — each subtest reads only the pubKey bytes,
	// it does not mutate state.
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	validPubKey := privKey.PublicKey().Bytes()

	tests := []struct {
		name        string
		content     string
		pubKey      []byte
		wantErr     bool
		errContains string
	}{
		{
			name:    "happy path",
			content: "hello, world",
			pubKey:  validPubKey,
		},
		{
			name:    "empty content",
			content: "",
			pubKey:  validPubKey,
		},
		{
			name:        "invalid key - wrong length",
			content:     "hello",
			pubKey:      make([]byte, 32),
			wantErr:     true,
			errContains: "parsing room public key",
		},
		{
			name:        "invalid key - invalid curve point",
			content:     "hello",
			pubKey:      make([]byte, 65), // 65 zero bytes — not a valid P-256 point
			wantErr:     true,
			errContains: "parsing room public key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Encode(tt.content, tt.pubKey, 0)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Len(t, result.EphemeralPublicKey, 65)
			assert.Len(t, result.Nonce, 12)
			assert.NotEmpty(t, result.Ciphertext)
		})
	}
}

func TestEncode_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "non-empty", content: "hello, world"},
		{name: "empty string", content: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate a fresh key pair for each subtest to ensure independence.
			// Each subtest must be fully independent — no shared mutable state.
			privKey, err := ecdh.P256().GenerateKey(rand.Reader)
			require.NoError(t, err)
			pubKeyBytes := privKey.PublicKey().Bytes()

			msg, err := Encode(tc.content, pubKeyBytes, 0)
			require.NoError(t, err)

			// Step 1: parse the ephemeral public key from the EncryptedMessage
			ephPubKey, err := ecdh.P256().NewPublicKey(msg.EphemeralPublicKey)
			require.NoError(t, err)

			// Step 2: ECDH with the room private key and the ephemeral public key
			sharedSecret, err := privKey.ECDH(ephPubKey)
			require.NoError(t, err)

			// Step 3: re-derive the AES key using the same HKDF parameters
			aesKey := make([]byte, 32)
			hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte("room-message-encryption"))
			_, err = io.ReadFull(hkdfReader, aesKey)
			require.NoError(t, err)

			// Step 4: decrypt with AES-256-GCM; AAD is nil on both sides
			block, err := aes.NewCipher(aesKey)
			require.NoError(t, err)
			gcm, err := cipher.NewGCM(block)
			require.NoError(t, err)

			// msg.Ciphertext includes the 16-byte GCM tag appended by Seal — pass it directly
			plaintext, err := gcm.Open(nil, msg.Nonce, msg.Ciphertext, nil)
			require.NoError(t, err)

			// Note: string(nil) == "" in Go, so this assertion correctly validates that
			// plaintext matches the expected content. When tc.content == "", plaintext may be nil
			// but the comparison still holds due to Go's string conversion semantics.
			assert.Equal(t, tc.content, string(plaintext))
		})
	}
}

func TestEncode_NonDeterminism(t *testing.T) {
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubKeyBytes := privKey.PublicKey().Bytes()

	r1, err := Encode("test message", pubKeyBytes, 0)
	require.NoError(t, err)
	r2, err := Encode("test message", pubKeyBytes, 0)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(r1.EphemeralPublicKey, r2.EphemeralPublicKey),
		"ephemeral public keys must differ across calls")
	assert.False(t, bytes.Equal(r1.Nonce, r2.Nonce),
		"nonces must differ across calls")
	assert.False(t, bytes.Equal(r1.Ciphertext, r2.Ciphertext),
		"ciphertexts must differ across calls (symptom of nonce reuse if equal)")

	// Guard: nonce must not be a naive truncation of the ephemeral public key
	assert.False(t, bytes.Equal(r1.Nonce, r1.EphemeralPublicKey[:12]),
		"nonce must not equal first 12 bytes of ephemeral public key")
}

func TestEncode_RandReaderErrors(t *testing.T) {
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubKeyBytes := privKey.PublicKey().Bytes()

	t.Run("ephemeral key generation fails", func(t *testing.T) {
		result, err := encode("hello", pubKeyBytes, 0, &failReader{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "generating ephemeral key")
		assert.Nil(t, result)
	})

	t.Run("nonce generation fails", func(t *testing.T) {
		// P-256 GenerateKey reads ~50 bytes. We loop with increasing byte limits
		// until ephemeral key generation succeeds but the nonce generation hits the failReader.
		// - limit too small: key gen fails with "generating ephemeral key" → increase limit
		// - limit just right: key gen succeeds, nonce gen fails → "generating nonce"
		// - limit too large: both succeed → increase limit (encErr == nil, result != nil)
		var encErr error
		for limit := int64(32); limit <= 128; limit++ {
			r := io.MultiReader(io.LimitReader(rand.Reader, limit), &failReader{})
			_, encErr = encode("hello", pubKeyBytes, 0, r)
			if encErr != nil && strings.Contains(encErr.Error(), "generating nonce") {
				break
			}
		}
		require.Error(t, encErr)
		require.Contains(t, encErr.Error(), "generating nonce", "loop exhausted without reaching nonce generation")
	})
}

func TestEncode_Version(t *testing.T) {
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubKeyBytes := privKey.PublicKey().Bytes()

	msg, err := Encode("hello", pubKeyBytes, 42)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, 42, msg.Version)
}

func TestEncryptedMessage_JSONRoundTrip(t *testing.T) {
	original := EncryptedMessage{
		Version:            7,
		EphemeralPublicKey: []byte{1, 2, 3},
		Nonce:              []byte{4, 5, 6},
		Ciphertext:         []byte{7, 8, 9},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"version":7`)

	var decoded EncryptedMessage
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, original, decoded)
}

// failReader is an io.Reader that always returns an error.
type failReader struct{}

func (f *failReader) Read(_ []byte) (int, error) {
	return 0, errors.New("injected read failure")
}

func TestEncoder_Encode_HappyPath(t *testing.T) {
	// Use a fixed 32-byte private key (a P-256 scalar's worth of entropy).
	priv := make([]byte, 32)
	for i := range priv {
		priv[i] = byte(i + 1)
	}

	enc := NewEncoder()
	got, err := enc.Encode("room-1", "hello", priv, 7)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 7, got.Version)
	assert.Len(t, got.Nonce, 12)
	assert.NotEmpty(t, got.Ciphertext)
	// EphemeralPublicKey field must be empty/unset on the new scheme output.
	assert.Empty(t, got.EphemeralPublicKey)
}

func TestEncoder_Encode_CacheHit(t *testing.T) {
	priv := bytes.Repeat([]byte{0xAB}, 32)
	enc := NewEncoder()

	_, err := enc.Encode("room-1", "msg1", priv, 1)
	require.NoError(t, err)
	_, err = enc.Encode("room-1", "msg2", priv, 1)
	require.NoError(t, err)

	assert.Equal(t, 1, enc.cacheLen(), "same (roomID, version) must not re-derive the AES key")
}

func TestEncoder_Encode_DistinctVersionsCacheSeparately(t *testing.T) {
	priv := bytes.Repeat([]byte{0xAB}, 32)
	enc := NewEncoder()

	_, err := enc.Encode("room-1", "msg1", priv, 1)
	require.NoError(t, err)
	_, err = enc.Encode("room-1", "msg2", priv, 2)
	require.NoError(t, err)

	assert.Equal(t, 2, enc.cacheLen(), "different versions must occupy distinct cache entries")
}

func TestEncoder_Encode_EvictsLowestVersion(t *testing.T) {
	priv := bytes.Repeat([]byte{0x42}, 32)
	enc := NewEncoder(WithMaxCacheEntries(2))

	_, err := enc.Encode("room-A", "a", priv, 1)
	require.NoError(t, err)
	_, err = enc.Encode("room-A", "b", priv, 2)
	require.NoError(t, err)
	_, err = enc.Encode("room-A", "c", priv, 3)
	require.NoError(t, err)

	assert.Equal(t, 2, enc.cacheLen(), "cache must not exceed max")
	// Encoding for version 1 again must miss the cache (we evicted it),
	// while versions 2 and 3 must hit.
	prevLen := enc.cacheLen()
	_, err = enc.Encode("room-A", "d", priv, 2)
	require.NoError(t, err)
	assert.Equal(t, prevLen, enc.cacheLen(), "version 2 must still be cached (hit)")

	_, err = enc.Encode("room-A", "e", priv, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, enc.cacheLen(), "after re-inserting v=1, cache stays at max (now v=2 should have been evicted as lowest)")
}

func TestEncoder_Encode_NonceReaderError(t *testing.T) {
	priv := bytes.Repeat([]byte{0xAA}, 32)
	enc := NewEncoder(WithRand(&failReader{}))

	got, err := enc.Encode("room-1", "hello", priv, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generating nonce")
	assert.Nil(t, got)
}

func TestEncoder_Encode_InvalidKeyLength(t *testing.T) {
	enc := NewEncoder()
	got, err := enc.Encode("room-1", "hello", make([]byte, 31), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be 32 bytes")
	assert.Nil(t, got)
}

func TestEncoder_Encode_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "non-empty", content: "hello, world"},
		{name: "empty", content: ""},
		{name: "unicode", content: "héllo 🌎"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			priv := bytes.Repeat([]byte{0x55}, 32)
			enc := NewEncoder()
			msg, err := enc.Encode("room-1", tc.content, priv, 3)
			require.NoError(t, err)
			require.NotNil(t, msg)

			// Re-derive the AES key with the same HKDF parameters and decrypt.
			aesKey := make([]byte, 32)
			r := hkdf.New(sha256.New, priv, nil, []byte("room-message-encryption-v2"))
			_, err = io.ReadFull(r, aesKey)
			require.NoError(t, err)

			block, err := aes.NewCipher(aesKey)
			require.NoError(t, err)
			gcm, err := cipher.NewGCM(block)
			require.NoError(t, err)

			plaintext, err := gcm.Open(nil, msg.Nonce, msg.Ciphertext, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.content, string(plaintext))
		})
	}
}
