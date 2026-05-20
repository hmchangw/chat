//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/restyutil"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

const testUserRoomIndex = "user-room"

// --- Shared HTTP helpers -----------------------------------------------------

// testHTTPClient is a bounded HTTP client for ES control-plane calls —
// stalled containers shouldn't be able to hang the integration job past
// the per-call deadline. Kept small on purpose: these calls hit localhost
// (docker-mapped port) and are cheap when they succeed.
//
// Used by seedDoc (below), by the index-cleanup path in uniqueESIndex
// (setup_shared_test.go), by putTestSpotlightIndex, and by the CCS-only
// helpers in integration_ccs_test.go.
var testHTTPClient = &http.Client{Timeout: 10 * time.Second}

// seedDoc PUTs a JSON document into ES, synchronously refreshing the index
// so the next search sees it.
func seedDoc(t *testing.T, esURL, index, id string, doc any) {
	t.Helper()
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	url := fmt.Sprintf("%s/%s/_doc/%s?refresh=true", esURL, index, id)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := testHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Truef(t, resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK,
		"seedDoc %s/%s: status=%d body=%s", index, id, resp.StatusCode, body)
}

// --- search.apps integration ------------------------------------------------

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

// --- search.users integration ------------------------------------------------

// usersFixture is a minimal fixture for the search.users path: NATS for the
// request/reply layer, and an httptest.Server standing in for the third-party
// HR endpoint. No Mongo or ES containers are needed.
type usersFixture struct {
	clientNATS *nats.Conn
	thirdParty *httptest.Server // controls the stub response
}

func setupUsersFixture(t *testing.T, thirdPartyHandler http.Handler) *usersFixture {
	t.Helper()

	// Start the stub third-party server.
	stub := httptest.NewServer(thirdPartyHandler)
	t.Cleanup(stub.Close)

	natsURL := sharedNATS(t)
	serverNC, err := natsutil.Connect(natsURL, "")
	require.NoError(t, err, "connect nats (server side)")
	t.Cleanup(func() { _ = serverNC.Drain() })

	clientNC, err := nats.Connect(natsURL)
	require.NoError(t, err, "connect nats (client side)")
	t.Cleanup(func() { clientNC.Close() })

	// Wire the handler with a real httpUsersClient pointing at the stub.
	usersRC := restyutil.New(stub.URL, restyutil.WithTimeout(5*time.Second))
	usersClient := newHTTPUsersClient(usersRC, "")

	h := newHandler(nil, nil, usersClient, newFakeCache(), handlerConfig{
		DocCounts:      25,
		MaxDocCounts:   100,
		RequestTimeout: 5 * time.Second,
	})

	router := natsrouter.New(serverNC, "search-service-test")
	router.Use(natsrouter.RequestID())
	h.Register(router)
	// Flush — see setupAppsFixture for the rationale.
	require.NoError(t, serverNC.NatsConn().Flush())
	t.Cleanup(func() { _ = router.Shutdown(context.Background()) })

	return &usersFixture{clientNATS: clientNC, thirdParty: stub}
}

func TestIntegration_SearchUsers_Happy(t *testing.T) {
	// Stub returns two users matching the query.
	stubResp := `[{"account":"alice","engName":"Alice Wang"},{"account":"alice2","engName":"Alice Chen"}]`

	f := setupUsersFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(stubResp))
	}))

	reqBytes, err := json.Marshal(model.SearchUsersRequest{Query: "alice"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchUsers("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var users []model.SearchUser
	require.NoError(t, json.Unmarshal(msg.Data, &users))

	require.Len(t, users, 2)
	assert.Equal(t, "alice", users[0].Account)
	assert.Equal(t, "Alice Wang", users[0].EngName)
}

func TestIntegration_SearchUsers_EmptyQueryReturnsBadRequest(t *testing.T) {
	// Stub should never be called for a bad-request scenario.
	f := setupUsersFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("third-party stub should not be called for empty query")
		w.WriteHeader(http.StatusInternalServerError)
	}))

	reqBytes, err := json.Marshal(model.SearchUsersRequest{Query: ""})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchUsers("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var envelope model.ErrorResponse
	require.NoError(t, json.Unmarshal(msg.Data, &envelope))
	require.NotEmpty(t, envelope.Error)
	assert.Equal(t, natsrouter.CodeBadRequest, envelope.Code)
}

