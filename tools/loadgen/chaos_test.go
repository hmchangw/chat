package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChaosAddLatencyToxic_PostsCorrectShape(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	err := chaosAddToxic(context.Background(), server.URL, "nats_inbound", "latency-fault", "latency", map[string]int{
		"latency": 200,
		"jitter":  50,
	})
	require.NoError(t, err)
	assert.Equal(t, "/proxies/nats_inbound/toxics", receivedPath)
	assert.Equal(t, "latency-fault", receivedBody["name"])
	assert.Equal(t, "latency", receivedBody["type"])
	attrs, ok := receivedBody["attributes"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(200), attrs["latency"])
	assert.Equal(t, float64(50), attrs["jitter"])
}

func TestChaosRemoveToxic_DeletesByName(t *testing.T) {
	var receivedPath string
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := chaosRemoveToxic(context.Background(), server.URL, "nats_inbound", "latency-fault")
	require.NoError(t, err)
	assert.Equal(t, "/proxies/nats_inbound/toxics/latency-fault", receivedPath)
	assert.Equal(t, http.MethodDelete, receivedMethod)
}

func TestChaosListToxics_ReturnsArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
            {"name":"latency-fault","type":"latency","attributes":{"latency":200,"jitter":50}}
        ]`))
	}))
	defer server.Close()

	toxics, err := chaosListToxics(context.Background(), server.URL, "nats_inbound")
	require.NoError(t, err)
	require.Len(t, toxics, 1)
	assert.Equal(t, "latency-fault", toxics[0].Name)
	assert.Equal(t, "latency", toxics[0].Type)
}

func TestChaosSubcommand_DispatchesAddRemoveList(t *testing.T) {
	// Verify the runChaos dispatcher correctly parses sub-actions.
	for _, action := range []string{"add", "remove", "list"} {
		t.Run(action, func(t *testing.T) {
			assert.True(t, isValidChaosAction(action), "%s should be a valid action", action)
		})
	}
	assert.False(t, isValidChaosAction("destroy"), "destroy should NOT be a valid action")
}
