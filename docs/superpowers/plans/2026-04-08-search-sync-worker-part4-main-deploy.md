# Search Sync Worker — Part 4: Main, Template, and Deploy

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire up the search-sync-worker main.go with Fetch-based batch consumer, ES template management, and deploy artifacts.

**Architecture:** Batch consumer loop using `cons.Fetch()` with FetchMaxWait for multiplexing fetch/timer/stop. Upserts ES index template on startup. First-message verification catches stale mappings.

**Tech Stack:** Go, NATS JetStream, pkg/searchengine, pkg/stream, pkg/shutdown, Docker

**Spec:** `docs/superpowers/specs/2026-04-07-search-sync-worker-design.md` — "Consumer Design", "ES Index Template Management", "Service Configuration" sections

**Depends on:** Part 1, Part 2, Part 3

**Push after each commit:** `git push -u origin claude/implement-search-sync-worker-yFtCC`

---

## File Structure

| Action | File | Purpose |
|--------|------|---------|
| Create | `search-sync-worker/template.go` | ES index template JSON + first-message verification |
| Create | `search-sync-worker/template_test.go` | Tests for template verification logic |
| Create | `search-sync-worker/main.go` | Config, wiring, Fetch-based batch consumer loop |
| Create | `search-sync-worker/deploy/Dockerfile` | Multi-stage Docker build |
| Create | `search-sync-worker/deploy/docker-compose.yml` | Local dev with NATS + ES |
| Create | `search-sync-worker/deploy/azure-pipelines.yml` | CI/CD pipeline |

---

### Task 9: ES Index Template and Verification

**Files:**
- Create: `search-sync-worker/template.go`
- Create: `search-sync-worker/template_test.go`

- [ ] **Step 1: Write failing tests in `search-sync-worker/template_test.go`**

```go
package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateBody(t *testing.T) {
	body := templateBody("messages-site1-v1")
	require.NotNil(t, body)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	// Verify index_patterns uses the configured prefix
	patterns, ok := parsed["index_patterns"].([]any)
	require.True(t, ok)
	assert.Equal(t, "messages-site1-v1-*", patterns[0])

	// Verify mapping has expected fields
	tmpl := parsed["template"].(map[string]any)
	mappings := tmpl["mappings"].(map[string]any)
	props := mappings["properties"].(map[string]any)
	assert.Contains(t, props, "messageId")
	assert.Contains(t, props, "roomId")
	assert.Contains(t, props, "msg")
	assert.Contains(t, props, "createdAt")
	assert.Contains(t, props, "siteId")
	assert.Contains(t, props, "senderUsername")

	// Verify dynamic is false
	assert.Equal(t, false, mappings["dynamic"])

	// Verify custom analyzer exists
	settings := tmpl["settings"].(map[string]any)
	analysis := settings["analysis"].(map[string]any)
	analyzers := analysis["analyzer"].(map[string]any)
	assert.Contains(t, analyzers, "custom_analyzer")
}

func TestTemplateName(t *testing.T) {
	assert.Equal(t, "messages-site1-v1_template", templateName("messages-site1-v1"))
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./search-sync-worker/ -run TestTemplate -v`
Expected: FAIL — `templateBody`, `templateName` undefined

