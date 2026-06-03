// Package blindsearch composes the message analyzer (pkg/msganalyzer) with the
// blind-index hasher (pkg/blindidx) into the single function pair used at BOTH
// index time (search-sync-worker) and query time (search-service). Routing all
// blind-token production through here guarantees the two sides are byte-identical
// — the core correctness invariant of blind-index search.
package blindsearch

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hmchangw/chat/pkg/blindidx"
	"github.com/hmchangw/chat/pkg/msganalyzer"
)

// LoadHasher decodes a hex-encoded blind key into raw bytes and builds a Hasher.
// The hex decode happens BEFORE the length check so a 32-byte key supplied as 64
// hex chars is not mistaken for a 64-byte key (carry-forward flag #3).
func LoadHasher(hexKey, version string) (*blindidx.Hasher, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode blind key hex: %w", err)
	}
	h, err := blindidx.New(raw, version)
	if err != nil {
		return nil, fmt.Errorf("build blind hasher: %w", err)
	}
	return h, nil
}

// Terms returns the ordered blinded terms for text: Analyze -> HMAC each token.
func Terms(h *blindidx.Hasher, text string) []string {
	return h.Tokens(msganalyzer.Analyze(text))
}

// Field returns the space-joined blinded terms, ready to store in / query against
// the `contentBlind` ES field (whitespace analyzer). Empty text -> "".
func Field(h *blindidx.Hasher, text string) string {
	return strings.Join(Terms(h, text), " ")
}
