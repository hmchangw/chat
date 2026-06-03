//go:build integration

package main

// Integration tests for the encrypted message-search path (arms A and B).
//
// ES is stubbed via httptest (mirroring integration_messages_test.go): the stub
// captures the `_search` request URL + body so the test can assert the query
// targeted the ENC index pattern and matched on `contentBlind`, then returns an
// ENCRYPTED hit (contentBlind / contentEnc / encNonce + metadata, NO plaintext
// `content`). The real searchMessages handler runs over the real router/NATS;
// the enc dependencies are injected directly on the handler struct — the same
// seam main.go uses (handler.hasher/cipher/historyClient).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindidx"
	"github.com/hmchangw/chat/pkg/blindsearch"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/subject"
)

// capturedSearch records what the ES stub saw for the `_search` call.
type capturedSearch struct {
	mu   sync.Mutex
	path string
	body []byte
}

func (c *capturedSearch) record(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = path
	c.body = body
}

func (c *capturedSearch) snapshot() (string, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.body
}

// encStubServer returns an httptest ES stub that records the `_search` request
// and replies with the supplied raw `_search` response body for that endpoint.
func encStubServer(t *testing.T, cap *capturedSearch, searchResponse []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The Elastic Go client performs a product-check handshake on connect
		// and rejects any server not advertising itself as Elasticsearch.
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/_search") {
			cap.record(r.URL.Path, body)
			_, _ = w.Write(searchResponse)
			return
		}
		// Any other endpoint (e.g. the product-check ping) gets an empty 200.
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// encHitResponse builds an ES `_search` response carrying a single ENCRYPTED
// hit — contentBlind/contentEnc/encNonce + metadata, deliberately NO plaintext
// `content` field (the enc index never stores plaintext).
func encHitResponse(t *testing.T, msgID, roomID, blindField string, contentEnc, nonce []byte) []byte {
	t.Helper()
	body := map[string]any{
		"hits": map[string]any{
			"total": map[string]any{"value": 1},
			"hits": []any{map[string]any{"_source": map[string]any{
				"messageId":    msgID,
				"roomId":       roomID,
				"siteId":       testSiteID,
				"userId":       "u1",
				"userAccount":  "alice",
				"contentBlind": blindField,
				"contentEnc":   contentEnc,
				"encNonce":     nonce,
				"createdAt":    "2026-04-01T12:00:00Z",
			}}},
		},
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return data
}

// encFixture wires the real searchMessages handler over the real router/NATS
// with an httptest ES stub and the injected enc dependencies.
type encFixture struct {
	clientNATS *nats.Conn
	captured   *capturedSearch
}

func setupEncFixture(t *testing.T, h *handler, queueGroup string, searchResponse []byte) *encFixture {
	t.Helper()
	captured := &capturedSearch{}
	esStub := encStubServer(t, captured, searchResponse)

	engine, err := searchengine.New(context.Background(), searchengine.Config{Backend: "elasticsearch", URL: esStub.URL})
	require.NoError(t, err)
	h.store = newESStore(engine, testUserRoomIndex)

	clientNATS := setupRouter(t, queueGroup, h.Register)
	return &encFixture{clientNATS: clientNATS, captured: captured}
}

// newEncHandler builds a handler configured for the encrypted path with bench
// mode on (so the per-request Variant override is honored) and the supplied enc
// dependencies injected via the same struct-field seam main.go uses.
func newEncHandler(cache RestrictedRoomCache, hasher *blindidx.Hasher, cipher decrypter, history historyBatchClient) *handler {
	h := newHandler(nil, nil, nil, cache, &handlerConfig{
		SiteID:                  testSiteID,
		DocCounts:               25,
		MaxDocCounts:            100,
		RestrictedRoomsCacheTTL: 5 * time.Minute,
		RecentWindow:            365 * 24 * time.Hour,
		RequestTimeout:          5 * time.Second,
		UserRoomIndex:           testUserRoomIndex,
		SpotlightReadPattern:    "spotlight-*",
		EncDefaultArm:           armC,
		BenchModeEnabled:        true,
		EncIndexPattern:         EncMessageIndexPattern,
	})
	h.hasher = hasher
	h.cipher = cipher
	h.historyClient = history
	return h
}

func TestIntegration_SearchMessages_ArmA_DecrypterContent(t *testing.T) {
	const query = "hello"
	hasher := testHasher(t)
	blindField := blindsearch.Field(hasher, query)

	// Arm A: the decrypter returns a known plaintext for the hit's room.
	cipher := &fakeDecrypter{byRoom: map[string]string{"r1": "decrypted-secret"}}

	cache := newFakeCache()
	cache.store["alice"] = map[string]int64{} // empty restricted map, cache hit

	h := newEncHandler(cache, hasher, cipher, &fakeHistoryBatchClient{})
	resp := encHitResponse(t, "m1", "r1", blindField, []byte("CIPHERTEXT"), []byte("NONCE"))
	f := setupEncFixture(t, h, "search-service-test-enc-a", resp)

	reqBytes, err := json.Marshal(model.SearchMessagesRequest{Query: query, Variant: "A"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchMessages("alice", testSiteID), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var out model.SearchMessagesResponse
	require.NoError(t, json.Unmarshal(msg.Data, &out))

	require.Len(t, out.Messages, 1)
	assert.EqualValues(t, 1, out.Total)
	got := out.Messages[0]
	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "r1", got.RoomID)
	// Content equals the fake decrypter's plaintext — proves arm A decrypted.
	assert.Equal(t, "decrypted-secret", got.Content)

	// The decrypter was called with the hit's ciphertext + nonce.
	require.Len(t, cipher.calls, 1)
	assert.Equal(t, "r1", cipher.calls[0].roomID)
	assert.Equal(t, []byte("CIPHERTEXT"), cipher.calls[0].payload)
	assert.Equal(t, []byte("NONCE"), cipher.calls[0].nonce)

	assertEncQuery(t, f.captured, hasher, query)
}

func TestIntegration_SearchMessages_ArmB_HistoryClientContent(t *testing.T) {
	const query = "hello"
	hasher := testHasher(t)
	blindField := blindsearch.Field(hasher, query)

	// Arm B: history-service returns the plaintext body keyed by message ID.
	history := &fakeHistoryBatchClient{msgs: []cassandra.Message{
		{MessageID: "m1", RoomID: "r1", Msg: "history-secret"},
	}}

	cache := newFakeCache()
	cache.store["alice"] = map[string]int64{}

	h := newEncHandler(cache, hasher, &fakeDecrypter{}, history)
	resp := encHitResponse(t, "m1", "r1", blindField, []byte("CIPHERTEXT"), []byte("NONCE"))
	f := setupEncFixture(t, h, "search-service-test-enc-b", resp)

	reqBytes, err := json.Marshal(model.SearchMessagesRequest{Query: query, Variant: "B"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchMessages("alice", testSiteID), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var out model.SearchMessagesResponse
	require.NoError(t, json.Unmarshal(msg.Data, &out))

	require.Len(t, out.Messages, 1)
	assert.EqualValues(t, 1, out.Total)
	got := out.Messages[0]
	assert.Equal(t, "m1", got.MessageID)
	// Content equals the fake history client's body — proves arm B fetched.
	assert.Equal(t, "history-secret", got.Content)

	// History was asked for the hit's ID under the caller's account.
	require.Len(t, history.callIDs, 1)
	assert.Equal(t, []string{"m1"}, history.callIDs[0])
	assert.Equal(t, []string{"alice"}, history.callAcc)

	assertEncQuery(t, f.captured, hasher, query)
}

// assertEncQuery verifies the captured `_search` request targeted the ENC index
// pattern and matched on `contentBlind` with the blind terms equal to
// blindsearch.Field(hasher, query) — index/query parity.
func assertEncQuery(t *testing.T, captured *capturedSearch, hasher *blindidx.Hasher, query string) {
	t.Helper()
	path, body := captured.snapshot()
	require.NotEmpty(t, path, "ES stub never received a _search request")

	// The index pattern lands in the request URL path (comma-joined). The enc
	// pattern is enc-messages-* (+ the CCS variant); plaintext is messages-*.
	assert.Contains(t, path, "enc-messages", "search must target the enc index pattern, got path %q", path)
	assert.NotContains(t, path, "/messages-", "search must NOT target the plaintext messages index")

	var q struct {
		Query struct {
			Bool struct {
				Must []json.RawMessage `json:"must"`
			} `json:"bool"`
		} `json:"query"`
	}
	require.NoError(t, json.Unmarshal(body, &q))
	require.NotEmpty(t, q.Query.Bool.Must, "enc query must have a content must-clause")

	// Locate the contentBlind match clause and assert its blinded terms equal
	// what blindsearch.Field produces for this hasher (parity invariant).
	want := blindsearch.Field(hasher, query)
	require.NotEmpty(t, want, "blind field for query must be non-empty")

	var foundBlind bool
	for _, raw := range q.Query.Bool.Must {
		var clause struct {
			Match struct {
				ContentBlind struct {
					Query string `json:"query"`
				} `json:"contentBlind"`
			} `json:"match"`
		}
		if err := json.Unmarshal(raw, &clause); err != nil {
			continue
		}
		if clause.Match.ContentBlind.Query != "" {
			foundBlind = true
			assert.Equal(t, want, clause.Match.ContentBlind.Query,
				"query-side blind terms must equal blindsearch.Field(hasher, query)")
		}
	}
	assert.True(t, foundBlind, "enc query must match on contentBlind, body=%s", string(body))
}
