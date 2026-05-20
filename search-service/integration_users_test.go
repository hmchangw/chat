//go:build integration

package main

// Integration tests for search.users (NATS + httptest stub for HR endpoint).

import (
	"context"
	"encoding/json"
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
	"github.com/hmchangw/chat/pkg/restyutil"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

type usersFixture struct {
	clientNATS *nats.Conn
	thirdParty *httptest.Server // controls the stub response
}

func setupUsersFixture(t *testing.T, thirdPartyHandler http.Handler) *usersFixture {
	t.Helper()

	stub := httptest.NewServer(thirdPartyHandler)
	t.Cleanup(stub.Close)

	natsURL := testutil.NATS(t)
	serverNC, err := natsutil.Connect(natsURL, "")
	require.NoError(t, err, "connect nats (server side)")
	t.Cleanup(func() { _ = serverNC.Drain() })

	clientNC, err := nats.Connect(natsURL)
	require.NoError(t, err, "connect nats (client side)")
	t.Cleanup(func() { clientNC.Close() })

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
