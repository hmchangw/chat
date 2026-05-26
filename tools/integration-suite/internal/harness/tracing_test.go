package harness

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTraceparent_FormatMatchesW3C(t *testing.T) {
	tp := NewTraceparent()
	// Format: 00-<32hex>-<16hex>-<2hex>
	re := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)
	assert.True(t, re.MatchString(tp), "traceparent %q does not match format", tp)
}

func TestTraceIDFromTraceparent(t *testing.T) {
	tp := "00-4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7-1234567890abcdef-01"
	id, err := TraceIDFromTraceparent(tp)
	require.NoError(t, err)
	assert.Equal(t, "4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7", id)
}

func TestTraceIDFromTraceparent_Malformed(t *testing.T) {
	_, err := TraceIDFromTraceparent("garbage")
	assert.Error(t, err)
}

func TestNewTraceparent_UniqueAcrossCalls(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		seen[NewTraceparent()] = true
	}
	assert.Len(t, seen, 100, "expected 100 unique traceparents")
}