func TestIntegration_SearchUsers_ThirdPartyErrorReturnsInternal(t *testing.T) {
	// Stub returns a 503 to simulate a backend outage.
	f := setupUsersFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	reqBytes, err := json.Marshal(model.SearchUsersRequest{Query: "alice"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchUsers("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var envelope model.ErrorResponse
	require.NoError(t, json.Unmarshal(msg.Data, &envelope))
	require.NotEmpty(t, envelope.Error)
	assert.Equal(t, natsrouter.CodeInternal, envelope.Code,
		"non-2xx from third-party must surface as internal error, not raw status")
	// Raw third-party details must not leak to the caller.
	assert.NotContains(t, envelope.Error, "503", "status code from third-party must not leak")
}

// --- search.rooms integration ----------------------------------------

// roomsFixture wires a real ES container (for the spotlight index) and
// NATS. search.rooms is served directly from the spotlight index, so no
// Mongo is involved. The ES container is process-shared; per-test
// isolation comes from a unique spotlight index name (deleted on
// cleanup) plus a Valkey FLUSHDB on cleanup.
type roomsFixture struct {
	clientNATS     *nats.Conn
	esURL          string
	spotlightIndex string
}

// setupRoomsFixture wires the search-service router against the
// process-shared ES, Valkey and NATS containers. The spotlight index
// name is unique per test so leftovers from a sibling test can't leak
// into this one's hit set.
func setupRoomsFixture(t *testing.T) *roomsFixture {
	t.Helper()
	ctx := context.Background()

	esURL := sharedSingleNodeES(t)
	spotlightIndex := uniqueESIndex(t, "spotlight")
	putTestSpotlightIndex(t, esURL, spotlightIndex)

	natsURL := sharedNATS(t)
	serverNC, err := natsutil.Connect(natsURL, "")
	require.NoError(t, err, "connect nats (server side)")
	t.Cleanup(func() { _ = serverNC.Drain() })

	clientNC, err := nats.Connect(natsURL)
	require.NoError(t, err, "connect nats (client side)")
	t.Cleanup(func() { clientNC.Close() })

	engine, err := searchengine.New(ctx, searchengine.Config{Backend: "elasticsearch", URL: esURL})
	require.NoError(t, err, "build searchengine for subs fixture")

	esStore := newESStore(engine, testUserRoomIndex)
	cache := newValkeyCache(freshValkeyClient(t))
	h := newHandler(esStore, nil, nil, cache, handlerConfig{
		DocCounts:               25,
		MaxDocCounts:            100,
		RestrictedRoomsCacheTTL: 5 * time.Minute,
		RecentWindow:            365 * 24 * time.Hour,
		RequestTimeout:          5 * time.Second,
		SpotlightReadPattern:    spotlightIndex,
	})

	router := natsrouter.New(serverNC, "search-service-test-subs")
	router.Use(natsrouter.RequestID())
	h.Register(router)
	// Flush — see setupAppsFixture for the rationale.
	require.NoError(t, serverNC.NatsConn().Flush())
	t.Cleanup(func() { _ = router.Shutdown(context.Background()) })

	return &roomsFixture{clientNATS: clientNC, esURL: esURL, spotlightIndex: spotlightIndex}
}

// putTestSpotlightIndex creates a minimal spotlight index in ES with the
// fields needed by the subscription search query.
func putTestSpotlightIndex(t *testing.T, esURL, index string) {
	t.Helper()
	body := map[string]any{
		"settings": map[string]any{
			"number_of_shards":   1,
			"number_of_replicas": 0,
			"refresh_interval":   "1s",
		},
		"mappings": map[string]any{
			"dynamic": false,
			"properties": map[string]any{
				"roomId": map[string]any{"type": "keyword"},
				"roomName": map[string]any{
					"type": "search_as_you_type",
				},
				"roomType":    map[string]any{"type": "keyword"},
				"userAccount": map[string]any{"type": "keyword"},
				"siteId":      map[string]any{"type": "keyword"},
				"joinedAt":    map[string]any{"type": "date"},
			},
		},
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPut, esURL+"/"+index, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := testHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated,
		"create spotlight index: status=%d body=%s", resp.StatusCode, b)
}

func TestIntegration_SearchRooms_HappyPath(t *testing.T) {
	f := setupRoomsFixture(t)

	const account = "alice"
	now := time.Now().UTC()

	// Seed spotlight docs for two rooms alice is in.
	seedDoc(t, f.esURL, f.spotlightIndex, "spot-r1", map[string]any{
		"roomId":      "r1",
		"roomName":    "engineering-announcements",
		"roomType":    "channel",
		"userAccount": account,
		"siteId":      "site-local",
		"joinedAt":    now.Add(-48 * time.Hour).Format(time.RFC3339),
	})
	seedDoc(t, f.esURL, f.spotlightIndex, "spot-r2", map[string]any{
		"roomId":      "r2",
		"roomName":    "engineering-random",
		"roomType":    "channel",
		"userAccount": account,
		"siteId":      "site-local",
		"joinedAt":    now.Add(-24 * time.Hour).Format(time.RFC3339),
	})
	// A matching room owned by a different account. With the Mongo
	// hydration removed, the spotlight userAccount term filter is the
	// sole access boundary — this must not leak into alice's results.
	seedDoc(t, f.esURL, f.spotlightIndex, "spot-r3", map[string]any{
		"roomId":      "r3",
		"roomName":    "engineering-secret",
		"roomType":    "channel",
		"userAccount": "mallory",
		"siteId":      "site-local",
		"joinedAt":    now.Add(-12 * time.Hour).Format(time.RFC3339),
	})

	reqBytes, err := json.Marshal(model.SearchRoomsRequest{Query: "engineering"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchRooms(account), reqBytes, 10*time.Second)
	require.NoError(t, err)

	var resp model.SearchRoomsResponse
	require.NoError(t, json.Unmarshal(msg.Data, &resp))

	require.Len(t, resp.Rooms, 2, "both rooms matching 'engineering' must be returned")
	byID := map[string]model.SearchRoom{}
	for _, r := range resp.Rooms {
		byID[r.RoomID] = r
	}
	assert.Equal(t, model.SearchRoom{RoomID: "r1", Name: "engineering-announcements", RoomType: "channel", SiteID: "site-local"}, byID["r1"])
	assert.Equal(t, model.SearchRoom{RoomID: "r2", Name: "engineering-random", RoomType: "channel", SiteID: "site-local"}, byID["r2"])
	_, leaked := byID["r3"]
	assert.False(t, leaked, "rooms owned by another account must not leak")
}

func TestIntegration_SearchRooms_RoomTypeChannelFilter(t *testing.T) {
	f := setupRoomsFixture(t)

	const account = "bob"
	now := time.Now().UTC()

	seedDoc(t, f.esURL, f.spotlightIndex, "spot-b-r1", map[string]any{
		"roomId":      "b-r1",
		"roomName":    "bob-alice",
		"roomType":    "dm",
		"userAccount": account,
		"siteId":      "site-local",
		"joinedAt":    now.Add(-1 * time.Hour).Format(time.RFC3339),
	})
	seedDoc(t, f.esURL, f.spotlightIndex, "spot-b-r2", map[string]any{
		"roomId":      "b-r2",
		"roomName":    "bob-channel",
		"roomType":    "channel",
		"userAccount": account,
		"siteId":      "site-local",
		"joinedAt":    now.Add(-2 * time.Hour).Format(time.RFC3339),
	})

	reqBytes, err := json.Marshal(model.SearchRoomsRequest{Query: "bob", RoomType: "channel"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchRooms(account), reqBytes, 10*time.Second)
	require.NoError(t, err)

	var resp model.SearchRoomsResponse
	require.NoError(t, json.Unmarshal(msg.Data, &resp))

	require.Len(t, resp.Rooms, 1)
	assert.Equal(t, model.SearchRoom{RoomID: "b-r2", Name: "bob-channel", RoomType: "channel", SiteID: "site-local"}, resp.Rooms[0],
		"only the channel room must match roomType=channel filter")
}

func TestIntegration_SearchRooms_EmptyQueryReturnsBadRequest(t *testing.T) {
	f := setupRoomsFixture(t)

	reqBytes, err := json.Marshal(model.SearchRoomsRequest{Query: ""})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchRooms("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var envelope model.ErrorResponse
	require.NoError(t, json.Unmarshal(msg.Data, &envelope))
	require.NotEmpty(t, envelope.Error)
	assert.Equal(t, natsrouter.CodeBadRequest, envelope.Code)
}

func TestIntegration_SearchRooms_RoomTypeAppReturnsBadRequest(t *testing.T) {
	f := setupRoomsFixture(t)

	reqBytes, err := json.Marshal(model.SearchRoomsRequest{Query: "x", RoomType: "app"})
	require.NoError(t, err)

	msg, err := f.clientNATS.Request(subject.SearchRooms("alice"), reqBytes, 5*time.Second)
	require.NoError(t, err)

	var envelope model.ErrorResponse
	require.NoError(t, json.Unmarshal(msg.Data, &envelope))
	require.NotEmpty(t, envelope.Error)
	assert.Equal(t, natsrouter.CodeBadRequest, envelope.Code)
	assert.Contains(t, envelope.Error, "invalid roomType")
}

// --- search.messages v2 integration -----------------------------------------

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
