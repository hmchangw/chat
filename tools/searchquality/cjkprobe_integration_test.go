//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/msganalyzer"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/testutil"
)

// TestCustomAnalyzerDoesNotBigramCJK pins a surprising, load-bearing property of
// the production plaintext analyzer: ES's `custom_analyzer` does NOT bigram CJK,
// even though its filter chain lists `cjk_bigram`. The filter is INERT here
// because `cjk_bigram` only fires on CJK-typed tokens emitted by the standard/icu
// tokenizers, whereas our `pattern` tokenizer emits `word`-typed tokens — so a
// CJK run like `公园散步` survives as one whole token.
//
// This matters because it documents a deliberate, helpful divergence from
// `pkg/msganalyzer`, the Go port used for blind-hashed indexing: msganalyzer
// DOES bigram CJK runs (better for CJK substring search), while the legacy ES
// plaintext analyzer keeps whole CJK runs. The two are intentionally NOT
// token-for-token identical on CJK input. See spec §15. If a future ES analyzer
// change starts bigramming CJK, this test fails and forces a conscious decision.
func TestCustomAnalyzerDoesNotBigramCJK(t *testing.T) {
	esURL := testutil.Elasticsearch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	eng, err := searchengine.New(ctx, searchengine.Config{Backend: "elasticsearch", URL: esURL})
	require.NoError(t, err)

	indexC := testutil.ElasticsearchIndex(t, "cjkprobe-c")
	require.NoError(t, eng.UpsertTemplate(ctx, indexC+"_template", plaintextTemplateBody(indexC)))

	// Index a doc so the template-based index (with custom_analyzer) is created.
	_, err = eng.Bulk(ctx, []searchengine.BulkAction{
		{Action: searchengine.ActionIndex, Index: indexC, DocID: "seed", Version: 1, Doc: []byte(`{"content":"seed","lang":"en"}`)},
	})
	require.NoError(t, err)

	const cjk = "公园散步"

	// ES keeps the whole CJK run as a single token — cjk_bigram is inert.
	esTokens, err := eng.Analyze(ctx, indexC, "custom_analyzer", cjk)
	require.NoError(t, err)
	assert.Equal(t, []string{cjk}, esTokens,
		"ES custom_analyzer must keep the whole CJK run (cjk_bigram inert on word-typed pattern-tokenizer output)")

	// msganalyzer bigrams the same run into overlapping pairs.
	goTokens := msganalyzer.Analyze(cjk)
	assert.Equal(t, []string{"公园", "园散", "散步"}, goTokens,
		"msganalyzer must bigram the CJK run (the intentional divergence)")
	assert.Greater(t, len(goTokens), len(esTokens),
		"msganalyzer must emit more tokens than ES for a CJK run")
}
