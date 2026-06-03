package blindsearch_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindsearch"
)

var key32Hex = strings.Repeat("ab", 32) // 64 hex chars -> 32 raw bytes

func TestLoadHasher_DecodesHexAndEnforcesFloor(t *testing.T) {
	h, err := blindsearch.LoadHasher(key32Hex, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", h.Version())

	// 31 raw bytes (62 hex chars) must be rejected AFTER decoding, not pass as a
	// 62-char string (carry-forward flag #3).
	_, err = blindsearch.LoadHasher(strings.Repeat("ab", 31), "v1")
	require.Error(t, err)

	_, err = blindsearch.LoadHasher("nothex!!", "v1")
	require.Error(t, err)

	_, err = blindsearch.LoadHasher(key32Hex, "")
	require.Error(t, err)
}

func TestTermsAndField_MatchAndAreParallel(t *testing.T) {
	h, err := blindsearch.LoadHasher(key32Hex, "v1")
	require.NoError(t, err)

	terms := blindsearch.Terms(h, "Hello, World!")
	// "hello" and "world" -> 2 blinded hex terms.
	require.Len(t, terms, 2)
	for _, term := range terms {
		_, decErr := hex.DecodeString(term)
		assert.NoError(t, decErr, "term must be hex (whitespace-free) for the space-join")
		assert.NotContains(t, term, " ")
	}

	// Field is exactly the space-join of Terms — the contract both index and
	// query sides rely on.
	assert.Equal(t, strings.Join(terms, " "), blindsearch.Field(h, "Hello, World!"))

	// Empty content -> empty field (Phase-0 carry-forward flag #2).
	assert.Empty(t, blindsearch.Terms(h, ""))
	assert.Equal(t, "", blindsearch.Field(h, ""))
}
