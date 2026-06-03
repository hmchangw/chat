//go:build integration

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/atrest"
	"github.com/hmchangw/chat/pkg/blindsearch"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// fakeEncCipher is a deterministic, dependency-free encCipher used to drive the
// enc dual-write end-to-end without standing up Vault or Mongo. The ciphertext
// is just a tagged copy of the plaintext and the nonce is a fixed 12-byte blob —
// enough to assert that contentEnc is persisted (binary, not indexed) and the
// plaintext never reaches Elasticsearch.
type fakeEncCipher struct{}

func (fakeEncCipher) Encrypt(_ context.Context, _ string, fields atrest.EncryptedFields) ([]byte, atrest.EncMeta, error) {
	return []byte("ct:" + fields.Msg), atrest.EncMeta{Nonce: []byte("nonce12bytes")}, nil
}

// encTestKeyHex is a 32-byte blind key supplied as 64 hex chars (mirrors the
// pkg/blindsearch tests). The exact bytes are irrelevant — index-time and
// query-time tokenization run through the same hasher, so contentBlind is
// deterministic for a given key.
var encTestKeyHex = strings.Repeat("ab", 32)

// TestSearchSyncEncIntegration proves search-sync-worker writes the encrypted
// parallel index end-to-end: a real MessageEvent published to MESSAGES_CANONICAL
// is consumed by the real handler/collection path (enc enabled, fake cipher) and
// lands in the enc index with blinded tokens + ciphertext and NO plaintext.
func TestSearchSyncEncIntegration(t *testing.T) {
	esURL := setupElasticsearch(t)
	js, _ := setupNATSJetStream(t)
	ctx := context.Background()

	// Unique prefixes per test — shared ES, isolation is the caller's job.
	// The two stripped index patterns must NOT overlap: the enc prefix strips
	// to "enc-itest-*", so the plaintext prefix is deliberately rooted under a
	// disjoint base ("itest-plain-*") to avoid the same-priority ES template
	// pattern collision (enc-itest-* would otherwise also match enc-itest-plain-*).
	plaintextPrefix := "itest-plain-v1"
	encPrefix := "enc-itest-v1"

	engine, err := searchengine.New(ctx, searchengine.Config{Backend: "elasticsearch", URL: esURL})
	require.NoError(t, err, "create search engine")

	waitForClusterGreen(t, esURL, 120*time.Second)

	hasher, err := blindsearch.LoadHasher(encTestKeyHex, "v1")
	require.NoError(t, err, "load blind hasher")

	coll := newMessageCollectionEnc(plaintextPrefix, time.Time{}, encOptions{
		enabled:     true,
		indexPrefix: encPrefix,
		keyVersion:  "v1",
		hasher:      hasher,
		cipher:      fakeEncCipher{},
	})

	// Upsert BOTH the primary plaintext template and the enc aux template
	// (single-node-friendly settings so a one-node ES test container stays green).
	require.NoError(t,
		engine.UpsertTemplate(ctx, coll.TemplateName(), overrideIndexSettings(coll.TemplateBody())),
		"upsert primary template")
	aux := coll.AuxTemplates()
	require.Len(t, aux, 1, "enc collection must expose exactly one aux template")
	require.NoError(t,
		engine.UpsertTemplate(ctx, aux[0].Name, overrideIndexSettings(aux[0].Body)),
		"upsert enc aux template")

	// Pre-create both monthly indices so shard allocation completes before bulk.
	plaintextIndex := plaintextPrefix + "-2026-01"
	encIndex := encPrefix + "-2026-01"
	preCreateIndex(t, esURL, plaintextIndex)
	preCreateIndex(t, esURL, encIndex)
	waitForClusterGreen(t, esURL, 120*time.Second)

	// --- NATS stream + a single canonical message ---
	siteID := "site-enc-test"
	canonicalCfg := stream.MessagesCanonical(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonicalCfg.Name,
		Subjects: canonicalCfg.Subjects,
	})
	require.NoError(t, err, "create stream")

	content := "Hello, Encrypted World!"
	created := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventCreated,
		SiteID:    siteID,
		Timestamp: created.UnixMilli(),
		Message: model.Message{
			ID: "enc-msg-1", RoomID: "room-enc-1", UserID: "u1", UserAccount: "alice",
			Content: content, CreatedAt: created,
		},
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	_, err = js.Publish(ctx, subject.MsgCanonicalCreated(siteID), data)
	require.NoError(t, err, "publish canonical message")

	// --- Run the real consumer/handler path ---
	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
		Durable:   "search-sync-worker-enc-test",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err, "create consumer")

	handler := NewHandler(&engineAdapter{engine: engine}, coll, 100)

	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(10*time.Second))
	require.NoError(t, err, "fetch messages")
	for msg := range batch.Messages() {
		handler.Add(ctx, msg)
	}
	handler.Flush(ctx)

	refreshIndex(t, esURL, encPrefix+"-*")

	// --- Assertions on the enc index ---
	t.Run("doc exists in enc index with msg id", func(t *testing.T) {
		doc := getDoc(t, esURL, encIndex, "enc-msg-1")
		require.NotNil(t, doc, "enc doc enc-msg-1 must exist in %s", encIndex)
		assert.Equal(t, "enc-msg-1", doc["messageId"])
		assert.Equal(t, "room-enc-1", doc["roomId"])
	})

	t.Run("contentBlind matches blindsearch.Field", func(t *testing.T) {
		doc := getDoc(t, esURL, encIndex, "enc-msg-1")
		require.NotNil(t, doc)
		want := blindsearch.Field(hasher, content)
		assert.NotEmpty(t, want, "sanity: blinded field is non-empty for this content")
		assert.Equal(t, want, doc["contentBlind"], "stored contentBlind must equal blindsearch.Field")
	})

	t.Run("contentEnc present and plaintext content absent", func(t *testing.T) {
		doc := getDoc(t, esURL, encIndex, "enc-msg-1")
		require.NotNil(t, doc)

		// contentEnc round-trips through ES as base64 (binary mapping).
		enc, ok := doc["contentEnc"].(string)
		require.True(t, ok, "contentEnc must be present in _source")
		assert.NotEmpty(t, enc, "contentEnc must be non-empty")

		assert.Equal(t, "v1", doc["blindKeyVersion"])

		// Plaintext content field must NOT exist on the encrypted index.
		_, hasContent := doc["content"]
		assert.False(t, hasContent, "plaintext content must be absent from enc _source")
	})

	t.Run("enc template was created", func(t *testing.T) {
		mapping, mErr := engine.GetIndexMapping(ctx, encIndex)
		require.NoError(t, mErr, "get enc index mapping")
		require.NotNil(t, mapping)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(mapping, &parsed))
		// The mapping is keyed by index name at the top level.
		idxMap, ok := parsed[encIndex].(map[string]any)
		require.True(t, ok, "mapping must be keyed by index name %s: %s", encIndex, mapping)
		mappings, ok := idxMap["mappings"].(map[string]any)
		require.True(t, ok, "index entry must carry mappings")
		props, ok := mappings["properties"].(map[string]any)
		require.True(t, ok, "mappings must carry properties")

		cb, ok := props["contentBlind"].(map[string]any)
		require.True(t, ok, "enc template must map contentBlind")
		assert.Equal(t, "text", cb["type"])

		_, mappedContent := props["content"]
		assert.False(t, mappedContent, "enc template must not map plaintext content")
	})
}
