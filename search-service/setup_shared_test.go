//go:build integration

package main

// This file owns the process-shared test infrastructure used by every
// per-endpoint integration_*_test.go file. Each container is started
// exactly once via sync.Once and lives for the entire `go test` run;
// Ryuk (from testcontainers-go) reaps it after the process exits.
//
// Sharing is safe because tests within this package run sequentially and
// each fixture isolates state per-test:
//
//   - Elasticsearch: unique index name per test (uniqueESIndex), DELETEd
//     on cleanup.
//   - Valkey:        flushValkey wipes the keyspace on cleanup.
//   - NATS:          each test creates its own *nats.Conn pair and
//     router.Shutdown / nc.Close remove subscriptions before the next
//     test starts. Each fixture also uses a distinct queue group name.
//
// CCS tests are the one exception — they need two networked ES nodes and
// stand up their own pair inside setupCCSFixture. They still piggyback on
// the shared Valkey and NATS, since those don't care about the topology.
//
// This file also owns the test-wide constants and helpers that all
// integration files share: testUserRoomIndex, testHTTPClient, and seedDoc.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	natsmod "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

const testUserRoomIndex = "user-room"

// testHTTPClient is a bounded HTTP client for ES control-plane calls —
// stalled containers shouldn't be able to hang the integration job past
// the per-call deadline. Kept small on purpose: these calls hit localhost
// (docker-mapped port) and are cheap when they succeed.
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

var (
	sharedESOnce sync.Once
	sharedESURL  string
	sharedESErr  error

	sharedValkeyOnce sync.Once
	sharedValkeyAddr string
	sharedValkeyErr  error

	sharedNATSOnce sync.Once
	sharedNATSURL  string
	sharedNATSErr  error
)

// sharedSingleNodeES returns the URL of a process-shared single-node ES
// container. CCS tests do NOT use this — they need a pair of networked
// clusters and stand up their own.
func sharedSingleNodeES(t *testing.T) string {
	t.Helper()
	sharedESOnce.Do(func() {
		ctx := context.Background()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        testimages.Elasticsearch,
				ExposedPorts: []string{"9200/tcp"},
				Env: map[string]string{
					"discovery.type":         "single-node",
					"xpack.security.enabled": "false",
					"ES_JAVA_OPTS":           "-Xms512m -Xmx512m",
					"cluster.routing.allocation.disk.threshold_enabled": "false",
				},
				WaitingFor: wait.ForAll(
					wait.ForHTTP("/").WithPort("9200/tcp").WithStartupTimeout(120*time.Second),
					wait.ForHTTP("/_cluster/health?wait_for_status=yellow&timeout=60s").
						WithPort("9200/tcp").
						WithStartupTimeout(120*time.Second),
				),
			},
			Started: true,
		})
		if err != nil {
			sharedESErr = fmt.Errorf("start shared elasticsearch: %w", err)
			return
		}
		host, err := container.Host(ctx)
		if err != nil {
			_ = container.Terminate(ctx)
			sharedESErr = fmt.Errorf("get shared es host: %w", err)
			return
		}
		port, err := container.MappedPort(ctx, "9200")
		if err != nil {
			_ = container.Terminate(ctx)
			sharedESErr = fmt.Errorf("get shared es port: %w", err)
			return
		}
		sharedESURL = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
	if sharedESErr != nil {
		t.Fatalf("shared elasticsearch: %v", sharedESErr)
	}
	return sharedESURL
}

// sharedValkey returns the addr of a process-shared Valkey container.
// Callers should obtain a fresh client via freshValkeyClient so the
// keyspace is wiped on test cleanup.
func sharedValkey(t *testing.T) string {
	t.Helper()
	sharedValkeyOnce.Do(func() {
		ctx := context.Background()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        testimages.Valkey,
				ExposedPorts: []string{"6379/tcp"},
				Cmd:          []string{"valkey-server", "--save", "", "--appendonly", "no"},
				WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(30 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			sharedValkeyErr = fmt.Errorf("start shared valkey: %w", err)
			return
		}
		host, err := container.Host(ctx)
		if err != nil {
			_ = container.Terminate(ctx)
			sharedValkeyErr = fmt.Errorf("get shared valkey host: %w", err)
			return
		}
		port, err := container.MappedPort(ctx, "6379")
		if err != nil {
			_ = container.Terminate(ctx)
			sharedValkeyErr = fmt.Errorf("get shared valkey port: %w", err)
			return
		}
		sharedValkeyAddr = fmt.Sprintf("%s:%s", host, port.Port())
	})
	if sharedValkeyErr != nil {
		t.Fatalf("shared valkey: %v", sharedValkeyErr)
	}
	return sharedValkeyAddr
}

// sharedNATS returns the URL of a process-shared NATS container.
func sharedNATS(t *testing.T) string {
	t.Helper()
	sharedNATSOnce.Do(func() {
		ctx := context.Background()
		c, err := natsmod.Run(ctx, testimages.NATS,
			testcontainers.WithWaitStrategy(wait.ForLog("Server is ready").WithStartupTimeout(60*time.Second)),
		)
		if err != nil {
			sharedNATSErr = fmt.Errorf("start shared nats: %w", err)
			return
		}
		url, err := c.ConnectionString(ctx)
		if err != nil {
			_ = c.Terminate(ctx)
			sharedNATSErr = fmt.Errorf("get shared nats url: %w", err)
			return
		}
		sharedNATSURL = url
	})
	if sharedNATSErr != nil {
		t.Fatalf("shared nats: %v", sharedNATSErr)
	}
	return sharedNATSURL
}

// uniqueESIndex returns a per-test ES index name derived from t.Name()
// and registers a cleanup that DELETEs the index from the shared ES
// when the test ends. The hash keeps the name short, deterministic per
// test, and free of characters that ES dislikes (slashes from subtests).
func uniqueESIndex(t *testing.T, prefix string) string {
	t.Helper()
	esURL := sharedSingleNodeES(t)
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
// test starts clean. Tests in this package run sequentially, so a flush
// is sufficient isolation.
func freshValkeyClient(t *testing.T) valkeyutil.Client {
	t.Helper()
	addr := sharedValkey(t)
	client, err := valkeyutil.Connect(context.Background(), addr, "")
	require.NoError(t, err, "connect shared valkey")
	t.Cleanup(func() {
		flushValkey(t, addr)
		valkeyutil.Disconnect(client)
	})
	return client
}

// flushValkey wipes the keyspace at addr. Uses a raw go-redis client so
// we don't have to expose FLUSHDB on the production valkeyutil.Client
// interface. A FLUSHDB failure here is fatal to the test: state would
// leak into the next sibling test and produce a confusing assertion
// failure far from the real root cause.
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
