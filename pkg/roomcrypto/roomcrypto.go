package roomcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// EncryptedMessage holds the output of Encode.
// []byte fields marshal to base64 in JSON automatically.
//
// Note: this struct intentionally deviates from the project convention of including bson tags.
// It is a serialisation-only type sent to clients over JSON; it is never written to MongoDB.
type EncryptedMessage struct {
	EphemeralPublicKey []byte `json:"ephemeralPublicKey"` // 65 bytes, uncompressed P-256 point
	Nonce              []byte `json:"nonce"`              // 12 bytes, AES-GCM nonce
	Ciphertext         []byte `json:"ciphertext"`         // encrypted content + 16-byte AES-GCM tag
}

// Encode encrypts content using the room's P-256 public key.
// roomPublicKey is the uncompressed point (65 bytes) as stored in MongoDB.
func Encode(content string, roomPublicKey []byte) (*EncryptedMessage, error) {
	// Step 1: parse and validate the room public key
	roomPubKey, err := ecdh.P256().NewPublicKey(roomPublicKey)
	if err != nil {
		return nil, fmt.Errorf("parsing room public key: %w", err)
	}

	// Step 2: generate a fresh ephemeral P-256 key pair for this message
	ephemeralPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ephemeral key: %w", err)
	}

	// Step 3: ECDH — derive shared secret from ephemeral private key + room public key
	sharedSecret, err := ephemeralPrivKey.ECDH(roomPubKey)
	if err != nil {
		// Unreachable: roomPubKey was validated by NewPublicKey above.
		return nil, fmt.Errorf("computing ECDH shared secret: %w", err)
	}

	// Step 4: HKDF-SHA256 — derive 32-byte AES key from shared secret
	// info="room-message-encryption" provides domain separation
	aesKey := make([]byte, 32)
	hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte("room-message-encryption"))
	if _, err := io.ReadFull(hkdfReader, aesKey); err != nil {
		// Unreachable for SHA-256, but must be checked per project convention.
		return nil, fmt.Errorf("deriving AES key: %w", err)
	}

	// Step 5: AES-256-GCM cipher setup
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		// Unreachable: aesKey is always 32 bytes from HKDF above.
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		// Unreachable: AES always produces a 128-bit block cipher.
		return nil, fmt.Errorf("creating GCM wrapper: %w", err)
	}

	// Step 6: generate a random 12-byte nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Step 7: encrypt and authenticate; AAD is nil (see spec for trade-off rationale)
	// Seal appends the 16-byte GCM authentication tag to the ciphertext.
	ciphertext := gcm.Seal(nil, nonce, []byte(content), nil)

	return &EncryptedMessage{
		EphemeralPublicKey: ephemeralPrivKey.PublicKey().Bytes(),
		Nonce:              nonce,
		Ciphertext:         ciphertext,
	}, nil
}
