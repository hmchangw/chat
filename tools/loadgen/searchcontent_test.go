package main

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchableContent_Empty(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	assert.Equal(t, "", searchableContent(r, 0))
	assert.Equal(t, "", searchableContent(r, -10))
}

func TestSearchableContent_Deterministic(t *testing.T) {
	a := searchableContent(rand.New(rand.NewSource(42)), 200)
	b := searchableContent(rand.New(rand.NewSource(42)), 200)
	assert.Equal(t, a, b, "same seed must yield identical content")
}

func TestSearchableContent_RespectsSizeAndUTF8(t *testing.T) {
	for _, size := range []int{1, 5, 17, 50, 200, 1000} {
		r := rand.New(rand.NewSource(int64(size)))
		got := searchableContent(r, size)
		assert.LessOrEqual(t, len(got), size, "size=%d must not overflow byte budget", size)
		assert.NotEmpty(t, got, "size=%d must produce content", size)
		assert.True(t, utf8.ValidString(got), "size=%d must stay valid UTF-8 (no mid-rune cut)", size)
	}
}

// The whole point of the generator: across a seeded corpus, every benchmark
// query-pool term must appear in at least one document, so search-sustained
// arms actually return hits (and arms A/B exercise the decrypt path) instead of
// measuring zero-result latency.
func TestSearchableContent_CoversQueryPool(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	var corpus strings.Builder
	for i := 0; i < 3000; i++ {
		corpus.WriteString(searchableContent(r, 200))
		corpus.WriteByte('\n')
	}
	hay := corpus.String()
	for _, term := range searchQueryPool {
		assert.Contains(t, hay, term, "seeded corpus must contain query-pool term %q", term)
	}
}

func TestSearchableContent_Multilingual(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	var corpus strings.Builder
	for i := 0; i < 500; i++ {
		corpus.WriteString(searchableContent(r, 200))
	}
	hasCJK := false
	for _, ru := range corpus.String() {
		if ru >= 0x4E00 && ru <= 0x9FFF {
			hasCJK = true
			break
		}
	}
	require.True(t, hasCJK, "seeded corpus must include CJK content for the multilingual arms")
}
