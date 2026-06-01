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
	"golang.org/x/sync/singleflight"
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
	sf         singleflight.Group // coalesces concurrent DEK fetches per roomID
	randReader io.Reader
}

func (c *cipherImpl) Encrypt(ctx context.Context, roomID string, fields EncryptedFields) ([]byte, EncMeta, error) { //nolint:gocritic // hugeParam: fields is passed by value to satisfy the Cipher interface
	ctx, span := tracer.Start(ctx, "atrest.Encrypt")
	defer span.End()

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

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(c.randReader, nonce); err != nil {
		return nil, EncMeta{}, fmt.Errorf("nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, EncMeta{Nonce: nonce}, nil
}

func (c *cipherImpl) Decrypt(ctx context.Context, roomID string, payload []byte, meta EncMeta) (EncryptedFields, error) {
	ctx, span := tracer.Start(ctx, "atrest.Decrypt")
	defer span.End()

	span.SetAttributes(attribute.String("room_id", roomID))

	aead, cacheHit, err := c.dekFor(ctx, roomID)
	if err != nil {
		return EncryptedFields{}, fmt.Errorf("acquire DEK for room %s: %w", roomID, err)
	}
	span.SetAttributes(attribute.Bool("dek_cache_hit", cacheHit))

	// Guard the nonce length: crypto/cipher's GCM panics if len(nonce) !=
	// NonceSize(). meta.Nonce is row-persisted metadata that could be
	// corrupted or truncated, so we reject malformed input with a typed
	// error instead.
	if len(meta.Nonce) != aead.NonceSize() {
		return EncryptedFields{}, fmt.Errorf("%w: invalid nonce length %d (want %d)", ErrPayloadMalformed, len(meta.Nonce), aead.NonceSize())
	}
	plain, err := aead.Open(nil, meta.Nonce, payload, nil)
	if err != nil {
		return EncryptedFields{}, fmt.Errorf("%w: %w", ErrAuthFailed, err)
	}

	var decoded EncryptedFields
	if err := json.Unmarshal(plain, &decoded); err != nil {
		return EncryptedFields{}, fmt.Errorf("%w: %w", ErrPayloadMalformed, err)
	}
	return decoded, nil
}

// dekFor returns the AEAD for roomID, lazily creating a DEK if none exists.
// The bool reports a cache hit. Concurrent cache-miss callers for the same
// roomID coalesce via singleflight so only one runs the store fetch + unwrap +
// cache populate, bounding Vault QPS during cold-start miss bursts.
func (c *cipherImpl) dekFor(ctx context.Context, roomID string) (cipher.AEAD, bool, error) {
	if aead, ok := c.cache.get(roomID); ok {
		dekCacheHits.Inc()
		return aead, true, nil
	}
	dekCacheMisses.Inc()
	// The singleflight closure builds its own context: detached from the
	// caller's cancellation (so a short-lived leader doesn't poison every
	// coalesced waiter), with a bounded deadline (so a stuck Vault/Mongo
	// can't pin the slot indefinitely). Allocating bgCtx + cancel INSIDE
	// the closure is critical — singleflight invokes the closure only for
	// the leader, so the matching defer cancel() also runs only for the
	// leader. Allocating them outside would leak one *time.Timer per
	// coalesced waiter for ~dekFetchTimeout. Trace/log values flow through
	// unchanged because WithoutCancel preserves context.Values.
	v, err, _ := c.sf.Do(roomID, func() (any, error) {
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dekFetchTimeout)
		defer cancel()
		dek, err := c.fetchOrCreateDEK(bgCtx, roomID)
		if err != nil {
			return nil, err
		}
		aead, err := newAEAD(dek)
		if err != nil {
			return nil, fmt.Errorf("build AEAD: %w", err)
		}
		c.cache.set(roomID, aead)
		return aead, nil
	})
	if err != nil {
		return nil, false, err
	}
	return v.(cipher.AEAD), false, nil
}

// dekFetchTimeout bounds the detached DEK-fetch context. Kept strictly
// below the consumer-default ACK_WAIT (30s in pkg/stream/consumer.go) so
// a coalesced burst can't push past AckWait and trigger JetStream
// redelivery while the original Encrypt is still in flight. 20s covers
// retried Vault calls under a typical k8s outage with margin for the
// rest of the handler.
const dekFetchTimeout = 20 * time.Second

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

// createDEK asks the KeyWrapper to mint a fresh DEK + wrapped form in
// one call (HSM-friendly: the plaintext DEK originates inside the KEK
// provider) and upserts the row. A re-Get after Upsert detects a
// concurrent winner (Mongo's $setOnInsert keeps the first row inserted);
// on a lost race the winner's stored DEK is unwrapped and returned.
func (c *cipherImpl) createDEK(ctx context.Context, roomID string) ([]byte, *RoomDataKey, error) {
	dek, wrapped, err := c.wrapper.GenerateDataKey(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("generate DEK: %w", err)
	}
	if len(dek) != 32 {
		return nil, nil, fmt.Errorf("generate DEK: expected 32 bytes, got %d", len(dek))
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
