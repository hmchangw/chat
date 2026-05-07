package atrest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("github.com/hmchangw/chat/pkg/atrest")

// Cipher is the public API used by services to encrypt/decrypt message
// payloads. Its concrete implementation composes a KeyWrapper, a DEKStore
// and an LRU cache of unwrapped DEKs.
type Cipher interface {
	Encrypt(ctx context.Context, roomID string, fields EncryptedFields) ([]byte, EncMeta, error)
	Decrypt(ctx context.Context, roomID string, encPayload []byte, meta EncMeta) (EncryptedFields, error)
}

// NewCipher composes a Cipher from its dependencies.
func NewCipher(wrapper KeyWrapper, store DEKStore, cfg Config) Cipher {
	return newCipher(wrapper, store, newDEKCache(cfg.DEKCacheSize, cfg.DEKCacheTTL))
}

func newCipher(wrapper KeyWrapper, store DEKStore, cache *dekCache) Cipher {
	return &cipherImpl{wrapper: wrapper, store: store, cache: cache, randReader: rand.Reader}
}

// cipherImpl is the concrete Cipher. The unexported name avoids colliding
// with the imported `crypto/cipher` package.
type cipherImpl struct {
	wrapper    KeyWrapper
	store      DEKStore
	cache      *dekCache
	randReader io.Reader
}

func (c *cipherImpl) Encrypt(ctx context.Context, roomID string, fields EncryptedFields) (out []byte, meta EncMeta, err error) { //nolint:gocritic // hugeParam: fields is passed by value to satisfy the Cipher interface
	ctx, span := tracer.Start(ctx, "atrest.Encrypt")
	defer span.End()
	defer func() { encryptCounter.WithLabelValues(resultLabel(err)).Inc() }()

	span.SetAttributes(attribute.String("room_id", roomID))

	aead, cacheHit, err := c.dekFor(ctx, roomID)
	if err != nil {
		return nil, EncMeta{}, fmt.Errorf("acquire DEK for room %s: %w", roomID, err)
	}
	span.SetAttributes(attribute.Bool("dek_cache_hit", cacheHit))

	plaintext, err := json.Marshal(fields)
	if err != nil {
		return nil, EncMeta{}, fmt.Errorf("marshal payload: %w", err)
	}
	span.SetAttributes(attribute.Int("plaintext_bytes", len(plaintext)))

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(c.randReader, nonce); err != nil {
		return nil, EncMeta{}, fmt.Errorf("nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)
	span.SetAttributes(attribute.Int("ciphertext_bytes", len(ciphertext)))
	return ciphertext, EncMeta{Nonce: nonce}, nil
}

func (c *cipherImpl) Decrypt(ctx context.Context, roomID string, payload []byte, meta EncMeta) (out EncryptedFields, err error) {
	ctx, span := tracer.Start(ctx, "atrest.Decrypt")
	defer span.End()
	defer func() { decryptCounter.WithLabelValues(resultLabel(err)).Inc() }()

	span.SetAttributes(
		attribute.String("room_id", roomID),
		attribute.Int("ciphertext_bytes", len(payload)),
	)

	aead, cacheHit, err := c.dekFor(ctx, roomID)
	if err != nil {
		return EncryptedFields{}, fmt.Errorf("acquire DEK for room %s: %w", roomID, err)
	}
	span.SetAttributes(attribute.Bool("dek_cache_hit", cacheHit))

	plain, err := aead.Open(nil, meta.Nonce, payload, nil)
	if err != nil {
		return EncryptedFields{}, fmt.Errorf("%w: %w", ErrAuthFailed, err)
	}
	span.SetAttributes(attribute.Int("plaintext_bytes", len(plain)))

	var decoded EncryptedFields
	if err := json.Unmarshal(plain, &decoded); err != nil {
		return EncryptedFields{}, fmt.Errorf("%w: %w", ErrPayloadMalformed, err)
	}
	return decoded, nil
}

// dekFor returns the AEAD for roomID, creating a DEK lazily if no row
// exists yet. The second return is whether the AEAD was served from the
// in-memory cache.
func (c *cipherImpl) dekFor(ctx context.Context, roomID string) (cipher.AEAD, bool, error) {
	if aead, ok := c.cache.get(roomID); ok {
		dekCacheHits.Inc()
		return aead, true, nil
	}
	dekCacheMisses.Inc()
	dek, err := c.fetchOrCreateDEK(ctx, roomID)
	if err != nil {
		return nil, false, err
	}
	aead, err := newAEAD(dek)
	if err != nil {
		return nil, false, fmt.Errorf("build AEAD: %w", err)
	}
	c.cache.set(roomID, aead)
	return aead, false, nil
}

func (c *cipherImpl) fetchOrCreateDEK(ctx context.Context, roomID string) ([]byte, error) {
	row, err := c.store.Get(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("get DEK row: %w", err)
	}
	if row == nil {
		dek, _, err := c.createDEK(ctx, roomID)
		return dek, err
	}
	dek, err := c.wrapper.Unwrap(ctx, row.WrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrap dek: %w", err)
	}
	return dek, nil
}

// createDEK generates a fresh DEK, asks the KeyWrapper to wrap it, and
// upserts the row. A re-Get after Upsert detects a concurrent winner
// (Mongo's $setOnInsert keeps the first row inserted). On a lost race
// the winner's DEK is unwrapped and returned instead.
func (c *cipherImpl) createDEK(ctx context.Context, roomID string) ([]byte, *RoomDataKey, error) {
	dek := make([]byte, 32)
	if _, err := io.ReadFull(c.randReader, dek); err != nil {
		return nil, nil, fmt.Errorf("generate DEK: %w", err)
	}
	wrapped, err := c.wrapper.Wrap(ctx, dek)
	if err != nil {
		return nil, nil, fmt.Errorf("wrap DEK: %w", err)
	}
	row := RoomDataKey{
		ID:         roomID,
		WrappedDEK: wrapped,
		CreatedAt:  time.Now().UTC(),
	}
	if err := c.store.Upsert(ctx, row); err != nil {
		return nil, nil, fmt.Errorf("upsert DEK row: %w", err)
	}
	stored, err := c.store.Get(ctx, roomID)
	if err != nil {
		return nil, nil, fmt.Errorf("re-read DEK row after upsert: %w", err)
	}
	if stored == nil {
		return nil, nil, errors.New("dek row missing after upsert")
	}
	if !bytes.Equal(stored.WrappedDEK, wrapped) {
		// Lost the race: another goroutine inserted first. Unwrap theirs.
		dek2, err := c.wrapper.Unwrap(ctx, stored.WrappedDEK)
		if err != nil {
			return nil, nil, fmt.Errorf("unwrap winner DEK: %w", err)
		}
		return dek2, stored, nil
	}
	dekCreations.Inc()
	return dek, stored, nil
}

// newAEAD constructs the AES-256-GCM AEAD for a 32-byte key.
func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return g, nil
}
