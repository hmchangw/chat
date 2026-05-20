//go:build integration

package main

// Process-shared ES, Valkey, and NATS containers used by every
// integration_*_test.go file. Each starts once via sync.Once and is
// reaped by Ryuk at process exit. Tests run sequentially and isolate
// per-test via uniqueESIndex (DELETE on cleanup), Valkey FLUSHDB on
// cleanup, and a fresh *nats.Conn pair per test. CCS tests bring their
// own ES pair.

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
	"github.com/testcontainers/testcontainers-go"
	natsmod "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil"
	"github.com/hmchangw/chat/pkg/testutil/testimages"
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

var (
	sharedESOnce      sync.Once
	sharedESContainer testcontainers.Container
	sharedESURL       string
	sharedESErr       error

	sharedValkeyOnce      sync.Once
	sharedValkeyContainer testcontainers.Container
	sharedValkeyAddr      string
	sharedValkeyErr       error

	sharedNATSOnce      sync.Once
	sharedNATSContainer testcontainers.Container
	sharedNATSURL       string
	sharedNATSErr       error
)

// TestMain pre-warms the shared containers concurrently so the first
// test doesn't pay their startup serially, then explicitly terminates
// them on clean exit. Explicit cleanup is required because CI runs with
// TESTCONTAINERS_RYUK_DISABLED=true — Ryuk would otherwise reap the
// shared containers (they have no t.Cleanup). Locally Ryuk is enabled
// as a safety net for SIGKILL / Ctrl+C, where m.Run never returns.
func TestMain(m *testing.M) {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); ensureSharedES() }()
	go func() { defer wg.Done(); ensureSharedValkey() }()
	go func() { defer wg.Done(); ensureSharedNATS() }()
	wg.Wait()
	code := m.Run()
	terminateShared()
	os.Exit(code)
}

// terminateShared best-effort kills every shared container. Errors are
// logged but don't change the test exit code — the tests already passed
// or failed by this point.
func terminateShared() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for name, c := range map[string]testcontainers.Container{
		"elasticsearch": sharedESContainer,
		"valkey":        sharedValkeyContainer,
		"nats":          sharedNATSContainer,
	} {
		if c == nil {
			continue
		}
		if err := c.Terminate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "terminate shared %s: %v\n", name, err)
		}
	}
	testutil.TerminateMongo()
}

// sharedSingleNodeES returns the URL of the process-shared single-node ES.
func sharedSingleNodeES(t *testing.T) string {
	t.Helper()
	ensureSharedES()
	if sharedESErr != nil {
		t.Fatalf("shared elasticsearch: %v", sharedESErr)
	}
	return sharedESURL
}

func ensureSharedES() {
	sharedESOnce.Do(func() {
		ctx := context.Background()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        testimages.Elasticsearch,
				ExposedPorts: []string{"9200/tcp"},
				Env: map[string]string{
					"discovery.type":         "single-node",
					"xpack.security.enabled": "false",
					"ES_JAVA_OPTS":           "-Xms256m -Xmx256m",
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
		sharedESContainer = container
		sharedESURL = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
}

// sharedValkey returns the addr of a process-shared Valkey container.
// Callers should obtain a fresh client via freshValkeyClient so the
// keyspace is wiped on test cleanup.
func sharedValkey(t *testing.T) string {
	t.Helper()
	ensureSharedValkey()
	if sharedValkeyErr != nil {
		t.Fatalf("shared valkey: %v", sharedValkeyErr)
	}
	return sharedValkeyAddr
}

func ensureSharedValkey() {
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
		sharedValkeyContainer = container
		sharedValkeyAddr = fmt.Sprintf("%s:%s", host, port.Port())
	})
}

// sharedNATS returns the URL of a process-shared NATS container.
func sharedNATS(t *testing.T) string {
	t.Helper()
	ensureSharedNATS()
	if sharedNATSErr != nil {
		t.Fatalf("shared nats: %v", sharedNATSErr)
	}
	return sharedNATSURL
}

func ensureSharedNATS() {
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
		sharedNATSContainer = c
		sharedNATSURL = url
	})
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
