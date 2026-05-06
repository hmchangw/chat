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
// payloads. Its concrete implementation composes a KEKLoader, a DEKStore
// and an LRU cache of unwrapped DEKs.
type Cipher interface {
	Encrypt(ctx context.Context, roomID string, fields EncryptedFields) ([]byte, EncMeta, error)
	Decrypt(ctx context.Context, roomID string, encPayload []byte, meta EncMeta) (EncryptedFields, error)
}

// NewCipher composes a Cipher from its dependencies.
func NewCipher(loader KEKLoader, store DEKStore, cfg Config) Cipher {
	return newCipher(loader, store, newDEKCache(cfg.DEKCacheSize, cfg.DEKCacheTTL))
}

func newCipher(loader KEKLoader, store DEKStore, cache *dekCache) Cipher {
	return &cipherImpl{loader: loader, store: store, cache: cache, randReader: rand.Reader}
}

// cipherImpl is the concrete Cipher. The unexported name avoids colliding
// with the imported `crypto/cipher` package.
type cipherImpl struct {
	loader     KEKLoader
	store      DEKStore
	cache      *dekCache
	randReader io.Reader
}

func (c *cipherImpl) Encrypt(ctx context.Context, roomID string, fields EncryptedFields) (out []byte, meta EncMeta, err error) { //nolint:gocritic // hugeParam: fields is passed by value to satisfy the Cipher interface
	ctx, span := tracer.Start(ctx, "atrest.Encrypt")
	defer span.End()
	defer func() { encryptCounter.WithLabelValues(resultLabel(err)).Inc() }()

	span.SetAttributes(attribute.String("room_id", roomID))

	dek, cacheHit, err := c.dekFor(ctx, roomID)
	if err != nil {
		return nil, EncMeta{}, err
	}
	span.SetAttributes(attribute.Bool("dek_cache_hit", cacheHit))

	plaintext, err := json.Marshal(fields)
	if err != nil {
		return nil, EncMeta{}, fmt.Errorf("marshal payload: %w", err)
	}
	span.SetAttributes(attribute.Int("plaintext_bytes", len(plaintext)))

	ciphertext, nonce, err := encryptGCM(dek, plaintext, c.randReader)
	if err != nil {
		return nil, EncMeta{}, fmt.Errorf("encrypt payload: %w", err)
	}
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

	dek, cacheHit, err := c.dekFor(ctx, roomID)
	if err != nil {
		return EncryptedFields{}, err
	}
	span.SetAttributes(attribute.Bool("dek_cache_hit", cacheHit))

	plain, err := decryptGCM(dek, payload, meta.Nonce)
	if err != nil {
		return EncryptedFields{}, err
	}
	span.SetAttributes(attribute.Int("plaintext_bytes", len(plain)))

	var decoded EncryptedFields
	if err := json.Unmarshal(plain, &decoded); err != nil {
		return EncryptedFields{}, fmt.Errorf("%w: %w", ErrPayloadMalformed, err)
	}
	return decoded, nil
}

// dekFor returns the unwrapped DEK for roomID, creating one lazily if no
// row exists yet. The second return value indicates whether the DEK was
// served from the in-memory cache.
func (c *cipherImpl) dekFor(ctx context.Context, roomID string) ([]byte, bool, error) {
	if dek, ok := c.cache.get(roomID); ok {
		dekCacheHits.Inc()
		return dek, true, nil
	}
	dekCacheMisses.Inc()
	row, err := c.store.Get(ctx, roomID)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		dek, _, err := c.createDEK(ctx, roomID)
		if err != nil {
			return nil, false, err
		}
		c.cache.set(roomID, dek)
		return dek, false, nil
	}
	kek, ok := c.loader.ByVersion(row.KEKVersion)
	if !ok {
		return nil, false, fmt.Errorf("%w: version %d", ErrKEKVersionUnknown, row.KEKVersion)
	}
	dek, err := decryptGCM(kek, row.WrappedDEK, row.WrapNonce)
	if err != nil {
		return nil, false, fmt.Errorf("unwrap dek: %w", err)
	}
	c.cache.set(roomID, dek)
	return dek, false, nil
}

// createDEK generates and stores a fresh DEK. On a concurrent insert race,
// it re-Gets and uses the winner's row.
func (c *cipherImpl) createDEK(ctx context.Context, roomID string) ([]byte, *RoomDataKey, error) {
	dek := make([]byte, 32)
	if _, err := io.ReadFull(c.randReader, dek); err != nil {
		return nil, nil, fmt.Errorf("generate DEK: %w", err)
	}
	kekVersion, kek := c.loader.Current()
	wrapped, nonce, err := encryptGCM(kek, dek, c.randReader)
	if err != nil {
		return nil, nil, fmt.Errorf("wrap DEK: %w", err)
	}
	row := RoomDataKey{
		ID:         roomID,
		WrappedDEK: wrapped,
		WrapNonce:  nonce,
		KEKVersion: kekVersion,
		CreatedAt:  time.Now().UTC(),
	}
	if err := c.store.Upsert(ctx, row); err != nil {
		return nil, nil, err
	}
	// Re-read to detect a concurrent insert that won the race.
	stored, err := c.store.Get(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	if stored == nil {
		return nil, nil, errors.New("dek row missing after upsert")
	}
	if !bytes.Equal(stored.WrappedDEK, wrapped) {
		// Lost the race: another goroutine inserted first. Unwrap theirs.
		kek2, ok := c.loader.ByVersion(stored.KEKVersion)
		if !ok {
			return nil, nil, fmt.Errorf("%w: version %d", ErrKEKVersionUnknown, stored.KEKVersion)
		}
		dek2, err := decryptGCM(kek2, stored.WrappedDEK, stored.WrapNonce)
		if err != nil {
			return nil, nil, fmt.Errorf("unwrap winner DEK: %w", err)
		}
		return dek2, stored, nil
	}
	dekCreations.Inc()
	return dek, stored, nil
}

// encryptGCM seals plaintext with a fresh 12-byte random nonce. Returns
// (ciphertext, nonce). The auth tag is appended to the ciphertext by GCM.
func encryptGCM(key, plaintext []byte, r io.Reader) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("new cipher: %w", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, nil, fmt.Errorf("nonce: %w", err)
	}
	ct := g.Seal(nil, nonce, plaintext, nil)
	return ct, nonce, nil
}

func decryptGCM(key, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	plain, err := g.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthFailed, err)
	}
	return plain, nil
}
