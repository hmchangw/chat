//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

func setupElasticsearch(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "elasticsearch:8.17.0",
			ExposedPorts: []string{"9200/tcp"},
			Env: map[string]string{
				"discovery.type":         "single-node",
				"xpack.security.enabled": "false",
				"cluster.routing.allocation.disk.threshold_enabled": "false",
				"ES_JAVA_OPTS": "-Xms512m -Xmx512m",
			},
			WaitingFor: wait.ForAll(
				wait.ForHTTP("/").WithPort("9200/tcp").WithStartupTimeout(120*time.Second),
				wait.ForHTTP("/_cluster/health?wait_for_status=yellow&timeout=60s").
					WithPort("9200/tcp").
					WithStartupTimeout(120*time.Second),
			),
		},
		Started: true,
	})
	require.NoError(t, err, "start elasticsearch")
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "9200")
	require.NoError(t, err)

	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

func setupNATSJetStream(t *testing.T) (jetstream.JetStream, *nats.Conn) {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2.11-alpine",
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"--jetstream"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err, "start nats")
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "4222")
	require.NoError(t, err)

	natsURL := fmt.Sprintf("nats://%s:%s", host, port.Port())
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err, "connect nats")
	t.Cleanup(func() { nc.Close() })

	js, err := jetstream.New(nc)
	require.NoError(t, err, "init jetstream")

	return js, nc
}

// loadTestEvents reads MessageEvent fixtures from testdata/events.json.
func loadTestEvents(t *testing.T) []model.MessageEvent {
	t.Helper()
	data, err := os.ReadFile("testdata/events.json")
	require.NoError(t, err, "read testdata/events.json")

	var events []model.MessageEvent
	require.NoError(t, json.Unmarshal(data, &events), "unmarshal events")
	return events
}

// refreshIndex forces ES to make all indexed docs searchable.
func refreshIndex(t *testing.T, esURL, pattern string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s/_refresh", esURL, pattern), nil)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "refresh index %s: %s", pattern, body)
}

