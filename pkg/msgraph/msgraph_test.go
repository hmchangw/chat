package msgraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient wires a graphClient at the given token + graph servers.
func newTestClient(tokenURL, baseURL string) Client {
	return New(
		Config{TenantID: "t", ClientID: "c", ClientSecret: "s"},
		WithTokenURL(tokenURL),
		WithBaseURL(baseURL),
	)
}

func TestCreateOnlineMeeting_Success(t *testing.T) {
	var tokenCalls, meetingCalls int

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		assert.Equal(t, graphScope, r.Form.Get("scope"))
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok-123", ExpiresIn: 3600})
	}))
	defer tokenSrv.Close()

	graphSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meetingCalls++
		assert.Equal(t, "Bearer tok-123", r.Header.Get("Authorization"))
		assert.True(t, strings.Contains(r.URL.Path, "/users/alice%40corp.com/onlineMeetings") ||
			strings.Contains(r.URL.Path, "/users/alice@corp.com/onlineMeetings"),
			"organizer-scoped path expected, got %s", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(OnlineMeeting{ID: "m1", JoinURL: "https://join/1"})
	}))
	defer graphSrv.Close()

	c := newTestClient(tokenSrv.URL, graphSrv.URL)
	m, err := c.CreateOnlineMeeting(context.Background(), CreateOnlineMeetingRequest{
		Subject: "Standup", OrganizerEmail: "alice@corp.com", AttendeeEmails: []string{"bob@corp.com"},
	})
	require.NoError(t, err)
	assert.Equal(t, "m1", m.ID)
	assert.Equal(t, "https://join/1", m.JoinURL)

	// Second call reuses the cached token (no second token fetch).
	_, err = c.CreateOnlineMeeting(context.Background(), CreateOnlineMeetingRequest{OrganizerEmail: "alice@corp.com"})
	require.NoError(t, err)
	assert.Equal(t, 1, tokenCalls, "token should be cached across calls")
	assert.Equal(t, 2, meetingCalls)
}

func TestCreateOnlineMeeting_TokenError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(tokenResponse{Error: "invalid_client", ErrorDesc: "bad secret"})
	}))
	defer tokenSrv.Close()

	c := New(
		Config{TenantID: "t", ClientID: "c", ClientSecret: "super-secret-value"},
		WithTokenURL(tokenSrv.URL), WithBaseURL("http://unused"),
	)
	_, err := c.CreateOnlineMeeting(context.Background(), CreateOnlineMeetingRequest{OrganizerEmail: "a@b.com"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_client")
	// Never leak the secret in the error.
	assert.NotContains(t, err.Error(), "super-secret-value")
}

func TestCreateOnlineMeeting_GraphError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 3600})
	}))
	defer tokenSrv.Close()
	graphSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"Forbidden"}}`))
	}))
	defer graphSrv.Close()

	c := newTestClient(tokenSrv.URL, graphSrv.URL)
	_, err := c.CreateOnlineMeeting(context.Background(), CreateOnlineMeetingRequest{OrganizerEmail: "a@b.com"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestCreateOnlineMeeting_MissingJoinURL(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 3600})
	}))
	defer tokenSrv.Close()
	graphSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(OnlineMeeting{ID: "m1"}) // no joinWebUrl
	}))
	defer graphSrv.Close()

	c := newTestClient(tokenSrv.URL, graphSrv.URL)
	_, err := c.CreateOnlineMeeting(context.Background(), CreateOnlineMeetingRequest{OrganizerEmail: "a@b.com"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "joinWebUrl")
}
