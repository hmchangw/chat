// Package blindidx produces deterministic, non-reversible search terms
// ("blind index" tokens) by keyed-hashing analyzer tokens with HMAC-SHA256.
// Equal plaintext tokens hash to equal terms so Elasticsearch can match them,
// but the plaintext cannot be recovered from a term. The key is a single
// federation-wide secret, kept distinct from any pkg/atrest encryption key
// (key separation: one key per cryptographic purpose).
package blindidx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const (
	minKeyLen = 32 // HMAC-SHA256 key floor
	termBytes = 16 // 128-bit truncation -> 32 hex chars, whitespace-free
)

// Hasher hashes tokens with a fixed key and carries the active key version so
// callers can stamp documents for future rotation (versioned reindex).
type Hasher struct {
	key     []byte
	version string
}

// New returns a Hasher. The key must be at least 32 bytes and the version must
// be non-empty. The key is copied so the caller may reuse its buffer.
func New(key []byte, version string) (*Hasher, error) {
	if len(key) < minKeyLen {
		return nil, errors.New("blindidx: key must be at least 32 bytes")
	}
	if version == "" {
		return nil, errors.New("blindidx: version must not be empty")
	}
	k := make([]byte, len(key))
	copy(k, key)
	return &Hasher{key: k, version: version}, nil
}

// Version returns the key version label for this Hasher.
func (h *Hasher) Version() string { return h.version }

// Hash returns the blind-index term for a single token: the first 16 bytes of
// HMAC-SHA256(key, token), hex-encoded.
func (h *Hasher) Hash(token string) string {
	mac := hmac.New(sha256.New, h.key)
	mac.Write([]byte(token))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:termBytes])
}

// Tokens hashes a token stream in order, preserving length.
func (h *Hasher) Tokens(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = h.Hash(t)
	}
	return out
}