// countDocs returns the number of documents matching the index pattern.
func countDocs(t *testing.T, esURL, pattern string) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/%s/_count", esURL, pattern))
	require.NoError(t, err)
	defer resp.Body.Close()

	// 404 means no indices exist yet
	if resp.StatusCode == http.StatusNotFound {
		return 0
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result struct {
		Count int `json:"count"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	return result.Count
}

// waitForClusterGreen polls ES cluster health until status is green or timeout.
func waitForClusterGreen(t *testing.T, esURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("%s/_cluster/health?wait_for_status=green&timeout=5s", esURL))
		if err == nil {
			var health struct {
				Status string `json:"status"`
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if json.Unmarshal(body, &health) == nil && health.Status == "green" {
				t.Logf("ES cluster health: %s", health.Status)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("ES cluster did not reach green status within timeout")
}

// preCreateIndex creates an ES index so shard allocation completes early.
func preCreateIndex(t *testing.T, esURL, index string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/%s", esURL, index), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "pre-create index %s: %s", index, body)
}

// testTemplateBody returns an index template with single-node-friendly settings
// (1 shard, 0 replicas) so indices can be created in the test ES container.
func testTemplateBody(prefix string) json.RawMessage {
	body := messageTemplateBody(prefix)
	var tmpl map[string]any
	_ = json.Unmarshal(body, &tmpl)

	template := tmpl["template"].(map[string]any)
	settings := template["settings"].(map[string]any)
	settings["index"] = map[string]any{
		"number_of_shards":   1,
		"number_of_replicas": 0,
		"refresh_interval":   "1s",
	}
	data, _ := json.Marshal(tmpl)
	return data
}

// getDoc retrieves a single document from ES by ID. Returns nil if not found.
func getDoc(t *testing.T, esURL, index, docID string) map[string]any {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/%s/_doc/%s", esURL, index, docID))
	require.NoError(t, err)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result struct {
		Source map[string]any `json:"_source"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	return result.Source
}

func TestSearchSyncIntegration(t *testing.T) {
	esURL := setupElasticsearch(t)
	js, _ := setupNATSJetStream(t)
	ctx := context.Background()

	// --- Setup search engine + template ---
	prefix := "msgs-inttest-v1"
	engine, err := searchengine.New(ctx, "elasticsearch", esURL)
	require.NoError(t, err, "create search engine")

	// Wait for cluster to be green before creating indices.
	waitForClusterGreen(t, esURL, 120*time.Second)

	coll := newMessageCollection(prefix)
	err = engine.UpsertTemplate(ctx, coll.TemplateName(), testTemplateBody(prefix))
	require.NoError(t, err, "upsert template")

	// Pre-create indices so shard allocation completes before bulk indexing.
	preCreateIndex(t, esURL, prefix+"-2026-01")
	preCreateIndex(t, esURL, prefix+"-2026-02")
	waitForClusterGreen(t, esURL, 120*time.Second)

	// --- Setup NATS stream + consumer ---
	siteID := "site-test"
	canonicalCfg := stream.MessagesCanonical(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonicalCfg.Name,
		Subjects: canonicalCfg.Subjects,
	})
	require.NoError(t, err, "create stream")

	// --- Load and publish test events ---
	events := loadTestEvents(t)
	for _, evt := range events {
		data, marshalErr := json.Marshal(evt)
		require.NoError(t, marshalErr)

		// Route to correct canonical subject based on event type
		var subj string
		switch evt.Event {
		case model.EventCreated:
			subj = subject.MsgCanonicalCreated(siteID)
		case model.EventUpdated:
			subj = subject.MsgCanonicalUpdated(siteID)
		case model.EventDeleted:
			subj = subject.MsgCanonicalDeleted(siteID)
		default:
			t.Fatalf("unsupported event type in fixture: %q", evt.Event)
		}
		_, pubErr := js.Publish(ctx, subj, data)
		require.NoError(t, pubErr, "publish event %s", evt.Message.ID)
	}

	// --- Create consumer and process all messages ---
	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
		Durable:   "search-sync-worker-test",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err, "create consumer")

	handler := NewHandler(&engineAdapter{engine: engine}, coll, 100)

	// Fetch all published messages
	batch, err := cons.Fetch(len(events), jetstream.FetchMaxWait(10*time.Second))
	require.NoError(t, err, "fetch messages")

	for msg := range batch.Messages() {
		handler.Add(msg)
	}

	// Flush to ES
	handler.Flush(ctx)

	// --- Verify results in Elasticsearch ---
	refreshIndex(t, esURL, prefix+"-*")

	// Total doc count: 5 created - 1 deleted = 4
	t.Run("total doc count", func(t *testing.T) {
		total := countDocs(t, esURL, prefix+"-*")
		assert.Equal(t, 4, total, "expected 4 docs total (5 created, 1 update replacing, 1 delete)")
	})

	// January index: msg-001 (updated), msg-003 → 2 docs
	t.Run("january index count", func(t *testing.T) {
		janCount := countDocs(t, esURL, prefix+"-2026-01")
		assert.Equal(t, 2, janCount, "expected 2 docs in 2026-01 index")
	})

	// February index: msg-004, msg-005 → 2 docs
	t.Run("february index count", func(t *testing.T) {
		febCount := countDocs(t, esURL, prefix+"-2026-02")
		assert.Equal(t, 2, febCount, "expected 2 docs in 2026-02 index")
	})

	// Verify msg-001 was updated (content should be edited version)
	t.Run("msg-001 updated content", func(t *testing.T) {
		doc := getDoc(t, esURL, prefix+"-2026-01", "msg-001")
		require.NotNil(t, doc, "msg-001 should exist")
		assert.Equal(t, "hello world (edited)", doc["content"])
		assert.Equal(t, "alice", doc["userAccount"])
		assert.Equal(t, "room-1", doc["roomId"])
	})

	// Verify msg-002 was deleted
	t.Run("msg-002 deleted", func(t *testing.T) {
		doc := getDoc(t, esURL, prefix+"-2026-01", "msg-002")
		assert.Nil(t, doc, "msg-002 should be deleted")
	})

	// Verify msg-003 exists with correct content
	t.Run("msg-003 exists", func(t *testing.T) {
		doc := getDoc(t, esURL, prefix+"-2026-01", "msg-003")
		require.NotNil(t, doc, "msg-003 should exist")
		assert.Equal(t, "different room", doc["content"])
		assert.Equal(t, "room-2", doc["roomId"])
	})

	// Verify msg-004 in february index
	t.Run("msg-004 in february", func(t *testing.T) {
		doc := getDoc(t, esURL, prefix+"-2026-02", "msg-004")
		require.NotNil(t, doc, "msg-004 should exist")
		assert.Equal(t, "february message", doc["content"])
		assert.Equal(t, "charlie", doc["userAccount"])
	})
}

// searchHits queries ES for docs where the content field matches the given query string.
// Returns the number of matching hits.
func searchHits(t *testing.T, esURL, indexPattern, query string) int {
	t.Helper()
	body := fmt.Sprintf(`{"query":{"match":{"content":{"query":%q}}}}`, query)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s/_search", esURL, indexPattern), bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	require.NoError(t, json.Unmarshal(respBody, &result))
	return result.Hits.Total.Value
}

// searchHitsWildcard queries ES using a wildcard query on the content field.
// The search service uses this when the query contains underscores — it operates
// on the preserved original token so cost is bounded (one token per compound word).
func searchHitsWildcard(t *testing.T, esURL, indexPattern, pattern string) int {
	t.Helper()
	body := fmt.Sprintf(`{"query":{"wildcard":{"content":{"value":%q}}}}`, pattern)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s/_search", esURL, indexPattern), bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	require.NoError(t, json.Unmarshal(respBody, &result))
	return result.Hits.Total.Value
}

