package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hmchangw/chat/pkg/blindidx"
	"github.com/hmchangw/chat/pkg/blindsearch"
	"github.com/hmchangw/chat/pkg/msganalyzer"
	"github.com/hmchangw/chat/pkg/searchengine"
)

// Doc is a single corpus document.
type Doc struct {
	ID      string `json:"id"`
	Lang    string `json:"lang"`
	Content string `json:"content"`
}

// Query is a single corpus query with its known-relevant doc ids.
type Query struct {
	ID       string   `json:"id"`
	Lang     string   `json:"lang"`
	Text     string   `json:"text"`
	Relevant []string `json:"relevant"`
}

// Corpus is the parsed testdata/corpus.json.
type Corpus struct {
	Docs    []Doc   `json:"docs"`
	Queries []Query `json:"queries"`
}

// LoadCorpus parses a corpus JSON blob.
func LoadCorpus(data []byte) (*Corpus, error) {
	var c Corpus
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("unmarshal corpus: %w", err)
	}
	if len(c.Docs) == 0 {
		return nil, fmt.Errorf("corpus has no docs")
	}
	if len(c.Queries) == 0 {
		return nil, fmt.Errorf("corpus has no queries")
	}
	return &c, nil
}

// Langs returns the distinct language labels in stable sorted order.
func (c *Corpus) Langs() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, q := range c.Queries {
		if _, ok := seen[q.Lang]; ok {
			continue
		}
		seen[q.Lang] = struct{}{}
		out = append(out, q.Lang)
	}
	sort.Strings(out)
	return out
}

// plaintextTemplateBody returns a minimal index template whose `content` field
// uses the real production `custom_analyzer` (html_strip -> pattern tokenizer ->
// word_delimiter_graph -> cjk_bigram -> lowercase). The analysis block is copied
// verbatim from search-sync-worker/messages.go so the harness measures against
// the true analyzer. Single shard / zero replicas keep a single-node ES green.
func plaintextTemplateBody(indexC string) json.RawMessage {
	tmpl := map[string]any{
		"index_patterns": []string{indexC},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   1,
					"number_of_replicas": 0,
					"refresh_interval":   "1s",
				},
				"analysis": map[string]any{
					"analyzer": map[string]any{
						"custom_analyzer": map[string]any{
							"type":        "custom",
							"tokenizer":   "underscore_preserving",
							"filter":      []string{"underscore_subword", "cjk_bigram", "lowercase"},
							"char_filter": []string{"html_strip"},
						},
					},
					"tokenizer": map[string]any{
						"underscore_preserving": map[string]any{
							"type":    "pattern",
							"pattern": `[\s,;!?()\[\]{}"'<>]+`,
						},
					},
					"filter": map[string]any{
						"underscore_subword": map[string]any{
							"type":                 "word_delimiter_graph",
							"split_on_case_change": false,
							"split_on_numerics":    false,
							"preserve_original":    true,
						},
					},
				},
			},
			"mappings": map[string]any{
				"dynamic": false,
				"properties": map[string]any{
					"content": map[string]any{"type": "text", "analyzer": "custom_analyzer"},
					"lang":    map[string]any{"type": "keyword"},
				},
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}

// blindTemplateBody returns a minimal index template whose `contentBlind` field
// uses the built-in `whitespace` analyzer (mirrors the encrypted production
// template in search-sync-worker/encindex.go). The blind field stores the
// space-joined HMAC-blinded tokens produced by pkg/blindsearch.
func blindTemplateBody(indexBlind string) json.RawMessage {
	tmpl := map[string]any{
		"index_patterns": []string{indexBlind},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   1,
					"number_of_replicas": 0,
					"refresh_interval":   "1s",
				},
			},
			"mappings": map[string]any{
				"dynamic": false,
				"properties": map[string]any{
					"contentBlind": map[string]any{"type": "text", "analyzer": "whitespace"},
					"lang":         map[string]any{"type": "keyword"},
				},
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}

