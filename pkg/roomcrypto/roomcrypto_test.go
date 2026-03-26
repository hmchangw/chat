package roomcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
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
			result, err := Encode(tt.content, tt.pubKey)
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

			msg, err := Encode(tc.content, pubKeyBytes)
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

	r1, err := Encode("test message", pubKeyBytes)
	require.NoError(t, err)
	r2, err := Encode("test message", pubKeyBytes)
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
		result, err := encode("hello", pubKeyBytes, &failReader{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "generating ephemeral key")
		assert.Nil(t, result)
	})

	t.Run("nonce generation fails", func(t *testing.T) {
		// P-256 GenerateKey reads 32+ bytes (may retry via rejection sampling).
		// We loop with increasing byte limits until ephemeral key generation succeeds
		// but the nonce generation hits the failReader.
		// - limit too small: key gen fails with "generating ephemeral key" → increase limit
		// - limit just right: key gen succeeds, nonce gen fails → "generating nonce"
		// - limit too large: both succeed → increase limit (encErr == nil, result != nil)
		var encErr error
		for limit := int64(32); limit <= 4096; limit += 32 {
			r := io.MultiReader(io.LimitReader(rand.Reader, limit), &failReader{})
			_, encErr = encode("hello", pubKeyBytes, r)
			if encErr != nil && strings.Contains(encErr.Error(), "generating nonce") {
				break
			}
		}
		require.Error(t, encErr)
		assert.Contains(t, encErr.Error(), "generating nonce")
	})
}

// failReader is an io.Reader that always returns an error.
type failReader struct{}

func (f *failReader) Read(_ []byte) (int, error) {
	return 0, errors.New("injected read failure")
}

