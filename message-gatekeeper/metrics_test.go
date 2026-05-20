package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/cachestats"
)

// TestMetricsHandler_ExposesCacheCountersAndSize verifies the
// Prometheus scrape contract that gatekeeper will publish: hits,
// misses, and size series with the cache-name labels gatekeeper
// registers (subscription, roommeta). This is a contract test
// against pkg/cachestats + promhttp, not against main.go's wiring
// directly — main.go's behavior is integration-only.
func TestMetricsHandler_ExposesCacheCountersAndSize(t *testing.T) {
	reg := prometheus.NewRegistry()
	stats := cachestats.New(reg)

	subSize := 0
	subRec := stats.Register("subscription", func() int { return subSize })
	metaRec := stats.Register("roommeta", func() int { return 7 })

	subRec.Hit()
	subRec.Hit()
	subRec.Miss()
	subSize = 4096
	metaRec.Hit()

	server := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer server.Close()

	resp, err := http.Get(server.URL) //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := string(body)

	assert.Contains(t, out, `chat_cache_hits_total{cache="subscription"} 2`)
	assert.Contains(t, out, `chat_cache_misses_total{cache="subscription"} 1`)
	assert.Contains(t, out, `chat_cache_hits_total{cache="roommeta"} 1`)
	assert.Contains(t, out, `chat_cache_size{cache="subscription"} 4096`)
	assert.Contains(t, out, `chat_cache_size{cache="roommeta"} 7`)
	// Sanity: only the names we registered appear.
	assert.NotContains(t, strings.ToLower(out), `cache="user"`)
}
