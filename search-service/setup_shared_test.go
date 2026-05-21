//go:build integration

package main

// ES / NATS / Valkey / Mongo come from pkg/testutil. CCS tests bring
// their own ES pair (integration_ccs_test.go).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil"
)

const testUserRoomIndex = "user-room"

// NATS queue groups. Each search-service router gets its own so a slow
// drain after one test can't deliver to a sibling test's handler.
const (
	testQueueGroup     = "search-service-test"      // apps, users, CCS
	testQueueGroupSubs = "search-service-test-subs" // rooms
	testQueueGroupV2   = "search-service-test-v2"   // messages v2
)

// Bounded HTTP client for ES control-plane calls.
var testHTTPClient = &http.Client{Timeout: 10 * time.Second}

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

// TestMain pre-warms shared containers in parallel; fails fast on error.
func TestMain(m *testing.M) {
	var wg sync.WaitGroup
	prewarms := []func() error{
		testutil.EnsureElasticsearch,
		testutil.EnsureNATS,
		testutil.EnsureValkey,
		testutil.EnsureMongo,
	}
	errCh := make(chan error, len(prewarms))
	for _, fn := range prewarms {
		wg.Add(1)
		go func(f func() error) {
			defer wg.Done()
			if err := f(); err != nil {
				errCh <- err
			}
		}(fn)
	}
	wg.Wait()
	close(errCh)
	if err, ok := <-errCh; ok {
		fmt.Fprintf(os.Stderr, "prewarm shared containers: %v\n", err)
		testutil.TerminateAll()
		os.Exit(1)
	}
	testutil.RunTests(m)
}

// uniqueESIndex returns a per-test ES index name (fnv hash keeps it short
// and ES-safe across subtest slashes) and registers DELETE on cleanup.
func uniqueESIndex(t *testing.T, prefix string) string {
	t.Helper()
	esURL := testutil.Elasticsearch(t)
	h := fnv.New64a()
	_, _ = h.Write([]byte(t.Name()))
	name := fmt.Sprintf("%s-%x", prefix, h.Sum64())
	t.Cleanup(func() {
		req, err := http.NewRequest(http.MethodDelete, esURL+"/"+name, nil)
		if err != nil {
			t.Logf("delete index %s: build request: %v", name, err)
			return
		}
		resp, err := testHTTPClient.Do(req)
		if err != nil {
			t.Logf("delete index %s: %v", name, err)
			return
		}
		_ = resp.Body.Close()
	})
	return name
}
