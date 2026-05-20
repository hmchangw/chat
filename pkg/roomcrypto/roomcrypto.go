package roomcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

// EncryptedMessage holds the output of Encode.
// []byte fields marshal to base64 in JSON automatically.
//
// Note: this struct intentionally deviates from the project convention of including bson tags.
// It is a serialisation-only type sent to clients over JSON; it is never written to MongoDB.
type EncryptedMessage struct {
	// key version used to encrypt; matches roomkeystore VersionedKeyPair.Version
	Version            int    `json:"version"`
	EphemeralPublicKey []byte `json:"ephemeralPublicKey,omitempty"` // legacy scheme; empty on the new scheme
	Nonce              []byte `json:"nonce"`                        // 12 bytes, AES-GCM nonce
	Ciphertext         []byte `json:"ciphertext"`                   // encrypted content + 16-byte AES-GCM tag
}

// Encode encrypts content using the room's P-256 public key.
// roomPublicKey is the uncompressed point (65 bytes) as stored in MongoDB.
// version is stamped into the returned EncryptedMessage so receivers can
// pick the correct private key for decryption.
func Encode(content string, roomPublicKey []byte, version int) (*EncryptedMessage, error) {
	return encode(content, roomPublicKey, version, rand.Reader)
}

// encode is the internal implementation that accepts an io.Reader for randomness,
// enabling error path testing without changing the public API.
func encode(content string, roomPublicKey []byte, version int, randReader io.Reader) (*EncryptedMessage, error) {
	// Step 1: parse and validate the room public key
	roomPubKey, err := ecdh.P256().NewPublicKey(roomPublicKey)
	if err != nil {
		return nil, fmt.Errorf("parsing room public key: %w", err)
	}

	// Step 2: generate a fresh ephemeral P-256 key pair for this message
	ephemeralPrivKey, err := ecdh.P256().GenerateKey(randReader)
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
	if _, err := io.ReadFull(randReader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Step 7: encrypt and authenticate; AAD is nil (see spec for trade-off rationale)
	// Seal appends the 16-byte GCM authentication tag to the ciphertext.
	ciphertext := gcm.Seal(nil, nonce, []byte(content), nil)

	return &EncryptedMessage{
		Version:            version,
		EphemeralPublicKey: ephemeralPrivKey.PublicKey().Bytes(),
		Nonce:              nonce,
		Ciphertext:         ciphertext,
	}, nil
}

// Encoder holds the per-(roomId, version) AES-GCM cipher cache for the
// HKDF-only encryption scheme. Construct one per process and share it
// across goroutines.
type Encoder struct {
	mu    sync.RWMutex
	cache map[encoderCacheKey]cipher.AEAD
	rand  io.Reader
	max   int
}

type encoderCacheKey struct {
	roomID  string
	version int
}

// EncoderOption configures an Encoder at construction time.
type EncoderOption func(*Encoder)

// WithMaxCacheEntries sets the upper bound on the per-(roomId, version)
// AES-GCM cache. When exceeded, the entry with the lowest version is
// evicted. Default 4096.
func WithMaxCacheEntries(n int) EncoderOption {
	return func(e *Encoder) { e.max = n }
}

// WithRand overrides the source of randomness used for nonce generation.
// Intended for testing only.
func WithRand(r io.Reader) EncoderOption {
	return func(e *Encoder) { e.rand = r }
}

// NewEncoder constructs an Encoder with default cache size 4096 and
// crypto/rand.Reader as the randomness source.
func NewEncoder(opts ...EncoderOption) *Encoder {
	e := &Encoder{
		cache: make(map[encoderCacheKey]cipher.AEAD),
		rand:  rand.Reader,
		max:   4096,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Encode encrypts content under the AES key derived from roomPrivateKey
// for the given (roomID, version). The derived AES-GCM cipher is cached
// on the Encoder; repeat calls for the same (roomID, version) skip key
// derivation.
func (e *Encoder) Encode(roomID, content string, roomPrivateKey []byte, version int) (*EncryptedMessage, error) {
	gcm, err := e.aeadFor(roomID, roomPrivateKey, version)
	if err != nil {
		return nil, fmt.Errorf("preparing cipher for room %s v%d: %w", roomID, version, err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(e.rand, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, []byte(content), nil)
	return &EncryptedMessage{
		Version:    version,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

func (e *Encoder) aeadFor(roomID string, roomPrivateKey []byte, version int) (cipher.AEAD, error) {
	if len(roomPrivateKey) != 32 {
		return nil, fmt.Errorf("room private key must be 32 bytes, got %d", len(roomPrivateKey))
	}

	key := encoderCacheKey{roomID: roomID, version: version}

	e.mu.RLock()
	gcm, ok := e.cache[key]
	e.mu.RUnlock()
	if ok {
		return gcm, nil
	}

	aesKey := make([]byte, 32)
	r := hkdf.New(sha256.New, roomPrivateKey, nil, []byte("room-message-encryption-v2"))
	if _, err := io.ReadFull(r, aesKey); err != nil {
		return nil, fmt.Errorf("deriving AES key: %w", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	newGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM wrapper: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	// Double-check under write lock — another goroutine may have populated.
	if existing, ok := e.cache[key]; ok {
		return existing, nil
	}
	if len(e.cache) >= e.max {
		e.evictLowestVersionLocked()
	}
	e.cache[key] = newGCM
	return newGCM, nil
}

// evictLowestVersionLocked drops the entry with the lowest version
// across all rooms. This policy is deliberately simple (not LRU): hot
// rooms keep their entries via frequent insertions; rare rooms with
// old, unused versions are dropped first. Caller must hold e.mu for writing.
//
// Precondition: len(e.cache) > 0 (only called when cache is full).
func (e *Encoder) evictLowestVersionLocked() {
	var (
		victim    encoderCacheKey
		haveFirst bool
	)
	for k := range e.cache {
		if !haveFirst || k.version < victim.version {
			victim = k
			haveFirst = true
		}
	}
	if haveFirst {
		delete(e.cache, victim)
	}
}
