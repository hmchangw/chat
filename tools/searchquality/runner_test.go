package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindsearch"
)

var testBlindKey = strings.Repeat("ab", 32)

func TestLoadCorpus(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		data := []byte(`{"docs":[{"id":"d1","lang":"english","content":"hi"}],"queries":[{"id":"q1","lang":"english","text":"hi","relevant":["d1"]}]}`)
		c, err := LoadCorpus(data)
		require.NoError(t, err)
		assert.Len(t, c.Docs, 1)
		assert.Len(t, c.Queries, 1)
	})
	t.Run("no docs", func(t *testing.T) {
		_, err := LoadCorpus([]byte(`{"docs":[],"queries":[{"id":"q1"}]}`))
		assert.Error(t, err)
	})
	t.Run("no queries", func(t *testing.T) {
		_, err := LoadCorpus([]byte(`{"docs":[{"id":"d1"}],"queries":[]}`))
		assert.Error(t, err)
	})
	t.Run("malformed", func(t *testing.T) {
		_, err := LoadCorpus([]byte(`not json`))
		assert.Error(t, err)
	})
}

func TestCorpus_Langs(t *testing.T) {
	c := &Corpus{Queries: []Query{
		{Lang: "english"}, {Lang: "cjk"}, {Lang: "english"}, {Lang: "html"},
	}}
	assert.Equal(t, []string{"cjk", "english", "html"}, c.Langs())
}

func TestEmbeddedCorpusFile_Valid(t *testing.T) {
	data, err := os.ReadFile("testdata/corpus.json")
	require.NoError(t, err)
	c, err := LoadCorpus(data)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(c.Docs), 15)
	// Every query's relevant ids must reference real doc ids.
	docIDs := map[string]struct{}{}
	for _, d := range c.Docs {
		docIDs[d.ID] = struct{}{}
	}
	for _, q := range c.Queries {
		assert.NotEmpty(t, q.Relevant, "query %s has no relevant docs", q.ID)
		for _, r := range q.Relevant {
			_, ok := docIDs[r]
			assert.True(t, ok, "query %s references unknown doc %s", q.ID, r)
		}
	}
}

func TestPlaintextTemplateBody_HasCustomAnalyzer(t *testing.T) {
	body := plaintextTemplateBody("searchquality-c")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	tmpl := parsed["template"].(map[string]any)
	settings, _ := json.Marshal(tmpl["settings"])
	assert.Contains(t, string(settings), "custom_analyzer")
	assert.Contains(t, string(settings), "underscore_preserving")

	props := tmpl["mappings"].(map[string]any)["properties"].(map[string]any)
	content := props["content"].(map[string]any)
	assert.Equal(t, "text", content["type"])
	assert.Equal(t, "custom_analyzer", content["analyzer"])
}

func TestBlindTemplateBody_WhitespaceAnalyzer_NoCustom(t *testing.T) {
	body := blindTemplateBody("searchquality-blind")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	tmpl := parsed["template"].(map[string]any)
	settings, _ := json.Marshal(tmpl["settings"])
	assert.NotContains(t, string(settings), "custom_analyzer")

	props := tmpl["mappings"].(map[string]any)["properties"].(map[string]any)
	cb := props["contentBlind"].(map[string]any)
	assert.Equal(t, "text", cb["type"])
	assert.Equal(t, "whitespace", cb["analyzer"])
	_, hasContent := props["content"]
	assert.False(t, hasContent)
}

func TestMatchQueryBody(t *testing.T) {
	t.Run("non-empty builds AND match", func(t *testing.T) {
		body := matchQueryBody("content", "brown fox", 10)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(body, &parsed))
		assert.EqualValues(t, 10, parsed["size"])
		match := parsed["query"].(map[string]any)["match"].(map[string]any)["content"].(map[string]any)
		assert.Equal(t, "brown fox", match["query"])
		assert.Equal(t, "AND", match["operator"])
	})
	t.Run("empty value -> match_none", func(t *testing.T) {
		body := matchQueryBody("contentBlind", "", 10)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(body, &parsed))
		_, ok := parsed["query"].(map[string]any)["match_none"]
		assert.True(t, ok)
	})
}

func TestCorpusBulkActions_BothIndices(t *testing.T) {
	h, err := blindsearch.LoadHasher(testBlindKey, "v1")
	require.NoError(t, err)
	c := &Corpus{Docs: []Doc{{ID: "d1", Lang: "english", Content: "Hello World"}}}

	actions := corpusBulkActions(c, h, "idx-c", "idx-blind")
	require.Len(t, actions, 2)

	// First action: plaintext C.
	assert.Equal(t, "idx-c", actions[0].Index)
	assert.Equal(t, "d1", actions[0].DocID)
	assert.EqualValues(t, 1, actions[0].Version)
	var plain map[string]any
	require.NoError(t, json.Unmarshal(actions[0].Doc, &plain))
	assert.Equal(t, "Hello World", plain["content"])

	// Second action: blind index with space-joined hashed tokens.
	assert.Equal(t, "idx-blind", actions[1].Index)
	var blind map[string]any
	require.NoError(t, json.Unmarshal(actions[1].Doc, &blind))
	assert.Equal(t, blindsearch.Field(h, "Hello World"), blind["contentBlind"])
	_, hasContent := blind["content"]
	assert.False(t, hasContent)
}

func TestEqualTokens(t *testing.T) {
	assert.True(t, equalTokens([]string{"a", "b"}, []string{"a", "b"}))
	assert.False(t, equalTokens([]string{"a"}, []string{"a", "b"}))
	assert.False(t, equalTokens([]string{"a", "b"}, []string{"a", "c"}))
	assert.True(t, equalTokens(nil, []string{}))
}

func TestTopK(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, topK([]string{"a", "b", "c"}, 2))
	assert.Equal(t, []string{"a"}, topK([]string{"a"}, 5))
	assert.Empty(t, topK(nil, 3))
}

func TestParityDoc_String(t *testing.T) {
	p := parityDoc{DocID: "d1", Go: []string{"a"}, ES: []string{"b"}}
	s := p.String()
	assert.Contains(t, s, "d1")
	assert.Contains(t, s, "go=[a]")
	assert.Contains(t, s, "es=[b]")
}