// TestCustomAnalyzer verifies the underscore-preserving analyzer with HTML stripping.
// Indexes a doc with content "<b>error_handler</b> and <i>log_parser</i>" then searches
// for various subword and underscore combinations.
func TestCustomAnalyzer(t *testing.T) {
	esURL := setupElasticsearch(t)
	ctx := context.Background()

	prefix := "analyzer-test-v1"
	engine, err := searchengine.New(ctx, "elasticsearch", esURL)
	require.NoError(t, err)

	waitForClusterGreen(t, esURL, 120*time.Second)

	coll := newMessageCollection(prefix)
	err = engine.UpsertTemplate(ctx, coll.TemplateName(), testTemplateBody(prefix))
	require.NoError(t, err, "upsert template")

	preCreateIndex(t, esURL, prefix+"-2026-03")
	waitForClusterGreen(t, esURL, 120*time.Second)

	store := &engineAdapter{engine: engine}
	handler := NewHandler(store, coll, 100)

	// Doc 1: two-part underscore compounds with HTML
	evt1 := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID: "analyzer-msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "<b>error_handler</b> and <i>log_parser</i>",
			CreatedAt: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		},
		SiteID: "site-test", Timestamp: 2000001,
	}

	// Doc 2: three-part underscore compound
	evt2 := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID: "analyzer-msg-2", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "check the user_input_validator for issues",
			CreatedAt: time.Date(2026, 3, 10, 12, 5, 0, 0, time.UTC),
		},
		SiteID: "site-test", Timestamp: 2000002,
	}

	for _, evt := range []model.MessageEvent{evt1, evt2} {
		data, marshalErr := json.Marshal(evt)
		require.NoError(t, marshalErr)
		handler.Add(&stubMsg{data: data})
	}
	handler.Flush(ctx)

	refreshIndex(t, esURL, prefix+"-*")

	indexPattern := prefix + "-*"

	// Verify both docs were indexed
	require.NotNil(t, getDoc(t, esURL, prefix+"-2026-03", "analyzer-msg-1"))
	require.NotNil(t, getDoc(t, esURL, prefix+"-2026-03", "analyzer-msg-2"))

	// --- HTML stripping ---
	t.Run("html tags are stripped", func(t *testing.T) {
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "error_handler"),
			"should find doc with HTML-wrapped content")
	})

	// --- Two-part compound: error_handler, log_parser ---

	t.Run("exact compound word (match)", func(t *testing.T) {
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "error_handler"))
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "log_parser"))
	})

	t.Run("subword matches (match)", func(t *testing.T) {
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "error"))
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "handler"))
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "log"))
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "parser"))
	})

	// --- Underscore queries use wildcard ---
	// Search service rule: if query contains "_", use wildcard (append * if needed).
	// Wildcard operates on the preserved original token — bounded cost.

	t.Run("trailing underscore prefix (wildcard)", func(t *testing.T) {
		assert.Equal(t, 1, searchHitsWildcard(t, esURL, indexPattern, "error_*"),
			"'error_*' matches error_handler")
		assert.Equal(t, 1, searchHitsWildcard(t, esURL, indexPattern, "log_*"),
			"'log_*' matches log_parser")
	})

	t.Run("leading underscore suffix (wildcard)", func(t *testing.T) {
		assert.Equal(t, 1, searchHitsWildcard(t, esURL, indexPattern, "*_handler"),
			"'*_handler' matches error_handler")
		assert.Equal(t, 1, searchHitsWildcard(t, esURL, indexPattern, "*_parser"),
			"'*_parser' matches log_parser")
	})

	// --- Three-part compound: user_input_validator ---

	t.Run("three-part exact compound (match)", func(t *testing.T) {
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "user_input_validator"))
	})

	t.Run("three-part subwords (match)", func(t *testing.T) {
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "user"))
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "input"))
		assert.Equal(t, 1, searchHits(t, esURL, indexPattern, "validator"))
	})

	t.Run("three-part partial compound (wildcard)", func(t *testing.T) {
		assert.Equal(t, 1, searchHitsWildcard(t, esURL, indexPattern, "user_input*"),
			"'user_input*' matches user_input_validator")
		assert.Equal(t, 1, searchHitsWildcard(t, esURL, indexPattern, "*input_validator"),
			"'*input_validator' matches user_input_validator")
		assert.Equal(t, 1, searchHitsWildcard(t, esURL, indexPattern, "user_inp*"),
			"'user_inp*' partial match on original token")
	})

	t.Run("no false positives", func(t *testing.T) {
		assert.Equal(t, 0, searchHits(t, esURL, indexPattern, "nonexistent_term"))
		assert.Equal(t, 0, searchHitsWildcard(t, esURL, indexPattern, "error_parser*"),
			"cross-compound should not match")
	})
}
