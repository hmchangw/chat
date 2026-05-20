//go:build integration

package main

// Per-package shared test infrastructure. Containers (ES, NATS, Valkey,
// Mongo) come from pkg/testutil and are reaped by testutil.TerminateAll
// in TestMain. CCS tests bring their own ES pair (see integration_ccs_test.go).

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

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

const testUserRoomIndex = "user-room"

// testHTTPClient bounds ES control-plane calls so a stalled container can't hang the job.
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

// TestMain pre-warms the shared containers concurrently so the first test
// doesn't pay their startup serially, then explicitly terminates them on
// clean exit. TerminateAll runs first so containers disappear immediately;
// Ryuk is the safety net for SIGKILL / Ctrl+C where m.Run never returns.
//
// A pre-warm failure aborts the run with code 1 — better than letting
// every test fail individually with confusing "couldn't start container"
// errors.
func TestMain(m *testing.M) {
	var wg sync.WaitGroup
	errCh := make(chan error, 3)
	for _, fn := range []func() error{
		testutil.EnsureElasticsearch,
		testutil.EnsureNATS,
		testutil.EnsureValkey,
	} {
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
	code := m.Run()
	testutil.TerminateAll()
	os.Exit(code)
}

// uniqueESIndex returns a per-test ES index name derived from t.Name() and
// registers a cleanup that DELETEs the index. The fnv hash keeps the name
// short, deterministic per test, and free of characters that ES dislikes
// (slashes from subtests).
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

// freshValkeyClient returns a valkeyutil.Client connected to the shared
// Valkey, with cleanup that flushes the keyspace at test end so the next
// test starts clean. Tests in this package run sequentially.
func freshValkeyClient(t *testing.T) valkeyutil.Client {
	t.Helper()
	addr := testutil.Valkey(t)
	client, err := valkeyutil.Connect(context.Background(), addr, "")
	require.NoError(t, err, "connect shared valkey")
	t.Cleanup(func() {
		flushValkey(t, addr)
		valkeyutil.Disconnect(client)
	})
	return client
}

// flushValkey wipes the keyspace at addr. Uses a raw go-redis client so we
// don't have to expose FLUSHDB on the production valkeyutil.Client. A
// FLUSHDB failure is fatal: state would leak into the next sibling test.
func flushValkey(t *testing.T, addr string) {
	t.Helper()
	rc := goredis.NewClient(&goredis.Options{Addr: addr})
	defer func() { _ = rc.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rc.FlushDB(ctx).Err(); err != nil {
		t.Errorf("flush valkey at %s: %v", addr, err)
	}
}