- [ ] **Step 3: Implement `search-sync-worker/template.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
)

// templateName returns the ES index template name for the given prefix.
func templateName(prefix string) string {
	return fmt.Sprintf("%s_template", prefix)
}

// templateBody returns the ES index template JSON for the given prefix.
// The template uses dynamic: false and a custom underscore-preserving analyzer.
func templateBody(prefix string) json.RawMessage {
	tmpl := map[string]any{
		"index_patterns": []string{fmt.Sprintf("%s-*", prefix)},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   4,
					"number_of_replicas": 2,
					"refresh_interval":   "30s",
				},
				"analysis": map[string]any{
					"analyzer": map[string]any{
						"custom_analyzer": map[string]any{
							"type":        "custom",
							"tokenizer":   "underscore_preserving",
							"filter":      []string{"underscore_subword", "cjk_bigram", "lowercase"},
							"char_filter": []string{"html_strip"},
						},
					},
					"tokenizer": map[string]any{
						"underscore_preserving": map[string]any{
							"type":    "pattern",
							"pattern": `[\s,;!?()\[\]{}"'<>]+`,
						},
					},
					"filter": map[string]any{
						"underscore_subword": map[string]any{
							"type":                  "word_delimiter_graph",
							"split_on_case_change":  false,
							"split_on_numerics":     false,
							"preserve_original":     true,
						},
					},
				},
			},
			"mappings": map[string]any{
				"dynamic": false,
				"properties": map[string]any{
					"messageId":             map[string]any{"type": "keyword"},
					"roomId":                map[string]any{"type": "keyword"},
					"siteId":                map[string]any{"type": "keyword"},
					"senderUsername":         map[string]any{"type": "keyword"},
					"senderEngName":          map[string]any{"type": "keyword"},
					"msg":                    map[string]any{"type": "text", "analyzer": "custom_analyzer"},
					"attachmentText":         map[string]any{"type": "text", "analyzer": "custom_analyzer"},
					"cardData":               map[string]any{"type": "text", "analyzer": "custom_analyzer"},
					"createdAt":              map[string]any{"type": "date"},
					"updatedAt":              map[string]any{"type": "date"},
					"editedAt":               map[string]any{"type": "date"},
					"threadParentId":         map[string]any{"type": "keyword"},
					"threadParentCreatedAt":  map[string]any{"type": "date"},
					"tshow":                  map[string]any{"type": "boolean"},
					"visibleTo":              map[string]any{"type": "keyword"},
					"deleted":                map[string]any{"type": "boolean"},
					"type":                   map[string]any{"type": "keyword"},
				},
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./search-sync-worker/ -run TestTemplate -v -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add search-sync-worker/template.go search-sync-worker/template_test.go
git commit -m "feat(search-sync-worker): add ES index template with custom analyzer"
```

---

### Task 10: main.go — Fetch-based batch consumer

**Files:**
- Create: `search-sync-worker/main.go`

- [ ] **Step 1: Create `search-sync-worker/main.go`**

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
)

type config struct {
	NatsURL        string `env:"NATS_URL,required"`
	SiteID         string `env:"SITE_ID,required"`
	SearchURL      string `env:"SEARCH_URL,required"`
	SearchBackend  string `env:"SEARCH_BACKEND"  envDefault:"elasticsearch"`
	MsgIndexPrefix string `env:"MSG_INDEX_PREFIX,required"`
	BatchSize      int    `env:"BATCH_SIZE"      envDefault:"500"`
	FlushInterval  int    `env:"FLUSH_INTERVAL"  envDefault:"5"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "search-sync-worker")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	// Connect to search engine
	engine, err := searchengine.New(ctx, cfg.SearchBackend, cfg.SearchURL)
	if err != nil {
		slog.Error("search engine connect failed", "error", err)
		os.Exit(1)
	}

	// Upsert index template
	tmplName := templateName(cfg.MsgIndexPrefix)
	tmplBody := templateBody(cfg.MsgIndexPrefix)
	if err := engine.UpsertTemplate(ctx, tmplName, tmplBody); err != nil {
		slog.Error("upsert index template failed", "error", err)
		os.Exit(1)
	}
	slog.Info("index template upserted", "name", tmplName)

	// Connect to NATS
	nc, err := otelnats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	js, err := oteljetstream.New(nc)
	if err != nil {
		slog.Error("jetstream init failed", "error", err)
		os.Exit(1)
	}

	// Ensure MESSAGES_CANONICAL stream exists
	canonicalCfg := stream.MessagesCanonical(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonicalCfg.Name,
		Subjects: canonicalCfg.Subjects,
	}); err != nil {
		slog.Error("create MESSAGES_CANONICAL stream failed", "error", err)
		os.Exit(1)
	}

	// Create durable consumer with backoff
	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
		Durable:   "search-sync-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
		BackOff:   []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second},
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	// Create handler — wraps SearchEngine as Store via adapter
	handler := NewHandler(&engineAdapter{engine: engine}, cfg.MsgIndexPrefix, cfg.BatchSize)

	flushInterval := time.Duration(cfg.FlushInterval) * time.Second
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	// Batch consumer loop
	go func() {
		defer close(doneCh)
		lastFlush := time.Now()

		for {
			select {
			case <-stopCh:
				handler.Flush(ctx)
				return
			default:
			}

			fetchSize := cfg.BatchSize - handler.BufferLen()
			if fetchSize <= 0 {
				fetchSize = 1
			}

			batch, err := cons.Fetch(fetchSize, jetstream.FetchMaxWait(time.Second))
			if err != nil {
				if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
					handler.Flush(ctx)
					return
				}
				// Check stop after fetch error
				select {
				case <-stopCh:
					handler.Flush(ctx)
					return
				default:
				}
				// Timer flush on fetch timeout
				if handler.BufferLen() > 0 && time.Since(lastFlush) >= flushInterval {
					handler.Flush(ctx)
					lastFlush = time.Now()
				}
				continue
			}

			for msg := range batch.Messages() {
				handler.Add(msg)
			}

			// Check flush conditions
			if handler.BufferFull() {
				handler.Flush(ctx)
				lastFlush = time.Now()
			} else if handler.BufferLen() > 0 && time.Since(lastFlush) >= flushInterval {
				handler.Flush(ctx)
				lastFlush = time.Now()
			}
		}
	}()

	slog.Info("search-sync-worker running", "site", cfg.SiteID, "prefix", cfg.MsgIndexPrefix)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			close(stopCh)
			return nil
		},
		func(ctx context.Context) error {
			select {
			case <-doneCh:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("consumer loop drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { return nc.Drain() },
	)
}

// engineAdapter adapts searchengine.SearchEngine to the Handler's Store interface.
type engineAdapter struct {
	engine searchengine.SearchEngine
}

func (a *engineAdapter) Bulk(ctx context.Context, actions []searchengine.BulkAction) ([]searchengine.BulkResult, error) {
	return a.engine.Bulk(ctx, actions)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./search-sync-worker/`
Expected: Success (compilation only — no runtime test)

- [ ] **Step 3: Commit**

```bash
git add search-sync-worker/main.go
git commit -m "feat(search-sync-worker): add main.go with Fetch-based batch consumer loop"
```

---

### Task 11: Deploy artifacts

**Files:**
- Create: `search-sync-worker/deploy/Dockerfile`
- Create: `search-sync-worker/deploy/docker-compose.yml`
- Create: `search-sync-worker/deploy/azure-pipelines.yml`

- [ ] **Step 1: Create `search-sync-worker/deploy/Dockerfile`**

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY search-sync-worker/ search-sync-worker/
RUN CGO_ENABLED=0 go build -o /search-sync-worker ./search-sync-worker/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /search-sync-worker /search-sync-worker
ENTRYPOINT ["/search-sync-worker"]
```

- [ ] **Step 2: Create `search-sync-worker/deploy/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  elasticsearch:
    image: docker.elastic.co/elasticsearch/elasticsearch:8.17.0
    ports:
      - "9200:9200"
    environment:
      - discovery.type=single-node
      - xpack.security.enabled=false
      - ES_JAVA_OPTS=-Xms512m -Xmx512m

  search-sync-worker:
    build:
      context: ../..
      dockerfile: search-sync-worker/deploy/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - SEARCH_URL=http://elasticsearch:9200
      - SEARCH_BACKEND=elasticsearch
      - MSG_INDEX_PREFIX=messages-site-local-v1
    depends_on:
      - nats
      - elasticsearch
```

- [ ] **Step 3: Create `search-sync-worker/deploy/azure-pipelines.yml`**

```yaml
trigger:
  branches:
    include:
      - main
      - develop
  paths:
    include:
      - search-sync-worker/
      - pkg/

pr:
  branches:
    include:
      - main
  paths:
    include:
      - search-sync-worker/
      - pkg/

variables:
  GO_VERSION: '1.25'
  SERVICE_NAME: search-sync-worker
  REGISTRY: '$(containerRegistry)'

stages:
  - stage: Validate
    displayName: 'Lint & Test'
    jobs:
      - job: LintAndTest
        pool:
          vmImage: 'ubuntu-latest'
        steps:
          - task: GoTool@0
            inputs:
              version: '$(GO_VERSION)'
            displayName: 'Install Go $(GO_VERSION)'

          - script: go vet ./$(SERVICE_NAME)/... ./pkg/...
            displayName: 'Go Vet'

          - script: go test ./pkg/... -v -race -coverprofile=coverage-pkg.out
            displayName: 'Test shared packages'

          - script: go test ./$(SERVICE_NAME)/... -v -race -coverprofile=coverage-$(SERVICE_NAME).out
            displayName: 'Test $(SERVICE_NAME)'

          - script: go build -o /dev/null ./$(SERVICE_NAME)/
            displayName: 'Build $(SERVICE_NAME)'

  - stage: Build
    displayName: 'Build & Push Image'
    dependsOn: Validate
    condition: and(succeeded(), eq(variables['Build.SourceBranch'], 'refs/heads/main'))
    jobs:
      - job: BuildImage
        pool:
          vmImage: 'ubuntu-latest'
        steps:
          - task: Docker@2
            inputs:
              containerRegistry: '$(containerRegistry)'
              repository: 'chat/$(SERVICE_NAME)'
              command: 'buildAndPush'
              Dockerfile: '$(SERVICE_NAME)/deploy/Dockerfile'
              buildContext: '.'
              tags: |
                $(Build.BuildId)
                latest
            displayName: 'Build & push $(SERVICE_NAME)'
```

- [ ] **Step 4: Verify build compiles**

Run: `make build SERVICE=search-sync-worker`
Expected: Success

- [ ] **Step 5: Run all search-sync-worker tests**

Run: `make test SERVICE=search-sync-worker`
Expected: PASS

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 7: Commit and push**

```bash
git add search-sync-worker/deploy/
git commit -m "feat(search-sync-worker): add Dockerfile, docker-compose, and CI pipeline"
git push -u origin claude/implement-search-sync-worker-yFtCC
```

---

## Final Verification

After all tasks are complete:

- [ ] `make test` — all unit tests pass
- [ ] `make lint` — no lint errors
- [ ] `make build SERVICE=search-sync-worker` — binary builds
- [ ] All changes pushed to `claude/implement-search-sync-worker-yFtCC`
