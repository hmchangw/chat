//go:build integration

package main

// Per-package shared test infrastructure. Containers come from
// pkg/testutil; CCS tests bring their own ES pair (integration_ccs_test.go).

import (
	"bytes"
	"context"
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
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

const testUserRoomIndex = "user-room"

// NATS queue groups. Each search-service router gets its own so a slow
// drain after one test can't deliver to a sibling test's handler.
const (
	testQueueGroup     = "search-service-test"      // apps, users, CCS
	testQueueGroupSubs = "search-service-test-subs" // rooms
	testQueueGroupV2   = "search-service-test-v2"   // messages v2
)

// testHTTPClient bounds ES control-plane calls so a stalled container can't hang the job.
var testHTTPClient = &http.Client{Timeout: 10 * time.Second}

// seedDoc PUTs a JSON document into ES, synchronously refreshing the index.
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

// TestMain pre-warms the shared containers concurrently so the first test
// doesn't pay their startup serially. A pre-warm failure aborts the run
// before m.Run rather than letting every test fail individually.
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

// uniqueESIndex returns a per-test ES index name derived from t.Name() and
// registers cleanup that DELETEs the index. The fnv hash keeps the name
// short and free of characters ES dislikes (slashes from subtests).
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

// valkeyClient returns a valkeyutil.Client connected to the shared Valkey,
// with FLUSHDB on cleanup so sibling tests start clean. Tests in this
// package run sequentially.
func valkeyClient(t *testing.T) valkeyutil.Client {
	t.Helper()
	client, err := valkeyutil.Connect(context.Background(), testutil.Valkey(t), "")
	require.NoError(t, err, "connect shared valkey")
	t.Cleanup(func() {
		testutil.FlushValkey(t)
		valkeyutil.Disconnect(client)
	})
	return client
}
