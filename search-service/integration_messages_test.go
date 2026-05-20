//go:build integration

package main

// search.messages v2 integration tests. Stubs ES with an httptest
// server because the messages path is pure ES — no Mongo round-trip —
// and uses the process-shared NATS from setup_shared_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/subject"
)

// messagesV2Fixture stubs ES with a fake HTTP server (httptest). The
// messages path is pure ES — no Mongo round-trip — so no Mongo fixture
// is wired.
type messagesV2Fixture struct {
	clientNATS *nats.Conn
}

func setupMessagesV2Fixture(t *testing.T) *messagesV2Fixture {
	t.Helper()
	ctx := context.Background()

	// Stub ES: always return a canned response containing one hit.
	esStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the HTTP/1.1 connection stays open.
		_, _ = io.Copy(io.Discard, r.Body)
		// The Elastic Go client performs a "product check" handshake on
		// connect and rejects any server that doesn't advertise itself
		// as Elasticsearch via this header. Set it on every response so
		// the stub passes the check regardless of which endpoint is hit.
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":1},"hits":[{"_source":{` +
			`"messageId":"m1","roomId":"r1","siteId":"site-a","userId":"u1",` +
			`"userAccount":"alice","content":"hello","createdAt":"2026-04-01T12:00:00Z"}}]}}`))
	}))
	t.Cleanup(esStub.Close)

	// Valkey stub — use the fakeCache wired in-process via handler injection.
	fakeValkey := newFakeCache()
	fakeValkey.store["alice"] = map[string]int64{} // empty restricted map, cache hit

	natsURL := sharedNATS(t)

	serverNATS, err := natsutil.Connect(natsURL, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverNATS.Drain() })

	clientNATS, err := nats.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(func() { clientNATS.Close() })

	// Wire search-service with the stub ES engine. No Mongo store needed
	// for the messages path.
	engine, err := searchengine.New(ctx, searchengine.Config{Backend: "elasticsearch", URL: esStub.URL})
	require.NoError(t, err)
	esStore := newESStore(engine, testUserRoomIndex)

	h := newHandler(esStore, nil, nil, fakeValkey, handlerConfig{
		DocCounts:               25,
		MaxDocCounts:            100,
		RestrictedRoomsCacheTTL: 5 * time.Minute,
		RecentWindow:            365 * 24 * time.Hour,
		RequestTimeout:          5 * time.Second,
		UserRoomIndex:           testUserRoomIndex,
		SpotlightReadPattern:    "spotlight-*",
	})

	router := natsrouter.New(serverNATS, "search-service-test-v2")
	router.Use(natsrouter.RequestID())
	h.Register(router)
	// Flush — see setupAppsFixture for the rationale.
	require.NoError(t, serverNATS.NatsConn().Flush())
	t.Cleanup(func() { _ = router.Shutdown(context.Background()) })

	return &messagesV2Fixture{clientNATS: clientNATS}
}

func TestIntegration_SearchMessages_V2_HitProjection(t *testing.T) {
	f := setupMessagesV2Fixture(t)

	reqBytes, err := json.Marshal(model.SearchMessagesRequest{Query: "hello"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchMessages("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var resp model.SearchMessagesResponse
	require.NoError(t, json.Unmarshal(msg.Data, &resp))

	require.Len(t, resp.Messages, 1)
	assert.EqualValues(t, 1, resp.Total)

	got := resp.Messages[0]
	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "r1", got.RoomID)
	assert.Equal(t, "site-a", got.SiteID)
	assert.Equal(t, "alice", got.UserAccount)
	assert.Equal(t, "hello", got.Content)
}

func TestIntegration_SearchMessages_V2_EmptyQueryReturnsBadRequest(t *testing.T) {
	f := setupMessagesV2Fixture(t)

	reqBytes, err := json.Marshal(model.SearchMessagesRequest{Query: ""})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchMessages("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var envelope model.ErrorResponse
	require.NoError(t, json.Unmarshal(msg.Data, &envelope))
	assert.Equal(t, natsrouter.CodeBadRequest, envelope.Code)
}
