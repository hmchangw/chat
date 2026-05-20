//go:build integration

package main

// search.apps integration tests. Uses the process-shared Mongo
// (testutil.MongoDB) and NATS (sharedNATS in setup_shared_test.go); ES
// and Valkey are stubbed because the apps path doesn't touch them.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

// setupAppsFixture starts an isolated Mongo container (via pkg/testutil) and
// a single search-service router bound to that DB. ES/Valkey are not used by
// search.apps, so we wire fakes (the existing `fakeStore` / `fakeCache`
// satisfy the interfaces but never get called on the apps path).
type appsFixture struct {
	clientNATS *nats.Conn
	mongoDB    *mongo.Database
}

func setupAppsFixture(t *testing.T) *appsFixture {
	t.Helper()

	mongoDB := testutil.MongoDB(t, "search_service_test")

	natsURL := sharedNATS(t)

	serverNATS, err := natsutil.Connect(natsURL, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverNATS.Drain() })

	clientNATS, err := nats.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(func() { clientNATS.Close() })

	// Wire the handler with a real mongoStore and stub ES/cache.
	mongoStore := newMongoStore(mongoDB)
	store := &fakeStore{}
	cache := newFakeCache()
	h := newHandler(store, mongoStore, nil, cache, handlerConfig{
		DocCounts:               25,
		MaxDocCounts:            100,
		RestrictedRoomsCacheTTL: 5 * time.Minute,
		RecentWindow:            365 * 24 * time.Hour,
		RequestTimeout:          5 * time.Second,
		SpotlightReadPattern:    "spotlight-*",
	})

	router := natsrouter.New(serverNATS, "search-service-test")
	router.Use(natsrouter.RequestID())
	h.Register(router)
	// Flush ensures subscriptions are registered on the server before the
	// fixture returns. Without this, fast tests that fire a request
	// immediately can hit "no responders available" while subscriptions
	// are still propagating. natsutil.Connect returns an otelnats.Conn
	// wrapper that doesn't expose Flush; reach through to the underlying
	// *nats.Conn.
	require.NoError(t, serverNATS.NatsConn().Flush())
	t.Cleanup(func() {
		_ = router.Shutdown(context.Background())
	})

	return &appsFixture{clientNATS: clientNATS, mongoDB: mongoDB}
}

func TestIntegration_SearchApps_PrototypePipeline(t *testing.T) {
	f := setupAppsFixture(t)
	ctx := context.Background()

	// Seed 3 apps in Mongo. The prototype pipeline matches by `name` regex
	// (case-insensitive) and applies $limit; the full $lookup access-guard
	// pipeline is implemented in a follow-up.
	_, err := f.mongoDB.Collection("apps").InsertMany(ctx, []any{
		map[string]any{"_id": "a1", "name": "Weather Alpha", "assistant": map[string]any{"enabled": true, "name": "weather.bot"}},
		map[string]any{"_id": "a2", "name": "Weatherly", "assistant": map[string]any{"enabled": false, "name": "weatherly.bot"}},
		map[string]any{"_id": "a3", "name": "Calendar"},
	})
	require.NoError(t, err)

	reqBytes, err := json.Marshal(model.SearchAppsRequest{Query: "weather"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchApps("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var resp model.SearchAppsResponse
	require.NoError(t, json.Unmarshal(msg.Data, &resp))

	require.Len(t, resp.Apps, 2, "two apps match the 'weather' regex")
	names := []string{resp.Apps[0].Name, resp.Apps[1].Name}
	assert.Contains(t, names, "Weather Alpha")
	assert.Contains(t, names, "Weatherly")
}

func TestIntegration_SearchApps_AssistantEnabledFilter(t *testing.T) {
	f := setupAppsFixture(t)
	ctx := context.Background()

	_, err := f.mongoDB.Collection("apps").InsertMany(ctx, []any{
		map[string]any{"_id": "a1", "name": "Weather Alpha", "assistant": map[string]any{"enabled": true, "name": "weather.bot"}},
		map[string]any{"_id": "a2", "name": "Weatherly", "assistant": map[string]any{"enabled": false, "name": "weatherly.bot"}},
	})
	require.NoError(t, err)

	enabled := true
	reqBytes, err := json.Marshal(model.SearchAppsRequest{
		Query:            "weather",
		AssistantEnabled: &enabled,
	})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchApps("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var resp model.SearchAppsResponse
	require.NoError(t, json.Unmarshal(msg.Data, &resp))

	require.Len(t, resp.Apps, 1)
	assert.Equal(t, "Weather Alpha", resp.Apps[0].Name)
}

func TestIntegration_SearchApps_EmptyQueryReturnsBadRequest(t *testing.T) {
	f := setupAppsFixture(t)

	reqBytes, err := json.Marshal(model.SearchAppsRequest{Query: ""})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchApps("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var envelope model.ErrorResponse
	require.NoError(t, json.Unmarshal(msg.Data, &envelope))
	require.NotEmpty(t, envelope.Error)
	assert.Equal(t, natsrouter.CodeBadRequest, envelope.Code)
}
