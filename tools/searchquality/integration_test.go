//go:build integration

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindsearch"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTests(m) }

// TestRun_EndToEnd asserts the quality harness runs against a real ES, produces
// per-language metrics in [0,1], runs the parity oracle, and writes the report
// file. It deliberately does NOT hard-gate on the 0.95/0.90 thresholds — the
// small corpus may not pass deterministically and that judgement is the user's
// to make from the report.
func TestRun_EndToEnd(t *testing.T) {
	esURL := testutil.Elasticsearch(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	eng, err := searchengine.New(ctx, searchengine.Config{Backend: "elasticsearch", URL: esURL})
	require.NoError(t, err)

	hasher, err := blindsearch.LoadHasher(strings.Repeat("ab", 32), "v1")
	require.NoError(t, err)

	data, err := os.ReadFile("testdata/corpus.json")
	require.NoError(t, err)
	corpus, err := LoadCorpus(data)
	require.NoError(t, err)

	// Per-test isolated index names + DELETE on cleanup.
	indexC := testutil.ElasticsearchIndex(t, "searchquality-c")
	indexBlind := testutil.ElasticsearchIndex(t, "searchquality-blind")

	cfg := RunConfig{
		IndexC:     indexC,
		IndexBlind: indexBlind,
		K:          10,
		P:          0.9,
		RecallGate: 0.95,
		RBOGate:    0.90,
	}

	report, err := Run(ctx, eng, hasher, corpus, cfg, refreshIndices(t, esURL, indexC, indexBlind))
	require.NoError(t, err)

	// Metrics are produced for every language in the corpus.
	require.Len(t, report.Langs, len(corpus.Langs()))
	for _, l := range report.Langs {
		assert.NotEmpty(t, l.Lang)
		assert.Positive(t, l.Queries)
		assertInUnitInterval(t, l.Recall, "recall", l.Lang)
		assertInUnitInterval(t, l.Jaccard, "jaccard", l.Lang)
		assertInUnitInterval(t, l.RBO, "rbo", l.Lang)
		assert.GreaterOrEqual(t, l.ParityDivergences, 0)
	}

	// The report file is written and contains the expected structure.
	reportPath := t.TempDir() + "/report.md"
	md := renderReport(report)
	require.NoError(t, os.WriteFile(reportPath, []byte(md), 0o600))

	written, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	assert.Contains(t, string(written), "# Encrypted Search Quality Report")
	assert.Contains(t, string(written), "## Gate verdict:")
	assert.Contains(t, string(written), "Recall@10")

	t.Logf("quality report:\n%s", md)
}

func assertInUnitInterval(t *testing.T, v float64, metric, lang string) {
	t.Helper()
	assert.GreaterOrEqual(t, v, 0.0, "%s for %s below 0", metric, lang)
	assert.LessOrEqual(t, v, 1.0, "%s for %s above 1", metric, lang)
}

// refreshIndices returns a refresh func that forces ES to make the indices
// searchable before queries run.
func refreshIndices(t *testing.T, esURL string, indices ...string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		for _, idx := range indices {
			url := strings.TrimRight(esURL, "/") + "/" + idx + "/_refresh"
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
			if err != nil {
				return fmt.Errorf("build refresh request: %w", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("refresh %s: %w", idx, err)
			}
			_ = resp.Body.Close()
		}
		return nil
	}
}
