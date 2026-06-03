package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindsearch"
	"github.com/hmchangw/chat/pkg/searchengine"
)

// fakeEngine is a hand-written SearchEngine stub that lets the runner be
// exercised without a real Elasticsearch. It returns the same ranked id list
// for every search and a fixed analyze token stream.
type fakeEngine struct {
	bulkErr     error
	bulkResults []searchengine.BulkResult
	searchErr   error
	searchRaw   json.RawMessage
	analyzeErr  error
	analyzeToks map[string][]string // keyed by analyzed text

	upsertedTemplates []string
	searchCalls       int
}

func (f *fakeEngine) Ping(context.Context) error { return nil }

func (f *fakeEngine) Bulk(context.Context, []searchengine.BulkAction) ([]searchengine.BulkResult, error) {
	return f.bulkResults, f.bulkErr
}

func (f *fakeEngine) UpsertTemplate(_ context.Context, name string, _ json.RawMessage) error {
	f.upsertedTemplates = append(f.upsertedTemplates, name)
	return nil
}

func (f *fakeEngine) GetIndexMapping(context.Context, string) (json.RawMessage, error) {
	return nil, nil
}

func (f *fakeEngine) Search(context.Context, []string, json.RawMessage) (json.RawMessage, error) {
	f.searchCalls++
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchRaw, nil
}

func (f *fakeEngine) GetDoc(context.Context, string, string) (json.RawMessage, bool, error) {
	return nil, false, nil
}

func (f *fakeEngine) Analyze(_ context.Context, _, _, text string) ([]string, error) {
	if f.analyzeErr != nil {
		return nil, f.analyzeErr
	}
	if toks, ok := f.analyzeToks[text]; ok {
		return toks, nil
	}
	return nil, nil
}

func hitsJSON(ids ...string) json.RawMessage {
	var sb strings.Builder
	sb.WriteString(`{"hits":{"hits":[`)
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"_id":"` + id + `"}`)
	}
	sb.WriteString(`]}}`)
	return json.RawMessage(sb.String())
}

func smallCorpus() *Corpus {
	return &Corpus{
		Docs: []Doc{
			{ID: "d1", Lang: "english", Content: "hello world"},
			{ID: "d2", Lang: "english", Content: "goodbye world"},
		},
		Queries: []Query{
			{ID: "q1", Lang: "english", Text: "world", Relevant: []string{"d1", "d2"}},
		},
	}
}

func TestRun_WithFakeEngine_ProducesReport(t *testing.T) {
	h, err := blindsearch.LoadHasher(testBlindKey, "v1")
	require.NoError(t, err)

	eng := &fakeEngine{
		bulkResults: []searchengine.BulkResult{{Status: 201}, {Status: 201}, {Status: 201}, {Status: 201}},
		searchRaw:   hitsJSON("d1", "d2"),
		analyzeToks: map[string][]string{
			"hello world":   {"hello", "world"},
			"goodbye world": {"goodbye", "world"},
		},
	}

	cfg := RunConfig{IndexC: "c", IndexBlind: "b", K: 10, P: 0.9, RecallGate: 0.95, RBOGate: 0.90}
	rep, err := Run(context.Background(), eng, h, smallCorpus(), cfg, nil)
	require.NoError(t, err)

	require.Len(t, rep.Langs, 1)
	en := rep.Langs[0]
	assert.Equal(t, "english", en.Lang)
	assert.InDelta(t, 1.0, en.Recall, 1e-9)
	assert.InDelta(t, 1.0, en.RBO, 1e-9)
	assert.Equal(t, 0, en.ParityDivergences)
	assert.Len(t, eng.upsertedTemplates, 2)
	// 2 searches per query (C + blind).
	assert.Equal(t, 2, eng.searchCalls)
}

func TestRun_BulkItemError(t *testing.T) {
	h, err := blindsearch.LoadHasher(testBlindKey, "v1")
	require.NoError(t, err)
	eng := &fakeEngine{
		bulkResults: []searchengine.BulkResult{{Status: 400, ErrorType: "mapper_parsing_exception", Error: "bad"}},
	}
	_, err = Run(context.Background(), eng, h, smallCorpus(),
		RunConfig{IndexC: "c", IndexBlind: "b", K: 10, P: 0.9}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mapper_parsing_exception")
}

func TestRun_BulkError(t *testing.T) {
	h, _ := blindsearch.LoadHasher(testBlindKey, "v1")
	eng := &fakeEngine{bulkErr: errors.New("boom")}
	_, err := Run(context.Background(), eng, h, smallCorpus(),
		RunConfig{IndexC: "c", IndexBlind: "b", K: 10, P: 0.9}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bulk index corpus")
}

func TestRun_RefreshError(t *testing.T) {
	h, _ := blindsearch.LoadHasher(testBlindKey, "v1")
	eng := &fakeEngine{bulkResults: []searchengine.BulkResult{{Status: 201}}}
	refresh := func(context.Context) error { return errors.New("refresh boom") }
	_, err := Run(context.Background(), eng, h, smallCorpus(),
		RunConfig{IndexC: "c", IndexBlind: "b", K: 10, P: 0.9}, refresh)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh indices")
}

func TestRun_ParityDivergenceRecorded(t *testing.T) {
	h, _ := blindsearch.LoadHasher(testBlindKey, "v1")
	eng := &fakeEngine{
		bulkResults: []searchengine.BulkResult{{Status: 201}, {Status: 201}, {Status: 201}, {Status: 201}},
		searchRaw:   hitsJSON("d1", "d2"),
		// ES returns different tokens than the Go analyzer -> divergence.
		analyzeToks: map[string][]string{
			"hello world":   {"hello"},
			"goodbye world": {"goodbye"},
		},
	}
	rep, err := Run(context.Background(), eng, h, smallCorpus(),
		RunConfig{IndexC: "c", IndexBlind: "b", K: 10, P: 0.9, RecallGate: 0.95, RBOGate: 0.90}, nil)
	require.NoError(t, err)
	require.Len(t, rep.Langs, 1)
	assert.Equal(t, 2, rep.Langs[0].ParityDivergences)
	assert.NotEmpty(t, rep.Langs[0].ParityExamples)
}

func TestRun_SearchError(t *testing.T) {
	h, _ := blindsearch.LoadHasher(testBlindKey, "v1")
	eng := &fakeEngine{
		bulkResults: []searchengine.BulkResult{{Status: 201}, {Status: 201}, {Status: 201}, {Status: 201}},
		searchErr:   errors.New("search boom"),
		analyzeToks: map[string][]string{"hello world": {"hello", "world"}, "goodbye world": {"goodbye", "world"}},
	}
	_, err := Run(context.Background(), eng, h, smallCorpus(),
		RunConfig{IndexC: "c", IndexBlind: "b", K: 10, P: 0.9}, nil)
	require.Error(t, err)
}

func TestRankedIDs_DecodesHits(t *testing.T) {
	eng := &fakeEngine{searchRaw: hitsJSON("a", "b", "c")}
	ids, err := rankedIDs(context.Background(), eng, "idx", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, ids)
}

func TestHTTPRefresh(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := httpRefresh(srv.URL, "idx-a", "idx-b")(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"/idx-a/_refresh", "/idx-b/_refresh"}, paths)
}
