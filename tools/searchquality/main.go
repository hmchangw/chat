// Command searchquality is the offline search-quality harness for the encrypted
// (blind) message-search path. It indexes a small multilingual corpus into a
// plaintext control index (C) and a blind index, runs a per-language query set
// against both, and reports recall@K / Jaccard / RBO of the blind index relative
// to C plus an ES `_analyze`-vs-Go parity divergence count. Run it against a
// compose Elasticsearch; the gate is recall@10 >= 0.95 AND RBO >= 0.90 per
// language.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/blindsearch"
	"github.com/hmchangw/chat/pkg/searchengine"
)

func main() {
	if err := run(); err != nil {
		slog.Error("searchquality failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		esURL      = flag.String("es-url", "http://localhost:9200", "Elasticsearch URL")
		corpusPath = flag.String("corpus", "testdata/corpus.json", "path to corpus.json")
		out        = flag.String("out", "report.md", "output markdown report path")
		blindKey   = flag.String("blind-key", strings.Repeat("ab", 32), "hex blind-index key (32 bytes / 64 hex chars)")
		keyVersion = flag.String("blind-key-version", "v1", "blind-index key version")
		k          = flag.Int("k", 10, "top-K cutoff for recall/Jaccard")
		p          = flag.Float64("p", 0.9, "RBO persistence parameter")
		recallGate = flag.Float64("recall-gate", 0.95, "minimum mean recall@K per language")
		rboGate    = flag.Float64("rbo-gate", 0.90, "minimum mean RBO per language")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	corpusData, err := os.ReadFile(*corpusPath) // #nosec G304 -- operator-supplied corpus path for an offline dev tool
	if err != nil {
		return fmt.Errorf("read corpus: %w", err)
	}
	corpus, err := LoadCorpus(corpusData)
	if err != nil {
		return err
	}

	hasher, err := blindsearch.LoadHasher(*blindKey, *keyVersion)
	if err != nil {
		return fmt.Errorf("load blind hasher: %w", err)
	}

	eng, err := searchengine.New(ctx, searchengine.Config{Backend: "elasticsearch", URL: *esURL})
	if err != nil {
		return fmt.Errorf("connect elasticsearch: %w", err)
	}

	cfg := RunConfig{
		IndexC:     "searchquality-c",
		IndexBlind: "searchquality-blind",
		K:          *k,
		P:          *p,
		RecallGate: *recallGate,
		RBOGate:    *rboGate,
	}

	report, err := Run(ctx, eng, hasher, corpus, cfg, httpRefresh(*esURL, cfg.IndexC, cfg.IndexBlind))
	if err != nil {
		return fmt.Errorf("run harness: %w", err)
	}

	md := renderReport(report)
	if err := os.WriteFile(*out, []byte(md), 0o600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	slog.Info("wrote report", "path", *out, "gatePassed", report.OverallPassed())
	fmt.Print(md)
	return nil
}

// httpRefresh returns a refresh func that forces ES to make the given indices
// searchable via the `_refresh` endpoint (not exposed on the SearchEngine
// interface).
func httpRefresh(esURL string, indices ...string) func(ctx context.Context) error {
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
