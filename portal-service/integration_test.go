//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/ginutil"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTests(m) }

func TestMongoDirectoryStore_LoadAll(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	ctx := context.Background()

	_, err := db.Collection("directory").InsertMany(ctx, []any{
		bson.D{{Key: "_id", Value: "alice"}, {Key: "employeeId", Value: "E001"}, {Key: "siteId", Value: "site-a"}},
		bson.D{{Key: "_id", Value: "bob"}, {Key: "employeeId", Value: "E002"}, {Key: "siteId", Value: ""}},
	})
	require.NoError(t, err)

	store := newMongoDirectoryStore(db, "directory")
	recs, err := store.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, recs, 2)

	byAccount := map[string]directoryRecord{}
	for _, r := range recs {
		byAccount[r.Account] = r
	}
	assert.Equal(t, directoryRecord{Account: "alice", EmployeeID: "E001", SiteID: "site-a"}, byAccount["alice"])
	assert.Equal(t, "", byAccount["bob"].SiteID) // not-ready rows load too; the gate decides
}

func TestMongoDirectoryStore_LoadAll_EmptyCollection(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	store := newMongoDirectoryStore(db, "directory")
	recs, err := store.LoadAll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, recs)
}

func TestPortal_EndToEnd_IssueAndReload(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	ctx := context.Background()

	_, err := db.Collection("directory").InsertOne(ctx,
		bson.D{{Key: "_id", Value: "alice"}, {Key: "employeeId", Value: "E001"}, {Key: "siteId", Value: "site-a"}})
	require.NoError(t, err)

	var upstreamRequestID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequestID = r.Header.Get(natsutil.RequestIDHeader)
		assert.Equal(t, "/auth", r.URL.Path)
		var req struct {
			Account       string `json:"account"`
			NATSPublicKey string `json:"natsPublicKey"`
		}
		// No require in the server goroutine — fail via HTTP so the client side reports it.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"natsJwt":"jwt-e2e","user":{"account":"` + req.Account + `"}}`))
	}))
	t.Cleanup(upstream.Close)

	store := newMongoDirectoryStore(db, "directory")
	dir := newDirMap()
	_, err = dir.Reload(ctx, store)
	require.NoError(t, err)

	h, err := NewPortalHandler(&PortalHandlerParams{
		Store:            store,
		Dir:              dir,
		Forwarder:        newRestyForwarder(5*time.Second, false),
		SiteAuthURLs:     map[string]string{"site-a": upstream.URL},
		SiteNATSURLs:     map[string]string{"site-a": "wss://nats-a:9222"},
		SiteFrontendURLs: map[string]string{"site-a": "https://chat-a.example.com"},
		AdminToken:       "it-admin-token",
		DevMode:          true, // account-shape body; no Keycloak in integration env
	})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ginutil.RequestID())
	registerRoutes(r, h)

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/session/nats-jwt", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	userPub := mustUserNKey(t)

	// 1) alice is ready → credentials relayed, natsUrl resolved, request id propagated.
	w := post(`{"account":"alice","natsPublicKey":"` + userPub + `"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"natsJwt":"jwt-e2e"`)
	assert.Contains(t, w.Body.String(), `"natsUrl":"wss://nats-a:9222"`)
	assert.True(t, idgen.IsValidUUID(upstreamRequestID), "X-Request-ID must reach auth-service")

	// 2) bob is unknown until the cron syncs + reloads.
	w = post(`{"account":"bob","natsPublicKey":"` + userPub + `"}`)
	require.Equal(t, http.StatusForbidden, w.Code)

	_, err = db.Collection("directory").InsertOne(ctx,
		bson.D{{Key: "_id", Value: "bob"}, {Key: "employeeId", Value: "E002"}, {Key: "siteId", Value: "site-a"}})
	require.NoError(t, err)

	reloadReq := httptest.NewRequest(http.MethodPost, "/admin/cache/reload", bytes.NewReader(nil))
	reloadReq.Header.Set("Authorization", "Bearer it-admin-token")
	wr := httptest.NewRecorder()
	r.ServeHTTP(wr, reloadReq)
	require.Equal(t, http.StatusOK, wr.Code, wr.Body.String())
	assert.Contains(t, wr.Body.String(), `"records":2`, "reload must re-read alice and bob from Mongo")

	w = post(`{"account":"bob","natsPublicKey":"` + userPub + `"}`)
	assert.Equal(t, http.StatusOK, w.Code, "bob must resolve after reload")
	assert.Contains(t, w.Body.String(), `"natsJwt":"jwt-e2e"`)
}