// matchQueryBody builds a `match` query (operator AND) on field for value,
// sized to topN, returning only the `_id` (and lang) — used for both the
// plaintext content query and the blind contentBlind query.
func matchQueryBody(field, value string, topN int) json.RawMessage {
	if value == "" {
		// Empty analyzed query -> match nothing (mirrors the enc-path
		// match_none guard) rather than match_all.
		body, _ := json.Marshal(map[string]any{
			"size":  topN,
			"query": map[string]any{"match_none": map[string]any{}},
		})
		return body
	}
	body, _ := json.Marshal(map[string]any{
		"size": topN,
		"query": map[string]any{
			"match": map[string]any{
				field: map[string]any{"query": value, "operator": "AND"},
			},
		},
	})
	return body
}

// searchHits is the slim ES `_search` response shape this harness reads.
type searchHits struct {
	Hits struct {
		Hits []struct {
			ID string `json:"_id"`
		} `json:"hits"`
	} `json:"hits"`
}

// rankedIDs runs a search and returns the hit ids in ES rank order.
func rankedIDs(ctx context.Context, eng searchengine.SearchEngine, index string, body json.RawMessage) ([]string, error) {
	raw, err := eng.Search(ctx, []string{index}, body)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", index, err)
	}
	var parsed searchHits
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	ids := make([]string, 0, len(parsed.Hits.Hits))
	for _, h := range parsed.Hits.Hits {
		ids = append(ids, h.ID)
	}
	return ids, nil
}

// corpusBulkActions builds the index actions for both C (plaintext content) and
// the blind index (blindsearch.Field tokens).
func corpusBulkActions(c *Corpus, h *blindidx.Hasher, indexC, indexBlind string) []searchengine.BulkAction {
	actions := make([]searchengine.BulkAction, 0, 2*len(c.Docs))
	for _, d := range c.Docs {
		plain, _ := json.Marshal(map[string]any{"content": d.Content, "lang": d.Lang})
		blind, _ := json.Marshal(map[string]any{
			"contentBlind": blindsearch.Field(h, d.Content),
			"lang":         d.Lang,
		})
		// Version must be a positive ES external version; the harness writes
		// each doc exactly once, so a constant 1 is sufficient.
		actions = append(actions,
			searchengine.BulkAction{Action: searchengine.ActionIndex, Index: indexC, DocID: d.ID, Version: 1, Doc: plain},
			searchengine.BulkAction{Action: searchengine.ActionIndex, Index: indexBlind, DocID: d.ID, Version: 1, Doc: blind},
		)
	}
	return actions
}

// parityDoc records a single corpus doc whose Go analyzer output diverged from
// ES `_analyze`.
type parityDoc struct {
	DocID string
	Lang  string
	Go    []string
	ES    []string
}

// String renders a compact one-line divergence example for the report.
func (p parityDoc) String() string {
	return fmt.Sprintf("%s: go=%v es=%v", p.DocID, p.Go, p.ES)
}

// checkParity compares msganalyzer.Analyze(content) against ES `_analyze`
// (custom_analyzer scoped to indexC) for every corpus doc, returning the
// divergent docs. Empty-content docs are skipped (no tokens either side).
func checkParity(ctx context.Context, eng searchengine.SearchEngine, indexC string, c *Corpus) ([]parityDoc, error) {
	var diverged []parityDoc
	for _, d := range c.Docs {
		goTokens := msganalyzer.Analyze(d.Content)
		esTokens, err := eng.Analyze(ctx, indexC, "custom_analyzer", d.Content)
		if err != nil {
			return nil, fmt.Errorf("analyze doc %s: %w", d.ID, err)
		}
		if !equalTokens(goTokens, esTokens) {
			diverged = append(diverged, parityDoc{DocID: d.ID, Lang: d.Lang, Go: goTokens, ES: esTokens})
		}
	}
	return diverged, nil
}

func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RunConfig parameterizes a quality run.
type RunConfig struct {
	IndexC     string
	IndexBlind string
	K          int
	P          float64
	RecallGate float64
	RBOGate    float64
}

