package blindidx_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindidx"
)

// 32-byte test key; never used outside tests.
var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestNew_RejectsShortKey(t *testing.T) {
	_, err := blindidx.New([]byte("too-short"), "v1")
	require.Error(t, err)
}

func TestNew_RejectsEmptyVersion(t *testing.T) {
	_, err := blindidx.New(testKey, "")
	require.Error(t, err)
}

func TestHasher_Version(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", h.Version())
}

func TestHash_DeterministicAndOpaque(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)

	got := h.Hash("salary")
	// Deterministic: same input, same output.
	assert.Equal(t, got, h.Hash("salary"))
	// Opaque: 32 lowercase hex chars (128-bit truncation), never the plaintext.
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), got)
	assert.NotContains(t, got, "salary")
}

func TestHash_DistinctTokensDiffer(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)
	assert.NotEqual(t, h.Hash("salary"), h.Hash("bonus"))
}

func TestHash_DifferentKeysProduceDifferentTerms(t *testing.T) {
	h1, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)
	otherKey := []byte("FEDCBA9876543210FEDCBA9876543210")
	h2, err := blindidx.New(otherKey, "v1")
	require.NoError(t, err)
	assert.NotEqual(t, h1.Hash("salary"), h2.Hash("salary"))
}

func TestTokens_PreservesOrderAndLength(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)

	in := []string{"a", "b", "a"}
	got := h.Tokens(in)
	require.Len(t, got, 3)
	assert.Equal(t, h.Hash("a"), got[0])
	assert.Equal(t, h.Hash("b"), got[1])
	// Equal plaintext tokens map to equal terms (required for matching).
	assert.Equal(t, got[0], got[2])
}
