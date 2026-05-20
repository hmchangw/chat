//go:build integration

package testutil

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

var (
	esOnce      sync.Once
	esContainer testcontainers.Container
	esURL       string
	esInitErr   error
)

func ensureElasticsearch() (string, error) {
	esOnce.Do(func() {
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
			esInitErr = fmt.Errorf("start elasticsearch: %w", err)
			return
		}
		host, err := container.Host(ctx)
		if err != nil {
			_ = container.Terminate(ctx)
			esInitErr = fmt.Errorf("get es host: %w", err)
			return
		}
		port, err := container.MappedPort(ctx, "9200")
		if err != nil {
			_ = container.Terminate(ctx)
			esInitErr = fmt.Errorf("get es port: %w", err)
			return
		}
		esContainer = container
		esURL = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
	return esURL, esInitErr
}

// Elasticsearch returns the URL of a process-shared single-node ES container.
func Elasticsearch(t *testing.T) string {
	t.Helper()
	u, err := ensureElasticsearch()
	if err != nil {
		t.Fatalf("testutil.Elasticsearch: %v", err)
	}
	return u
}

// EnsureElasticsearch starts the shared ES container if not already
// started. No-t variant intended for TestMain pre-warming.
func EnsureElasticsearch() error { _, err := ensureElasticsearch(); return err }

// TerminateElasticsearch stops the shared ES container. Best-effort and
// idempotent — safe to call from TestMain even if no test touched ES.
func TerminateElasticsearch() {
	if esContainer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := esContainer.Terminate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "terminate shared elasticsearch: %v\n", err)
	}
	esContainer = nil
}