// Run executes the full quality harness: upsert templates, bulk-index the
// corpus into C and blind, refresh, run every query against both indices,
// run the parity oracle, and aggregate per-language metrics into a Report.
//
// refresh is injected so the runner does not depend on a refresh method
// outside the SearchEngine interface (the integration test passes an HTTP
// refresh; unit tests can pass a no-op).
func Run(ctx context.Context, eng searchengine.SearchEngine, h *blindidx.Hasher, c *Corpus, cfg RunConfig, refresh func(ctx context.Context) error) (Report, error) {
	if err := eng.UpsertTemplate(ctx, cfg.IndexC+"_template", plaintextTemplateBody(cfg.IndexC)); err != nil {
		return Report{}, fmt.Errorf("upsert plaintext template: %w", err)
	}
	if err := eng.UpsertTemplate(ctx, cfg.IndexBlind+"_template", blindTemplateBody(cfg.IndexBlind)); err != nil {
		return Report{}, fmt.Errorf("upsert blind template: %w", err)
	}

	results, err := eng.Bulk(ctx, corpusBulkActions(c, h, cfg.IndexC, cfg.IndexBlind))
	if err != nil {
		return Report{}, fmt.Errorf("bulk index corpus: %w", err)
	}
	for i, r := range results {
		if r.ErrorType != "" {
			return Report{}, fmt.Errorf("bulk item %d failed: %s: %s", i, r.ErrorType, r.Error)
		}
	}

	if refresh != nil {
		if err := refresh(ctx); err != nil {
			return Report{}, fmt.Errorf("refresh indices: %w", err)
		}
	}

	diverged, err := checkParity(ctx, eng, cfg.IndexC, c)
	if err != nil {
		return Report{}, fmt.Errorf("parity oracle: %w", err)
	}

	rep, err := aggregate(ctx, eng, h, c, cfg, diverged)
	if err != nil {
		return Report{}, err
	}
	return rep, nil
}

// aggregate runs every query against both indices and folds the per-query
// metrics into per-language means.
func aggregate(ctx context.Context, eng searchengine.SearchEngine, h *blindidx.Hasher, c *Corpus, cfg RunConfig, diverged []parityDoc) (Report, error) {
	type acc struct {
		recall, jaccard, rbo float64
		n                    int
	}
	accs := map[string]*acc{}

	for _, q := range c.Queries {
		cBody := matchQueryBody("content", strings.TrimSpace(q.Text), cfg.K)
		blindBody := matchQueryBody("contentBlind", blindsearch.Field(h, q.Text), cfg.K)

		cRanked, err := rankedIDs(ctx, eng, cfg.IndexC, cBody)
		if err != nil {
			return Report{}, fmt.Errorf("query %s on C: %w", q.ID, err)
		}
		blindRanked, err := rankedIDs(ctx, eng, cfg.IndexBlind, blindBody)
		if err != nil {
			return Report{}, fmt.Errorf("query %s on blind: %w", q.ID, err)
		}

		a := accs[q.Lang]
		if a == nil {
			a = &acc{}
			accs[q.Lang] = a
		}
		a.recall += RecallAtK(q.Relevant, blindRanked, cfg.K)
		a.jaccard += Jaccard(topK(cRanked, cfg.K), topK(blindRanked, cfg.K))
		a.rbo += RBO(cRanked, blindRanked, cfg.P)
		a.n++
	}

	parityByLang := map[string][]parityDoc{}
	for _, d := range diverged {
		parityByLang[d.Lang] = append(parityByLang[d.Lang], d)
	}

	rep := Report{K: cfg.K, P: cfg.P, RecallGate: cfg.RecallGate, RBOGate: cfg.RBOGate}
	for _, lang := range c.Langs() {
		a := accs[lang]
		if a == nil || a.n == 0 {
			continue
		}
		pds := parityByLang[lang]
		examples := make([]string, 0, len(pds))
		for i, pd := range pds {
			if i >= 3 {
				break
			}
			examples = append(examples, pd.String())
		}
		rep.Langs = append(rep.Langs, LangResult{
			Lang:              lang,
			Queries:           a.n,
			Recall:            a.recall / float64(a.n),
			Jaccard:           a.jaccard / float64(a.n),
			RBO:               a.rbo / float64(a.n),
			ParityDivergences: len(pds),
			ParityExamples:    examples,
		})
	}
	return rep, nil
}

func topK(ids []string, k int) []string {
	if k > len(ids) {
		k = len(ids)
	}
	return ids[:k]
}
