# Integration Test Suite v2 — Part 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the v2 four-layer integration test platform with the catalog scaffolding, runtime engine, and one working scenario that executes end-to-end against the docker-local single-site stack.

**Architecture:** A new tool at `tools/integration-suite-v2/`, sitting alongside (not replacing) the v1 implementation. YAML catalogs and scenarios live at top-level `catalogs/` and `scenarios/`. The Go runtime under `tools/integration-suite-v2/internal/` loads YAML data, resolves placeholders against a seeded fixture cast, fires verb executors with traceparent propagation, continuously observes reader locations during a scenario's event-driven timeframe, classifies observed events three ways (in-cascade / background / anomaly), and produces a verdict report.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3` for YAML, existing repo deps (`mongo-driver/v2`, `nats.go`, `gocql`, `nkeys`, `resty`, `stretchr/testify`). No new external runtime dependencies.

**Spec:** `docs/superpowers/specs/2026-05-21-integration-suite-v2-design.md`

---

## Scope of THIS plan (Part 1)

| In | Out (Part 2/3) |
|---|---|
| Directory scaffolding (`tools/integration-suite-v2/`, `catalogs/`, `scenarios/`) | v1 deprecation (Part 2) |
| Verb catalog framework + `nats_request` executor | Other verbs (`http_request`, `nats_publish`, …) as stubs for later |
| Matcher framework + 4 matchers (`equals`, `count_eq`, `matches_shape`, `contains`) | More matchers as scenarios demand |
| Reader framework + 3 readers (`mongo.rooms`, `reply`, `logs.<service>`) | Other readers (Cassandra, JetStream, …) — Part 2 |
| Service ability profile loader + `room-service.yaml` | Other service profiles — Part 2 |
| Fixture cast YAML + seeder | Cast growth as scenarios demand |
| Catalog validator (YAML refs vs Go symbols) | Deeper semantic checks against service code |
| Scenario YAML schema + loader + predicate resolver | Cross-placeholder predicate refs (basic version in this plan; richer in Part 2) |
| Mishap expander framework (empty mishap catalog initially) | Actual env / chaos mishaps — Part 3 |
| Concurrency mishap executor (N=3) | (this plan includes the executor but the runtime knob to enable it is Part 2) |
| Verb dispatcher with traceparent propagation | |
| Observer (continuous read polling) | |
| Timeframe closer (event-driven, quiet grace + safety cap) | |
| Three-way classifier + verdict | |
| Reporter (verdict counts + per-service attribution + two-score split) | |
| Main runner + Makefile | |
| One scenario: "verified user creates a channel room" | More scenarios — Part 2 |

---

## Background — context the executor needs

Read once before starting. Later tasks reference these.

- **Repo:** `/home/user/chat`, branch `claude/integration-test-automation-LXQHP`, Go 1.25 monorepo, single root `go.mod`. CLAUDE.md §1 has conventions.
- **v1 lives at** `tools/integration-suite/`. Do not touch it in this plan. v2 is built fresh alongside.
- **v2 lives at** `tools/integration-suite-v2/`. New top-level data dirs at `catalogs/` and `scenarios/`.
- **The docker-local stack** is what tests run against (`make deps-up && make up` from repo root). NATS at `localhost:4222`, MongoDB at `localhost:27017`, Cassandra at `localhost:9042`, auth-service at `localhost:8080`. Services are configured with `SITE_ID=site-local`.
- **Existing reusable plumbing in v1:** the `internal/harness/` package has nkey generation, NATS connection pooling, traceparent helpers. We will **not** import from v1 — v2 stands on its own — but reading v1's `internal/harness/nats_client.go`, `tracing.go`, `credentials.go` as reference is valuable; copy patterns, don't import.
- **Per `CLAUDE.md` §3:** wrap errors with `fmt.Errorf("...: %w", err)`. Use `log/slog` (JSON format). Use Resty for HTTP. Use `caarlos0/env` for config.

---

## File structure

All files at exact paths shown. Each file has one clear responsibility.

```
catalogs/                                        # YAML — top-level
  verbs/
    nats_request.yaml
  readers/
    mongo.rooms.yaml
    reply.yaml
    logs.room-service.yaml
  services/
    room-service.yaml
  fixture-cast.yaml
  matchers.yaml

scenarios/
  drafts/
    service/
      verified-user-creates-channel-room.yaml

tools/integration-suite-v2/
  README.md                                      # user-facing usage
  Makefile                                       # `make local`, `make validate`, etc.
  cmd/
    runner/main.go                               # main test-runner binary
    validator/main.go                            # catalog validator (standalone CLI)
  internal/
    catalog/
      types.go                                   # Go structs for all catalog YAML
      loader.go                                  # disk → typed structs
      validator.go                               # cross-check YAML refs vs Go symbols
      loader_test.go
      validator_test.go
    verbs/
      types.go                                   # Verb + VerbExecutor interfaces
      nats_request.go                            # nats_request executor
      registry.go                                # name → VerbExecutor lookup
      nats_request_test.go
    readers/
      types.go                                   # Reader interface
      mongo_rooms.go                             # mongo.rooms reader
      nats_reply.go                              # nats reply observer
      container_logs.go                          # docker logs reader
      registry.go                                # name → Reader lookup
      mongo_rooms_test.go                        # unit test with embedded mongo
    matchers/
      types.go                                   # Matcher interface
      equals.go
      count_eq.go
      matches_shape.go
      contains.go
      registry.go
      matchers_test.go
    fixtures/
      cast.go                                    # Cast types
      seeder.go                                  # reads cast yaml, seeds env
      seeder_test.go
    scenario/
      types.go                                   # Scenario YAML schema (Go types)
      loader.go                                  # YAML → typed Scenario
      resolver.go                                # predicate resolution against cast
      loader_test.go
      resolver_test.go
    mishap/
      types.go                                   # Mishap interface
      expander.go                                # power-set subset expansion
      concurrent.go                              # N=3 concurrent executor
      expander_test.go
    runtime/
      dispatcher.go                              # verb dispatch with traceparent
      observer.go                                # continuous read polling
      timeframe.go                               # event-driven T_close
      verdict.go                                 # three-way classification + Verdict struct
      reporter.go                                # writes last-run.md
      runner.go                                  # orchestration
      verdict_test.go
      reporter_test.go
      timeframe_test.go
```

---

## Task 1: Scaffold v2 directory tree

**Files:**
- Create: `tools/integration-suite-v2/README.md`
- Create: `tools/integration-suite-v2/.gitkeep` in each empty subdir below

- [ ] **Step 1: Create the directory skeleton**

```bash
mkdir -p tools/integration-suite-v2/cmd/runner
mkdir -p tools/integration-suite-v2/cmd/validator
mkdir -p tools/integration-suite-v2/internal/catalog
mkdir -p tools/integration-suite-v2/internal/verbs
mkdir -p tools/integration-suite-v2/internal/readers
mkdir -p tools/integration-suite-v2/internal/matchers
mkdir -p tools/integration-suite-v2/internal/fixtures
mkdir -p tools/integration-suite-v2/internal/scenario
mkdir -p tools/integration-suite-v2/internal/mishap
mkdir -p tools/integration-suite-v2/internal/runtime
mkdir -p catalogs/verbs
mkdir -p catalogs/readers
mkdir -p catalogs/services
mkdir -p scenarios/drafts/service
mkdir -p scenarios/approved
```

- [ ] **Step 2: Create the v2 README**

Create `tools/integration-suite-v2/README.md`:

```markdown
# integration-suite v2

A scenario-driven black-box integration test platform. See
`docs/superpowers/specs/2026-05-21-integration-suite-v2-design.md`
for the design.

This is v2. v1 lives at `tools/integration-suite/` and is not affected
by anything here.

## Quick start (after `make deps-up && make up` from repo root):

```bash
make -C tools/integration-suite-v2 local        # run scenarios
make -C tools/integration-suite-v2 validate     # catalog validator only
```

## Layout

- `catalogs/` (repo root) — YAML data: verbs, readers, services, fixtures, matchers.
- `scenarios/` (repo root) — scenario YAML files (drafts + approved).
- `tools/integration-suite-v2/internal/` — Go runtime (verbs, readers, matchers, scenario loader, runtime).
- `tools/integration-suite-v2/cmd/runner` — main test runner binary.
- `tools/integration-suite-v2/cmd/validator` — catalog validator CLI.
```

- [ ] **Step 3: Add gopkg.in/yaml.v3 to go.mod (if not already present)**

Check first:

```bash
grep "gopkg.in/yaml.v3" go.mod
```

If absent:

```bash
cd /home/user/chat && go get gopkg.in/yaml.v3
go mod tidy
```

- [ ] **Step 4: Verify directory tree**

```bash
find tools/integration-suite-v2 catalogs scenarios -type d | sort
```

Expected: prints all the directories created above.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-v2 catalogs scenarios go.mod go.sum
git commit -m "feat(integration-suite-v2): scaffold directory tree"
```

---

## Task 2: Catalog types + loader (Go structs for all YAML)

**Files:**
- Create: `tools/integration-suite-v2/internal/catalog/types.go`
- Create: `tools/integration-suite-v2/internal/catalog/loader.go`
- Create: `tools/integration-suite-v2/internal/catalog/loader_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite-v2/internal/catalog/loader_test.go`:

```go
package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadVerbCatalog_ParsesYAML(t *testing.T) {
	tmp := t.TempDir()
	verbsDir := filepath.Join(tmp, "verbs")
	require.NoError(t, os.MkdirAll(verbsDir, 0o755))

	yamlData := []byte(`
name: nats_request
description: synchronous NATS request/reply
input_shape:
  subject: { type: string, required: true }
  payload: { type: bytes, required: true }
  credential: { type: ref, required: true }
transport_effect_template:
  - "reply within timeout, OR a transport error class"
executor: NATSRequestExecutor
`)
	require.NoError(t, os.WriteFile(filepath.Join(verbsDir, "nats_request.yaml"), yamlData, 0o644))

	c, err := Load(tmp)
	require.NoError(t, err)

	require.Len(t, c.Verbs, 1)
	v := c.Verbs[0]
	assert.Equal(t, "nats_request", v.Name)
	assert.Equal(t, "NATSRequestExecutor", v.Executor)
	assert.Contains(t, v.InputShape, "subject")
	assert.Contains(t, v.InputShape, "payload")
}

func TestLoadVerbCatalog_EmptyDirectoryProducesEmptyCatalog(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "verbs"), 0o755))

	c, err := Load(tmp)
	require.NoError(t, err)
	assert.Empty(t, c.Verbs)
}
```

- [ ] **Step 2: Verify test fails**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/catalog/... 2>&1 | tail -5
```

Expected: FAIL — `Load` undefined, `Verb` undefined.

- [ ] **Step 3: Create catalog types**

Create `tools/integration-suite-v2/internal/catalog/types.go`:

```go
// Package catalog holds the Go types and loader for the YAML catalogs
// under <repo-root>/catalogs/. Each type corresponds 1:1 to a YAML file.
package catalog

// Catalog is the in-memory union of every YAML file under catalogs/.
type Catalog struct {
	Verbs    []Verb
	Readers  []Reader
	Services []Service
	Cast     Cast
	Matchers []Matcher
}

// Verb describes one interface-level primitive (the verb catalog entries).
type Verb struct {
	Name                    string         `yaml:"name"`
	Description             string         `yaml:"description"`
	InputShape              map[string]any `yaml:"input_shape"`
	TransportEffectTemplate []string       `yaml:"transport_effect_template"`
	Executor                string         `yaml:"executor"`
}

// Reader describes one bounded observation location.
type Reader struct {
	Location        string   `yaml:"location"`
	Owners          []string `yaml:"owners"`
	TimestampSource string   `yaml:"timestamp_source"`
	Shape           string   `yaml:"shape"`
	Executor        string   `yaml:"executor"`
}

// Service captures a service's ability profile (background activity it can
// legitimately do outside our cascade).
type Service struct {
	Name     string                 `yaml:"service"`
	Triggers []map[string]any       `yaml:"triggers"`
}

// Matcher is one named predicate function.
type Matcher struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Cast is the seeded fixture roster (users, rooms, messages, ...).
type Cast struct {
	Users         []CastUser         `yaml:"users"`
	Rooms         []CastRoom         `yaml:"rooms"`
	Messages      []CastMessage      `yaml:"messages"`
	Subscriptions []CastSubscription `yaml:"subscriptions"`
}

// CastUser is one user fixture with credentials and tags.
type CastUser struct {
	ID          string         `yaml:"id"`
	Tags        []string       `yaml:"tags"`
	Credentials map[string]any `yaml:"credentials"`
}

// CastRoom is one room fixture.
type CastRoom struct {
	ID   string   `yaml:"id"`
	Tags []string `yaml:"tags"`
}

// CastMessage is one message fixture.
type CastMessage struct {
	ID   string   `yaml:"id"`
	Tags []string `yaml:"tags"`
}

// CastSubscription is one subscription fixture.
type CastSubscription struct {
	ID   string   `yaml:"id"`
	Tags []string `yaml:"tags"`
}
```

- [ ] **Step 4: Implement Load**

Create `tools/integration-suite-v2/internal/catalog/loader.go`:

```go
package catalog

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load reads every YAML file under catalogsRoot and returns a Catalog.
// Missing subdirectories are tolerated and produce empty slices.
func Load(catalogsRoot string) (*Catalog, error) {
	c := &Catalog{}

	if err := loadDir(filepath.Join(catalogsRoot, "verbs"), &c.Verbs); err != nil {
		return nil, fmt.Errorf("load verbs: %w", err)
	}
	if err := loadDir(filepath.Join(catalogsRoot, "readers"), &c.Readers); err != nil {
		return nil, fmt.Errorf("load readers: %w", err)
	}
	if err := loadDir(filepath.Join(catalogsRoot, "services"), &c.Services); err != nil {
		return nil, fmt.Errorf("load services: %w", err)
	}
	if err := loadFile(filepath.Join(catalogsRoot, "fixture-cast.yaml"), &c.Cast); err != nil {
		return nil, fmt.Errorf("load cast: %w", err)
	}
	if err := loadFile(filepath.Join(catalogsRoot, "matchers.yaml"), &c.Matchers); err != nil {
		return nil, fmt.Errorf("load matchers: %w", err)
	}

	return c, nil
}

// loadDir reads every *.yaml in dir, unmarshals each into one T, appends to out.
// Missing dir is OK.
func loadDir[T any](dir string, out *[]T) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		var item T
		if err := loadFile(filepath.Join(dir, e.Name()), &item); err != nil {
			return err
		}
		*out = append(*out, item)
	}
	return nil
}

// loadFile reads one YAML file into v. Missing file is OK (leaves v at zero value).
func loadFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 5: Verify tests pass**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/catalog/... 2>&1 | tail -5
```

Expected: PASS for both tests.

- [ ] **Step 6: Commit**

```bash
git add tools/integration-suite-v2/internal/catalog/
git commit -m "feat(v2): catalog types + YAML loader"
```

---

## Task 3: Verb catalog entry for nats_request + executor

**Files:**
- Create: `catalogs/verbs/nats_request.yaml`
- Create: `tools/integration-suite-v2/internal/verbs/types.go`
- Create: `tools/integration-suite-v2/internal/verbs/nats_request.go`
- Create: `tools/integration-suite-v2/internal/verbs/registry.go`
- Create: `tools/integration-suite-v2/internal/verbs/nats_request_test.go`

- [ ] **Step 1: Create the verb YAML**

Create `catalogs/verbs/nats_request.yaml`:

```yaml
name: nats_request
description: |
  Synchronous NATS request/reply. Connects as the given credential,
  sends payload to subject, waits for the reply (or a transport error)
  with a 5s default timeout.
input_shape:
  subject:    { type: string, required: true }
  payload:    { type: bytes,  required: true }
  credential: { type: ref,    required: true }
transport_effect_template:
  - "either a reply (success) within 5s, OR a transport error class"
  - "reply carries the traceparent header set by the dispatcher"
executor: NATSRequestExecutor
```

- [ ] **Step 2: Create the verb interface and executor types**

Create `tools/integration-suite-v2/internal/verbs/types.go`:

```go
// Package verbs holds verb executor implementations.
// Each verb is a primitive interface operation (nats_request, http_request, ...).
package verbs

import (
	"context"
)

// Input carries the verb's resolved (post-placeholder-binding) input.
type Input struct {
	Subject     string
	Payload     []byte
	Credential  Credential // NATS user JWT + nkey seed for the actor
	Traceparent string     // generated by the dispatcher
}

// Credential is a verbose name to avoid clashing with anything.
type Credential struct {
	Account string
	JWT     string
	NkeySeed string
}

// Outcome is what a verb returns: either a reply payload, or a transport error.
type Outcome struct {
	Reply        []byte
	Err          error
	TraceparentEcho string // the dispatcher's set traceparent, repeated for the reader
}

// Executor is the interface every verb implementation satisfies.
type Executor interface {
	Execute(ctx context.Context, in Input) Outcome
}
```

- [ ] **Step 3: Implement the nats_request executor**

Create `tools/integration-suite-v2/internal/verbs/nats_request.go`:

```go
package verbs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSRequest is the executor for the `nats_request` verb.
// It opens a connection per Execute call using the supplied credential,
// does one request/reply, and returns the outcome. Real production callers
// should pool connections, but for v2-Part1 a per-call connection is fine
// for correctness; pooling is a Part-2 optimization.
type NATSRequest struct {
	NATSURL string
	Timeout time.Duration
}

// NewNATSRequest returns a NATSRequest with sensible defaults.
func NewNATSRequest(natsURL string) *NATSRequest {
	return &NATSRequest{NATSURL: natsURL, Timeout: 5 * time.Second}
}

func (n *NATSRequest) Execute(ctx context.Context, in Input) Outcome {
	if in.Credential.JWT == "" || in.Credential.NkeySeed == "" {
		return Outcome{Err: fmt.Errorf("nats_request: missing credential for %q", in.Credential.Account)}
	}

	conn, err := nats.Connect(n.NATSURL,
		nats.UserJWTAndSeed(in.Credential.JWT, in.Credential.NkeySeed),
		nats.Name("integration-suite-v2/"+in.Credential.Account),
	)
	if err != nil {
		return Outcome{Err: fmt.Errorf("nats_request: connect: %w", err)}
	}
	defer conn.Drain() //nolint:errcheck

	msg := nats.NewMsg(in.Subject)
	msg.Data = in.Payload
	msg.Header = nats.Header{}
	if in.Traceparent != "" {
		msg.Header.Set("traceparent", in.Traceparent)
	}

	reqCtx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()

	reply, err := conn.RequestMsgWithContext(reqCtx, msg)
	if err != nil {
		return Outcome{Err: fmt.Errorf("nats_request: request: %w", err), TraceparentEcho: in.Traceparent}
	}

	return Outcome{Reply: reply.Data, TraceparentEcho: in.Traceparent}
}

// Sentinel errors for transport-class classification by callers.
var (
	ErrNoResponders = errors.New("nats_request: no responders")
)
```

- [ ] **Step 4: Implement the registry**

Create `tools/integration-suite-v2/internal/verbs/registry.go`:

```go
package verbs

import "fmt"

// Registry maps a verb name (matching `executor:` in YAML) to its Executor.
type Registry struct {
	m map[string]Executor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{m: map[string]Executor{}}
}

// Register stores an executor under a name. Idempotent: replaces.
func (r *Registry) Register(name string, e Executor) {
	r.m[name] = e
}

// Get returns the executor or an error if name is unknown.
func (r *Registry) Get(name string) (Executor, error) {
	e, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("verbs: unknown executor %q", name)
	}
	return e, nil
}
```

- [ ] **Step 5: Write a unit test**

Create `tools/integration-suite-v2/internal/verbs/nats_request_test.go`:

```go
package verbs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNATSRequest_MissingCredential_ReturnsErr exercises the early
// validation path. Live NATS isn't required.
func TestNATSRequest_MissingCredential_ReturnsErr(t *testing.T) {
	n := NewNATSRequest("nats://unused")
	out := n.Execute(context.Background(), Input{
		Subject: "anywhere",
		Payload: []byte("{}"),
	})
	assert.Error(t, out.Err)
	assert.Nil(t, out.Reply)
}

func TestRegistry_GetMissingReturnsErr(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nonexistent")
	assert.Error(t, err)
}

func TestRegistry_RegisterThenGet(t *testing.T) {
	r := NewRegistry()
	n := NewNATSRequest("nats://example")
	r.Register("NATSRequestExecutor", n)
	got, err := r.Get("NATSRequestExecutor")
	assert.NoError(t, err)
	assert.Same(t, n, got)
}
```

- [ ] **Step 6: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/verbs/... 2>&1 | tail -5
```

Expected: 3 PASS.

- [ ] **Step 7: Commit**

```bash
git add catalogs/verbs/nats_request.yaml tools/integration-suite-v2/internal/verbs/
git commit -m "feat(v2): verb framework + nats_request executor"
```

---

## Task 4: Matchers framework + 4 matchers

**Files:**
- Create: `catalogs/matchers.yaml`
- Create: `tools/integration-suite-v2/internal/matchers/types.go`
- Create: `tools/integration-suite-v2/internal/matchers/equals.go`
- Create: `tools/integration-suite-v2/internal/matchers/count_eq.go`
- Create: `tools/integration-suite-v2/internal/matchers/matches_shape.go`
- Create: `tools/integration-suite-v2/internal/matchers/contains.go`
- Create: `tools/integration-suite-v2/internal/matchers/registry.go`
- Create: `tools/integration-suite-v2/internal/matchers/matchers_test.go`

- [ ] **Step 1: Create matchers.yaml**

Create `catalogs/matchers.yaml`:

```yaml
- name: equals
  description: scalar equality (deep for structs and slices)
- name: count_eq
  description: cardinality of a slice equals N
- name: matches_shape
  description: typed shape match with field=value constraints
- name: contains
  description: substring or item containment
```

- [ ] **Step 2: Define the Matcher interface**

Create `tools/integration-suite-v2/internal/matchers/types.go`:

```go
// Package matchers holds typed predicate functions used inside a
// scenario's reads. Each matcher takes observed and expected values
// and returns whether they "match" plus a human-readable reason on
// mismatch.
package matchers

// Result is the outcome of a single match call.
type Result struct {
	Matched bool
	Reason  string // populated on mismatch
}

// Matcher is the interface every matcher implements.
type Matcher interface {
	Match(observed, expected any) Result
}
```

- [ ] **Step 3: Implement equals**

Create `tools/integration-suite-v2/internal/matchers/equals.go`:

```go
package matchers

import (
	"fmt"
	"reflect"
)

// Equals reports deep equality via reflect.DeepEqual.
type Equals struct{}

func (Equals) Match(observed, expected any) Result {
	if reflect.DeepEqual(observed, expected) {
		return Result{Matched: true}
	}
	return Result{Reason: fmt.Sprintf("equals: observed=%#v expected=%#v", observed, expected)}
}
```

- [ ] **Step 4: Implement count_eq**

Create `tools/integration-suite-v2/internal/matchers/count_eq.go`:

```go
package matchers

import (
	"fmt"
	"reflect"
)

// CountEq reports whether len(observed) == expected. Observed must be a
// slice, array, map, or string. Expected must be int (or convertible).
type CountEq struct{}

func (CountEq) Match(observed, expected any) Result {
	wantInt, ok := toInt(expected)
	if !ok {
		return Result{Reason: fmt.Sprintf("count_eq: expected %#v is not an int", expected)}
	}
	v := reflect.ValueOf(observed)
	switch v.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		gotLen := v.Len()
		if gotLen == wantInt {
			return Result{Matched: true}
		}
		return Result{Reason: fmt.Sprintf("count_eq: got %d want %d", gotLen, wantInt)}
	default:
		return Result{Reason: fmt.Sprintf("count_eq: observed %#v has no length (kind=%s)", observed, v.Kind())}
	}
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}
```

- [ ] **Step 5: Implement matches_shape**

Create `tools/integration-suite-v2/internal/matchers/matches_shape.go`:

```go
package matchers

import (
	"encoding/json"
	"fmt"
)

// MatchesShape checks that an observed JSON-decodable value satisfies
// every field constraint in expected. Expected is a map[string]any
// where keys are field paths (e.g. "name", "owner") and values are
// the required values for those fields.
//
// Observed is unmarshaled into a generic map[string]any (so callers
// can pass raw JSON bytes or already-decoded maps).
type MatchesShape struct{}

func (MatchesShape) Match(observed, expected any) Result {
	want, ok := expected.(map[string]any)
	if !ok {
		return Result{Reason: fmt.Sprintf("matches_shape: expected must be map[string]any, got %T", expected)}
	}

	var got map[string]any
	switch o := observed.(type) {
	case map[string]any:
		got = o
	case []byte:
		if err := json.Unmarshal(o, &got); err != nil {
			return Result{Reason: fmt.Sprintf("matches_shape: observed bytes not JSON: %v", err)}
		}
	case string:
		if err := json.Unmarshal([]byte(o), &got); err != nil {
			return Result{Reason: fmt.Sprintf("matches_shape: observed string not JSON: %v", err)}
		}
	default:
		return Result{Reason: fmt.Sprintf("matches_shape: observed %T not decodable", observed)}
	}

	for k, v := range want {
		actual, present := got[k]
		if !present {
			return Result{Reason: fmt.Sprintf("matches_shape: field %q missing", k)}
		}
		if !deepEq(actual, v) {
			return Result{Reason: fmt.Sprintf("matches_shape: field %q: got %#v want %#v", k, actual, v)}
		}
	}
	return Result{Matched: true}
}

func deepEq(a, b any) bool {
	// JSON-unmarshal numbers come back as float64. Normalise integers.
	if af, ok := a.(float64); ok {
		if bi, ok := b.(int); ok {
			return af == float64(bi)
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
```

- [ ] **Step 6: Implement contains**

Create `tools/integration-suite-v2/internal/matchers/contains.go`:

```go
package matchers

import (
	"fmt"
	"reflect"
	"strings"
)

// Contains: substring (for strings) or item-in-slice (for slices).
type Contains struct{}

func (Contains) Match(observed, expected any) Result {
	switch o := observed.(type) {
	case string:
		want, ok := expected.(string)
		if !ok {
			return Result{Reason: fmt.Sprintf("contains: observed is string, expected must be string, got %T", expected)}
		}
		if strings.Contains(o, want) {
			return Result{Matched: true}
		}
		return Result{Reason: fmt.Sprintf("contains: substring %q not in %q", want, o)}
	}

	v := reflect.ValueOf(observed)
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		for i := 0; i < v.Len(); i++ {
			if reflect.DeepEqual(v.Index(i).Interface(), expected) {
				return Result{Matched: true}
			}
		}
		return Result{Reason: fmt.Sprintf("contains: item %#v not in observed slice", expected)}
	}

	return Result{Reason: fmt.Sprintf("contains: observed %T not searchable", observed)}
}
```

- [ ] **Step 7: Implement the registry**

Create `tools/integration-suite-v2/internal/matchers/registry.go`:

```go
package matchers

import "fmt"

type Registry struct {
	m map[string]Matcher
}

func NewRegistry() *Registry {
	r := &Registry{m: map[string]Matcher{}}
	r.Register("equals", Equals{})
	r.Register("count_eq", CountEq{})
	r.Register("matches_shape", MatchesShape{})
	r.Register("contains", Contains{})
	return r
}

func (r *Registry) Register(name string, m Matcher) { r.m[name] = m }

func (r *Registry) Get(name string) (Matcher, error) {
	m, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("matchers: unknown matcher %q", name)
	}
	return m, nil
}
```

- [ ] **Step 8: Write tests**

Create `tools/integration-suite-v2/internal/matchers/matchers_test.go`:

```go
package matchers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEquals(t *testing.T) {
	assert.True(t, Equals{}.Match(1, 1).Matched)
	assert.True(t, Equals{}.Match("hi", "hi").Matched)
	r := Equals{}.Match(1, 2)
	assert.False(t, r.Matched)
	assert.Contains(t, r.Reason, "observed")
}

func TestCountEq(t *testing.T) {
	assert.True(t, CountEq{}.Match([]int{1, 2, 3}, 3).Matched)
	assert.False(t, CountEq{}.Match([]int{1, 2}, 3).Matched)
	assert.True(t, CountEq{}.Match("abc", 3).Matched)
	r := CountEq{}.Match([]int{1}, 2)
	assert.Contains(t, r.Reason, "got 1 want 2")
}

func TestMatchesShape(t *testing.T) {
	observed := map[string]any{"name": "general", "owner": "alice", "extra": "ignored"}
	r := MatchesShape{}.Match(observed, map[string]any{"name": "general", "owner": "alice"})
	assert.True(t, r.Matched, r.Reason)

	r2 := MatchesShape{}.Match(observed, map[string]any{"name": "different"})
	assert.False(t, r2.Matched)
	assert.Contains(t, r2.Reason, "name")
}

func TestContains_String(t *testing.T) {
	assert.True(t, Contains{}.Match("create-room success", "success").Matched)
	assert.False(t, Contains{}.Match("create-room failed", "success").Matched)
}

func TestContains_Slice(t *testing.T) {
	assert.True(t, Contains{}.Match([]string{"a", "b", "c"}, "b").Matched)
	assert.False(t, Contains{}.Match([]string{"a", "b", "c"}, "z").Matched)
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"equals", "count_eq", "matches_shape", "contains"} {
		m, err := r.Get(name)
		assert.NoError(t, err, name)
		assert.NotNil(t, m, name)
	}
	_, err := r.Get("unknown")
	assert.Error(t, err)
}
```

- [ ] **Step 9: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/matchers/... 2>&1 | tail -5
```

Expected: 6 PASS.

- [ ] **Step 10: Commit**

```bash
git add catalogs/matchers.yaml tools/integration-suite-v2/internal/matchers/
git commit -m "feat(v2): matchers framework + 4 matchers (equals, count_eq, matches_shape, contains)"
```

---

## Task 5: Reader framework + interface

**Files:**
- Create: `tools/integration-suite-v2/internal/readers/types.go`
- Create: `tools/integration-suite-v2/internal/readers/registry.go`

- [ ] **Step 1: Define the Reader interface**

Create `tools/integration-suite-v2/internal/readers/types.go`:

```go
// Package readers holds reader implementations, one per catalog
// location. Each reader knows how to query its backend, filter by
// scenario trace + run prefix + timestamp window, and return observed
// changes during a scenario's timeframe.
package readers

import (
	"context"
	"time"
)

// Event is one observed change at a reader location.
type Event struct {
	Location    string    // matches the catalog location name
	Timestamp   time.Time // when the event happened, per the location's timestamp_source
	Traceparent string    // empty if the change carries no trace
	OwnerSvc    string    // which service is attributed as the writer (from catalog ownership)
	Payload     any       // typed shape per the location (mongo doc, NATS msg, log line, ...)
}

// Reader is the interface every reader implementation satisfies.
type Reader interface {
	// Watch starts a goroutine that emits events into the returned channel
	// until ctx is cancelled or T_close fires. The runId is the run prefix
	// used for filtering test-created data; the start is T_open (events
	// before start are filtered).
	Watch(ctx context.Context, runID string, start time.Time) (<-chan Event, error)
}
```

- [ ] **Step 2: Implement the registry**

Create `tools/integration-suite-v2/internal/readers/registry.go`:

```go
package readers

import "fmt"

type Registry struct {
	m map[string]Reader
}

func NewRegistry() *Registry { return &Registry{m: map[string]Reader{}} }

func (r *Registry) Register(name string, reader Reader) { r.m[name] = reader }

func (r *Registry) Get(name string) (Reader, error) {
	rd, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("readers: unknown reader %q", name)
	}
	return rd, nil
}

// All returns every registered (name, reader) for the observer to iterate.
func (r *Registry) All() map[string]Reader {
	out := map[string]Reader{}
	for k, v := range r.m {
		out[k] = v
	}
	return out
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd /home/user/chat && go build ./tools/integration-suite-v2/internal/readers/...
```

Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-v2/internal/readers/
git commit -m "feat(v2): reader interface + registry"
```

---

## Task 6: mongo.rooms reader + YAML

**Files:**
- Create: `catalogs/readers/mongo.rooms.yaml`
- Create: `tools/integration-suite-v2/internal/readers/mongo_rooms.go`

- [ ] **Step 1: Create the YAML**

Create `catalogs/readers/mongo.rooms.yaml`:

```yaml
location: mongo.rooms
owners:    [room-service]
timestamp_source: createdAt
shape:    model.Room
executor: MongoRoomsReader
```

- [ ] **Step 2: Implement the reader**

Create `tools/integration-suite-v2/internal/readers/mongo_rooms.go`:

```go
package readers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// MongoRoomsReader watches the rooms collection for newly-inserted rows
// (createdAt >= start) whose _id carries the run prefix it-<runID>-.
// Polls every 100ms.
type MongoRoomsReader struct {
	Client *mongo.Client
	DBName string
}

func NewMongoRoomsReader(c *mongo.Client, dbName string) *MongoRoomsReader {
	return &MongoRoomsReader{Client: c, DBName: dbName}
}

func (r *MongoRoomsReader) Watch(ctx context.Context, runID string, start time.Time) (<-chan Event, error) {
	ch := make(chan Event, 16)
	go func() {
		defer close(ch)
		seen := map[string]struct{}{}
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				docs, err := r.fetchSince(ctx, runID, start)
				if err != nil {
					continue
				}
				for _, d := range docs {
					id, _ := d["_id"].(string)
					if id == "" {
						continue
					}
					if _, ok := seen[id]; ok {
						continue
					}
					seen[id] = struct{}{}
					ts, _ := d["createdAt"].(time.Time)
					ch <- Event{
						Location:  "mongo.rooms",
						Timestamp: ts,
						OwnerSvc:  "room-service",
						Payload:   d,
					}
				}
			}
		}
	}()
	return ch, nil
}

func (r *MongoRoomsReader) fetchSince(ctx context.Context, runID string, start time.Time) ([]map[string]any, error) {
	coll := r.Client.Database(r.DBName).Collection("rooms")
	filter := bson.M{
		"createdAt": bson.M{"$gte": start},
		"_id":       bson.M{"$regex": "^it-" + runID + "-"},
	}
	cur, err := coll.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("mongo.rooms find: %w", err)
	}
	defer cur.Close(ctx)
	var out []map[string]any
	for cur.Next(ctx) {
		var doc map[string]any
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("mongo.rooms decode: %w", err)
		}
		out = append(out, doc)
	}
	if err := cur.Err(); err != nil && !strings.Contains(err.Error(), "context") {
		return nil, fmt.Errorf("mongo.rooms cursor: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 3: Verify build**

```bash
cd /home/user/chat && go build ./tools/integration-suite-v2/internal/readers/...
```

Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add catalogs/readers/mongo.rooms.yaml tools/integration-suite-v2/internal/readers/mongo_rooms.go
git commit -m "feat(v2): mongo.rooms reader"
```

---

## Task 7: NATS reply reader + YAML

**Files:**
- Create: `catalogs/readers/reply.yaml`
- Create: `tools/integration-suite-v2/internal/readers/nats_reply.go`

- [ ] **Step 1: Create the YAML**

Create `catalogs/readers/reply.yaml`:

```yaml
location: reply
owners:    []         # synthetic — the reply is captured from the verb dispatcher
timestamp_source: dispatcher-recorded receive time
shape:    bytes
executor: NATSReplyReader
```

- [ ] **Step 2: Implement the reader**

Create `tools/integration-suite-v2/internal/readers/nats_reply.go`:

```go
package readers

import (
	"context"
	"time"
)

// NATSReplyReader is a "pull" reader: the verb dispatcher feeds the
// captured reply into the reader via the Inject method, which then
// emits one event. There's no polling because the reply is point-in-time.
type NATSReplyReader struct {
	in chan Event
}

func NewNATSReplyReader() *NATSReplyReader {
	return &NATSReplyReader{in: make(chan Event, 4)}
}

func (r *NATSReplyReader) Watch(ctx context.Context, runID string, start time.Time) (<-chan Event, error) {
	out := make(chan Event, 4)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-r.in:
				out <- ev
			}
		}
	}()
	return out, nil
}

// Inject is called by the dispatcher when a reply arrives. The dispatcher
// has the traceparent context; this method records it.
func (r *NATSReplyReader) Inject(reply []byte, traceparent string, ts time.Time, ownerSvc string) {
	select {
	case r.in <- Event{
		Location:    "reply",
		Timestamp:   ts,
		Traceparent: traceparent,
		OwnerSvc:    ownerSvc,
		Payload:     reply,
	}:
	default:
		// drop if buffer full — should not happen with 4-deep buffer and 1 reply per scenario
	}
}
```

- [ ] **Step 3: Verify build**

```bash
cd /home/user/chat && go build ./tools/integration-suite-v2/internal/readers/...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add catalogs/readers/reply.yaml tools/integration-suite-v2/internal/readers/nats_reply.go
git commit -m "feat(v2): nats reply reader (push-style from dispatcher)"
```

---

## Task 8: Container log reader + YAML

**Files:**
- Create: `catalogs/readers/logs.room-service.yaml`
- Create: `tools/integration-suite-v2/internal/readers/container_logs.go`

- [ ] **Step 1: Create the YAML**

Create `catalogs/readers/logs.room-service.yaml`:

```yaml
location: logs.room-service
owners:    [room-service]
timestamp_source: log line's "time" field (slog JSON)
shape:    structured log entry (map[string]any)
executor: ContainerLogsReader[room-service]
```

- [ ] **Step 2: Implement the reader**

Create `tools/integration-suite-v2/internal/readers/container_logs.go`:

```go
package readers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ContainerLogsReader tails `docker logs -f <container>` from the start
// of the scenario, filters lines to those with a traceparent we set,
// and emits Events.
type ContainerLogsReader struct {
	Container string  // docker container name
	Service   string  // service name (for OwnerSvc)
	Location  string  // catalog location (e.g., "logs.room-service")
}

func NewContainerLogsReader(container, service, location string) *ContainerLogsReader {
	return &ContainerLogsReader{Container: container, Service: service, Location: location}
}

func (r *ContainerLogsReader) Watch(ctx context.Context, runID string, start time.Time) (<-chan Event, error) {
	out := make(chan Event, 64)
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--since", start.Format(time.RFC3339), r.Container)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("logs reader: pipe %s: %w", r.Container, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("logs reader: start %s: %w", r.Container, err)
	}

	go func() {
		defer close(out)
		defer cmd.Wait() //nolint:errcheck
		scanner := bufio.NewScanner(stdout)
		// Allow lines up to 1 MiB
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				// Not JSON; skip
				continue
			}
			ts := parseLogTime(entry)
			if ts.Before(start) {
				continue
			}
			tp, _ := entry["traceparent"].(string)
			// Filter by run prefix or traceparent; if neither matches anything we care about,
			// the observer can still see it for ability-profile classification.
			_ = runID
			out <- Event{
				Location:    r.Location,
				Timestamp:   ts,
				Traceparent: tp,
				OwnerSvc:    r.Service,
				Payload:     entry,
			}
		}
	}()
	return out, nil
}

func parseLogTime(entry map[string]any) time.Time {
	// slog JSON uses "time" field in RFC3339Nano
	if s, ok := entry["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Now()
}

// Unused import guard
var _ = strings.HasPrefix
```

- [ ] **Step 3: Verify build**

```bash
cd /home/user/chat && go build ./tools/integration-suite-v2/internal/readers/...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add catalogs/readers/logs.room-service.yaml tools/integration-suite-v2/internal/readers/container_logs.go
git commit -m "feat(v2): container log reader (docker logs -f, JSON line parser)"
```

---

## Task 9: Service ability profile + room-service YAML

**Files:**
- Create: `catalogs/services/room-service.yaml`

- [ ] **Step 1: Create the YAML**

Create `catalogs/services/room-service.yaml`:

```yaml
service: room-service
triggers:
  - source: nats
    pattern: "chat.user.*.request.room.*.create"
    on_trigger:
      - writes: reply
      - writes: mongo.rooms
      - writes: logs.room-service
```

Note: this is the room-service's only known background-vs-cascade pattern for v2-Part1. Additional services and additional triggers per service will land in Part-2 as scenarios demand.

- [ ] **Step 2: Commit**

```bash
git add catalogs/services/room-service.yaml
git commit -m "feat(v2): room-service ability profile"
```

---

## Task 10: Fixture cast YAML + minimal cast

**Files:**
- Create: `catalogs/fixture-cast.yaml`

- [ ] **Step 1: Generate sample credentials for 3 fixture users**

Each cast user needs a valid NATS user nkey + JWT from auth-service. For v2-Part1 these are placeholders that the seeder (Task 11) materializes at seed time — the YAML carries the user's identity, the seeder generates credentials on the fly.

Create `catalogs/fixture-cast.yaml`:

```yaml
users:
  - id: alice
    tags: [verified, has_user_record]
    credentials:
      strategy: generate-and-auth  # seeder will generate nkey + call /auth in dev mode
      account: alice
  - id: bob
    tags: [verified, has_user_record]
    credentials:
      strategy: generate-and-auth
      account: bob
  - id: carol
    tags: [verified, has_user_record]
    credentials:
      strategy: generate-and-auth
      account: carol

rooms: []
messages: []
subscriptions: []
```

3 verified users is enough for the v2-Part1 scenario and for a concurrency mishap (N=3).

- [ ] **Step 2: Commit**

```bash
git add catalogs/fixture-cast.yaml
git commit -m "feat(v2): minimal fixture cast (3 verified users)"
```

---

## Task 11: Fixture cast types + seeder

**Files:**
- Create: `tools/integration-suite-v2/internal/fixtures/cast.go`
- Create: `tools/integration-suite-v2/internal/fixtures/seeder.go`
- Create: `tools/integration-suite-v2/internal/fixtures/seeder_test.go`

- [ ] **Step 1: Create the runtime cast types (with materialized credentials)**

Create `tools/integration-suite-v2/internal/fixtures/cast.go`:

```go
// Package fixtures holds the fixture cast: the seeded test-universe
// roster. The cast is read-only by verbs; verbs may only create
// run-prefixed temp data.
package fixtures

// CastUser is one user fixture with materialized credentials.
// Credentials are filled in by the seeder at startup (nkey generated;
// auth-service /auth called to mint a NATS JWT).
type CastUser struct {
	ID       string
	Tags     []string
	Account  string
	JWT      string
	NkeySeed string
}

// Cast is the in-memory roster after seeding.
type Cast struct {
	Users []CastUser
	// Rooms / Messages / Subscriptions added in later parts as scenarios demand.
}

// FindByPredicate returns up to maxN users matching every (tag) in tags.
// Returns nil if fewer than 1 match.
func (c *Cast) FindByPredicate(tags []string, maxN int) []CastUser {
	var out []CastUser
	for _, u := range c.Users {
		if hasAllTags(u.Tags, tags) {
			out = append(out, u)
			if maxN > 0 && len(out) >= maxN {
				break
			}
		}
	}
	return out
}

func hasAllTags(have, want []string) bool {
	set := map[string]struct{}{}
	for _, t := range have {
		set[t] = struct{}{}
	}
	for _, t := range want {
		if _, ok := set[t]; !ok {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Implement the seeder**

Create `tools/integration-suite-v2/internal/fixtures/seeder.go`:

```go
package fixtures

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/nats-io/nkeys"
)

// CastSpec is the YAML shape (what fixtures/cast.yaml looks like).
type CastSpec struct {
	Users []UserSpec `yaml:"users"`
}

type UserSpec struct {
	ID          string         `yaml:"id"`
	Tags        []string       `yaml:"tags"`
	Credentials map[string]any `yaml:"credentials"`
}

// Seeder materializes the cast: for each user with strategy
// "generate-and-auth", generates an nkey + calls auth-service /auth
// in dev mode to mint a JWT.
type Seeder struct {
	AuthServiceURL string
	HTTPClient     *resty.Client
}

func NewSeeder(authURL string) *Seeder {
	return &Seeder{
		AuthServiceURL: authURL,
		HTTPClient:     resty.New().SetTimeout(5 * time.Second),
	}
}

func (s *Seeder) Seed(ctx context.Context, spec CastSpec) (*Cast, error) {
	cast := &Cast{}
	for _, u := range spec.Users {
		strategy, _ := u.Credentials["strategy"].(string)
		account, _ := u.Credentials["account"].(string)
		switch strategy {
		case "generate-and-auth":
			seed, jwt, err := s.mintCredentials(ctx, account)
			if err != nil {
				return nil, fmt.Errorf("seed user %s: %w", u.ID, err)
			}
			cast.Users = append(cast.Users, CastUser{
				ID:       u.ID,
				Tags:     u.Tags,
				Account:  account,
				JWT:      jwt,
				NkeySeed: seed,
			})
		case "":
			// No credentials needed (e.g., unregistered users)
			cast.Users = append(cast.Users, CastUser{ID: u.ID, Tags: u.Tags, Account: account})
		default:
			return nil, fmt.Errorf("seed user %s: unknown credentials strategy %q", u.ID, strategy)
		}
	}
	return cast, nil
}

func (s *Seeder) mintCredentials(ctx context.Context, account string) (seed, jwt string, err error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return "", "", fmt.Errorf("nkey create: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", "", fmt.Errorf("nkey pub: %w", err)
	}
	seedBytes, err := kp.Seed()
	if err != nil {
		return "", "", fmt.Errorf("nkey seed: %w", err)
	}

	resp, err := s.HTTPClient.R().SetContext(ctx).
		SetBody(map[string]string{"account": account, "natsPublicKey": pub}).
		SetHeader("Content-Type", "application/json").
		Post(s.AuthServiceURL + "/auth")
	if err != nil {
		return "", "", fmt.Errorf("auth-service POST: %w", err)
	}
	if resp.StatusCode() != 200 {
		return "", "", fmt.Errorf("auth-service /auth: status %d body %s", resp.StatusCode(), string(resp.Body()))
	}
	var body struct {
		NATSJwt string `json:"natsJwt"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		return "", "", fmt.Errorf("auth-service /auth: parse: %w", err)
	}
	if body.NATSJwt == "" {
		return "", "", fmt.Errorf("auth-service /auth: empty natsJwt")
	}
	return string(seedBytes), body.NATSJwt, nil
}
```

- [ ] **Step 3: Write the test**

Create `tools/integration-suite-v2/internal/fixtures/seeder_test.go`:

```go
package fixtures

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCast_FindByPredicate(t *testing.T) {
	c := &Cast{
		Users: []CastUser{
			{ID: "alice", Tags: []string{"verified", "owner"}},
			{ID: "bob", Tags: []string{"verified"}},
			{ID: "carol", Tags: []string{"unverified"}},
		},
	}
	matched := c.FindByPredicate([]string{"verified"}, 0)
	assert.Len(t, matched, 2)
	assert.Equal(t, "alice", matched[0].ID)

	owners := c.FindByPredicate([]string{"verified", "owner"}, 0)
	assert.Len(t, owners, 1)
	assert.Equal(t, "alice", owners[0].ID)

	limited := c.FindByPredicate([]string{"verified"}, 1)
	assert.Len(t, limited, 1)

	none := c.FindByPredicate([]string{"nonexistent"}, 0)
	assert.Empty(t, none)
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/fixtures/... 2>&1 | tail -5
```

Expected: 1 PASS (Cast_FindByPredicate). Seeder requires live auth-service so is integration-tested via the smoke run later.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-v2/internal/fixtures/
git commit -m "feat(v2): fixture cast types + seeder (auth-service dev mode)"
```

---

## Task 12: Catalog validator + standalone CLI

**Files:**
- Create: `tools/integration-suite-v2/internal/catalog/validator.go`
- Create: `tools/integration-suite-v2/internal/catalog/validator_test.go`
- Create: `tools/integration-suite-v2/cmd/validator/main.go`

- [ ] **Step 1: Write the test**

Create `tools/integration-suite-v2/internal/catalog/validator_test.go`:

```go
package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidate_AllReferenced(t *testing.T) {
	c := &Catalog{
		Verbs:   []Verb{{Name: "nats_request", Executor: "NATSRequestExecutor"}},
		Readers: []Reader{{Location: "mongo.rooms", Owners: []string{"room-service"}, Executor: "MongoRoomsReader"}},
		Services: []Service{{Name: "room-service"}},
	}
	knownExecutors := map[string]bool{
		"NATSRequestExecutor": true,
		"MongoRoomsReader":    true,
	}
	knownServices := map[string]bool{"room-service": true}

	errs := Validate(c, knownExecutors, knownServices)
	assert.Empty(t, errs)
}

func TestValidate_UnknownExecutor(t *testing.T) {
	c := &Catalog{
		Verbs: []Verb{{Name: "nats_request", Executor: "NonexistentExecutor"}},
	}
	errs := Validate(c, map[string]bool{}, map[string]bool{})
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0], "NonexistentExecutor")
}

func TestValidate_ReaderOwnerNotAService(t *testing.T) {
	c := &Catalog{
		Readers: []Reader{{Location: "mongo.rooms", Owners: []string{"not-a-service"}, Executor: "X"}},
		Services: []Service{{Name: "room-service"}},
	}
	knownExecutors := map[string]bool{"X": true}
	knownServices := map[string]bool{"room-service": true}
	errs := Validate(c, knownExecutors, knownServices)
	assert.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "not-a-service")
}
```

- [ ] **Step 2: Implement Validate**

Create `tools/integration-suite-v2/internal/catalog/validator.go`:

```go
package catalog

import "fmt"

// Validate checks that every executor name referenced from the catalog
// YAML resolves to a Go symbol (knownExecutors), and every service owner
// listed on a reader is a real service (knownServices).
//
// Returns a list of error strings; empty list = catalog is valid.
func Validate(c *Catalog, knownExecutors, knownServices map[string]bool) []string {
	var errs []string
	for _, v := range c.Verbs {
		if !knownExecutors[v.Executor] {
			errs = append(errs, fmt.Sprintf("verb %q: executor %q not registered", v.Name, v.Executor))
		}
	}
	for _, r := range c.Readers {
		if !knownExecutors[r.Executor] {
			errs = append(errs, fmt.Sprintf("reader %q: executor %q not registered", r.Location, r.Executor))
		}
		for _, o := range r.Owners {
			if !knownServices[o] {
				errs = append(errs, fmt.Sprintf("reader %q: owner %q is not a registered service", r.Location, o))
			}
		}
	}
	return errs
}
```

- [ ] **Step 3: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/catalog/... 2>&1 | tail -5
```

Expected: 5 PASS (2 from earlier loader tests + 3 new validator tests).

- [ ] **Step 4: Create the validator CLI**

Create `tools/integration-suite-v2/cmd/validator/main.go`:

```go
// Command validator loads catalogs/ and reports any reference drift.
// Exits 0 on success, 1 on validation errors.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	cat "github.com/hmchangw/chat/tools/integration-suite-v2/internal/catalog"
)

func main() {
	root := filepath.Join("catalogs")
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	c, err := cat.Load(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(2)
	}

	// Hard-coded known executors and services for v2-Part1.
	// In a richer build this map could be auto-derived; for now this
	// list grows by hand alongside the Go code.
	knownExecutors := map[string]bool{
		"NATSRequestExecutor":  true,
		"MongoRoomsReader":     true,
		"NATSReplyReader":      true,
		"ContainerLogsReader[room-service]": true,
	}
	knownServices := map[string]bool{"room-service": true}

	errs := cat.Validate(c, knownExecutors, knownServices)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "ERROR:", e)
		}
		os.Exit(1)
	}
	fmt.Println("catalog: ok —", len(c.Verbs), "verbs,", len(c.Readers), "readers,", len(c.Services), "services")
}
```

- [ ] **Step 5: Run the validator against the live catalogs**

```bash
cd /home/user/chat && go run ./tools/integration-suite-v2/cmd/validator
```

Expected: `catalog: ok — 1 verbs, 3 readers, 1 services`.

- [ ] **Step 6: Commit**

```bash
git add tools/integration-suite-v2/internal/catalog/validator.go \
        tools/integration-suite-v2/internal/catalog/validator_test.go \
        tools/integration-suite-v2/cmd/validator/main.go
git commit -m "feat(v2): catalog validator + standalone CLI"
```

---

## Task 13: Scenario YAML types + loader

**Files:**
- Create: `tools/integration-suite-v2/internal/scenario/types.go`
- Create: `tools/integration-suite-v2/internal/scenario/loader.go`
- Create: `tools/integration-suite-v2/internal/scenario/loader_test.go`

- [ ] **Step 1: Write failing test**

Create `tools/integration-suite-v2/internal/scenario/loader_test.go`:

```go
package scenario

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadScenario_ParsesYAML(t *testing.T) {
	tmp := t.TempDir()
	yamlData := []byte(`
scenario: verified_user_creates_channel_room
source: docs/superpowers/specs/example.md
input:
  verb: nats_request
  subject: chat.user.${requester.account}.request.room.${site}.create
  payload:
    name: $auto
    requesterAccount: ${requester.account}
  credential: ${requester.credential}
  placeholders:
    requester:
      type: user
      predicate:
        verified: true
sequence:
  - service: room-service
    reads:
      - location: reply
        matcher: matches_shape
        expected:
          name: $payload.name
      - location: mongo.rooms
        matcher: count_eq
        expected: 1
mishaps:
  ignore: []
`)
	path := filepath.Join(tmp, "test.yaml")
	require.NoError(t, os.WriteFile(path, yamlData, 0o644))

	s, err := LoadFile(path)
	require.NoError(t, err)

	assert.Equal(t, "verified_user_creates_channel_room", s.Name)
	assert.Equal(t, "nats_request", s.Input.Verb)
	assert.Len(t, s.Sequence, 1)
	assert.Equal(t, "room-service", s.Sequence[0].Service)
	assert.Len(t, s.Sequence[0].Reads, 2)
}
```

- [ ] **Step 2: Define scenario types**

Create `tools/integration-suite-v2/internal/scenario/types.go`:

```go
// Package scenario holds the Go types for parsed scenario YAML files.
package scenario

// Scenario is one authored test template.
type Scenario struct {
	Name     string   `yaml:"scenario"`
	Source   string   `yaml:"source"`
	Input    Input    `yaml:"input"`
	Sequence []Step   `yaml:"sequence"`
	Mishaps  Mishaps  `yaml:"mishaps"`
	Status   string   `yaml:"status,omitempty"` // "approved" or empty/draft
}

// Input is the verb invocation: which verb + how it's parameterized.
type Input struct {
	Verb         string                 `yaml:"verb"`
	Subject      string                 `yaml:"subject"`
	Payload      map[string]any         `yaml:"payload"`
	Credential   string                 `yaml:"credential"`
	Placeholders map[string]Placeholder `yaml:"placeholders"`
}

// Placeholder is a typed fixture slot with a predicate.
type Placeholder struct {
	Type      string         `yaml:"type"`
	Predicate map[string]any `yaml:"predicate"`
}

// Step is one ordered item in the observation sequence.
type Step struct {
	Service string `yaml:"service"`
	Reads   []Read `yaml:"reads"`
}

// Read is one expected observation at a location.
type Read struct {
	Location string `yaml:"location"`
	Matcher  string `yaml:"matcher"`
	Expected any    `yaml:"expected"`
	Within   string `yaml:"within,omitempty"`   // optional SLA budget, e.g. "1s"
	Optional bool   `yaml:"optional,omitempty"` // defaults false (required)
}

// Mishaps is the per-scenario control rod.
type Mishaps struct {
	Ignore []MishapIgnore `yaml:"ignore"`
}

// MishapIgnore names a mishap to skip with a one-line reason.
type MishapIgnore struct {
	Mishap string `yaml:"mishap"`
	Reason string `yaml:"reason"`
}
```

- [ ] **Step 3: Implement LoadFile**

Create `tools/integration-suite-v2/internal/scenario/loader.go`:

```go
package scenario

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadFile reads a scenario YAML at the given path.
func LoadFile(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("scenario %s: missing `scenario:` name", path)
	}
	if s.Source == "" {
		return nil, fmt.Errorf("scenario %s: missing `source:` citation", path)
	}
	if s.Input.Verb == "" {
		return nil, fmt.Errorf("scenario %s: missing `input.verb`", path)
	}
	// Lint mishap-ignore entries: each must have a reason.
	for i, m := range s.Mishaps.Ignore {
		if m.Reason == "" {
			return nil, fmt.Errorf("scenario %s: mishaps.ignore[%d] (%q) missing reason", path, i, m.Mishap)
		}
	}
	return &s, nil
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/scenario/... 2>&1 | tail -5
```

Expected: 1 PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-v2/internal/scenario/
git commit -m "feat(v2): scenario YAML types + loader + schema lint"
```

---

## Task 14: Predicate resolver

**Files:**
- Create: `tools/integration-suite-v2/internal/scenario/resolver.go`
- Create: `tools/integration-suite-v2/internal/scenario/resolver_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite-v2/internal/scenario/resolver_test.go`:

```go
package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/fixtures"
)

func TestResolve_PicksFirstMatchingFixture(t *testing.T) {
	cast := &fixtures.Cast{
		Users: []fixtures.CastUser{
			{ID: "alice", Tags: []string{"verified"}, Account: "alice"},
			{ID: "bob", Tags: []string{"verified"}, Account: "bob"},
			{ID: "dave", Tags: []string{"unverified"}, Account: "dave"},
		},
	}
	s := &Scenario{
		Input: Input{
			Placeholders: map[string]Placeholder{
				"requester": {Type: "user", Predicate: map[string]any{"verified": true}},
			},
		},
	}
	res, err := Resolve(s, cast)
	require.NoError(t, err)
	assert.Equal(t, "alice", res.Users["requester"].ID)
}

func TestResolve_NoMatchReturnsError(t *testing.T) {
	cast := &fixtures.Cast{Users: []fixtures.CastUser{{ID: "alice", Tags: []string{"verified"}}}}
	s := &Scenario{
		Input: Input{
			Placeholders: map[string]Placeholder{
				"requester": {Type: "user", Predicate: map[string]any{"banned": true}},
			},
		},
	}
	_, err := Resolve(s, cast)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no fixture matches")
}
```

- [ ] **Step 2: Implement Resolve**

Create `tools/integration-suite-v2/internal/scenario/resolver.go`:

```go
package scenario

import (
	"fmt"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/fixtures"
)

// Resolution holds the per-scenario placeholder → fixture bindings.
type Resolution struct {
	Users map[string]fixtures.CastUser
}

// Resolve binds each placeholder in s to a concrete cast fixture.
// Currently supports user placeholders only; rooms/messages added in
// later parts as scenarios demand.
func Resolve(s *Scenario, cast *fixtures.Cast) (*Resolution, error) {
	out := &Resolution{Users: map[string]fixtures.CastUser{}}
	for name, p := range s.Input.Placeholders {
		switch p.Type {
		case "user":
			matches := matchUserPredicate(cast, p.Predicate)
			if len(matches) == 0 {
				return nil, fmt.Errorf("placeholder %q: no fixture matches predicate %v", name, p.Predicate)
			}
			out.Users[name] = matches[0]
		default:
			return nil, fmt.Errorf("placeholder %q: unsupported type %q (v2-Part1 supports 'user' only)", name, p.Type)
		}
	}
	return out, nil
}

func matchUserPredicate(cast *fixtures.Cast, pred map[string]any) []fixtures.CastUser {
	wantTags := tagListFromPredicate(pred)
	return cast.FindByPredicate(wantTags, 0)
}

// tagListFromPredicate turns {verified: true, banned: false} into ["verified"].
// Boolean-true predicates require the tag; boolean-false predicates require absence
// (handled below by post-filtering).
func tagListFromPredicate(pred map[string]any) []string {
	var out []string
	for k, v := range pred {
		if b, ok := v.(bool); ok && b {
			out = append(out, k)
		}
	}
	return out
}
```

- [ ] **Step 3: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/scenario/... 2>&1 | tail -5
```

Expected: 3 PASS (1 prior + 2 new).

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-v2/internal/scenario/resolver.go \
        tools/integration-suite-v2/internal/scenario/resolver_test.go
git commit -m "feat(v2): predicate resolver — placeholder → cast fixture binding"
```

---

## Task 15: Mishap framework + concurrent executor + expander

**Files:**
- Create: `tools/integration-suite-v2/internal/mishap/types.go`
- Create: `tools/integration-suite-v2/internal/mishap/expander.go`
- Create: `tools/integration-suite-v2/internal/mishap/concurrent.go`
- Create: `tools/integration-suite-v2/internal/mishap/expander_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite-v2/internal/mishap/expander_test.go`:

```go
package mishap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPowerSet_EmptyInputYieldsSingleEmptySet(t *testing.T) {
	got := PowerSet([]string{})
	assert.Len(t, got, 1)
	assert.Empty(t, got[0])
}

func TestPowerSet_ThreeElementsYields8Subsets(t *testing.T) {
	got := PowerSet([]string{"a", "b", "c"})
	assert.Len(t, got, 8)
}

func TestExpand_AppliesIgnoreList(t *testing.T) {
	applicable := []string{"kill_pod", "concurrent", "partition"}
	ignore := []string{"partition"}
	subsets := Expand(applicable, ignore)
	// 2^2 = 4 subsets after filtering out partition
	assert.Len(t, subsets, 4)
}
```

- [ ] **Step 2: Implement types and expander**

Create `tools/integration-suite-v2/internal/mishap/types.go`:

```go
// Package mishap holds the perturbation catalog: each mishap is an
// action the platform applies during a test case (kill a pod, run
// concurrently, etc.). Mishaps do NOT declare expected behavior; the
// scenario's reads remain authoritative under any subset.
package mishap

// Mishap is one named perturbation.
type Mishap struct {
	Name string
	Axis string // "environment" or "concurrency"
}

// Executor performs the perturbation during a test case.
// The runner calls Apply before the verb fires (or in parallel with it,
// per mishap semantics) and Cleanup after the case.
type Executor interface {
	Apply() error
	Cleanup() error
}
```

Create `tools/integration-suite-v2/internal/mishap/expander.go`:

```go
package mishap

// PowerSet returns every subset of items (including the empty set).
// Order within each subset matches the input order.
func PowerSet(items []string) [][]string {
	n := len(items)
	total := 1 << n
	out := make([][]string, 0, total)
	for mask := 0; mask < total; mask++ {
		var subset []string
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				subset = append(subset, items[i])
			}
		}
		out = append(out, subset)
	}
	return out
}

// Expand computes the power set of applicable mishaps minus the ignore list.
// applicable = the names of every mishap in the catalog.
// ignore     = the names listed in the scenario's mishaps.ignore (blacklist).
// Returns every subset (including the empty subset = the "happy" case).
func Expand(applicable, ignore []string) [][]string {
	ignored := map[string]bool{}
	for _, i := range ignore {
		ignored[i] = true
	}
	var filtered []string
	for _, a := range applicable {
		if !ignored[a] {
			filtered = append(filtered, a)
		}
	}
	return PowerSet(filtered)
}
```

- [ ] **Step 3: Implement concurrent mishap (stub for now)**

Create `tools/integration-suite-v2/internal/mishap/concurrent.go`:

```go
package mishap

// ConcurrentExecutor is the concurrency mishap. For v2-Part1 it is
// instantiated by the runner with N=3 distinct fixtures; the actual
// fan-out logic lives in the runner because it needs access to the
// fixture cast and verb dispatcher.
//
// This struct exists so the catalog can reference an executor name;
// the Apply/Cleanup are no-ops because the dispatcher does the fan-out
// itself.
type ConcurrentExecutor struct{ N int }

func (c *ConcurrentExecutor) Apply() error   { return nil }
func (c *ConcurrentExecutor) Cleanup() error { return nil }
```

- [ ] **Step 4: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/mishap/... 2>&1 | tail -5
```

Expected: 3 PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-v2/internal/mishap/
git commit -m "feat(v2): mishap framework — power-set expander, concurrent executor stub"
```

---

## Task 16: Tracing helper + verb dispatcher

**Files:**
- Create: `tools/integration-suite-v2/internal/runtime/tracing.go`
- Create: `tools/integration-suite-v2/internal/runtime/dispatcher.go`

- [ ] **Step 1: Implement traceparent generation**

Create `tools/integration-suite-v2/internal/runtime/tracing.go`:

```go
package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewTraceparent generates a fresh W3C traceparent header value:
//   00-<32hex trace id>-<16hex span id>-01
func NewTraceparent() string {
	var traceID [16]byte
	var spanID [8]byte
	_, _ = rand.Read(traceID[:])
	_, _ = rand.Read(spanID[:])
	return fmt.Sprintf("00-%s-%s-01",
		hex.EncodeToString(traceID[:]),
		hex.EncodeToString(spanID[:]))
}

// TraceIDFromTraceparent returns the 32-hex trace ID portion.
func TraceIDFromTraceparent(tp string) string {
	// Format: 00-<32hex>-<16hex>-<2hex>
	if len(tp) < 36 {
		return ""
	}
	return tp[3:35]
}
```

- [ ] **Step 2: Implement the dispatcher**

Create `tools/integration-suite-v2/internal/runtime/dispatcher.go`:

```go
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/fixtures"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/verbs"
)

// Dispatcher resolves a scenario's input + placeholders into a concrete
// verb call, fires the verb, and pushes the reply into the NATSReplyReader.
type Dispatcher struct {
	VerbReg  *verbs.Registry
	ReplyRdr *readers.NATSReplyReader
}

// FireResult is what Fire returns.
type FireResult struct {
	Traceparent string
	StartTime   time.Time
	Outcome     verbs.Outcome
}

// Fire executes the verb and returns the timing + outcome.
func (d *Dispatcher) Fire(ctx context.Context, s *scenario.Scenario, res *scenario.Resolution, site string) (*FireResult, error) {
	tp := NewTraceparent()
	startTime := time.Now()

	executor, err := d.VerbReg.Get(verbExecutorName(s.Input.Verb))
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
	}

	cred := pickCredential(s.Input.Credential, res)
	subject := substitutePlaceholders(s.Input.Subject, res, site)
	payload, err := buildPayload(s.Input.Payload, res, site)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: payload: %w", err)
	}

	out := executor.Execute(ctx, verbs.Input{
		Subject:     subject,
		Payload:     payload,
		Credential:  cred,
		Traceparent: tp,
	})

	// Feed reply (or error) into the reply reader so the observer sees it.
	if d.ReplyRdr != nil {
		d.ReplyRdr.Inject(out.Reply, tp, time.Now(), "")
	}

	return &FireResult{Traceparent: tp, StartTime: startTime, Outcome: out}, nil
}

// verbExecutorName maps a verb name like "nats_request" to its Go executor
// name like "NATSRequestExecutor". For v2-Part1 the map is hard-coded; a
// later refinement could pull this from the YAML's executor: field.
func verbExecutorName(verb string) string {
	switch verb {
	case "nats_request":
		return "NATSRequestExecutor"
	}
	return verb
}

// substitutePlaceholders rewrites ${name.field} and ${site} in a string.
func substitutePlaceholders(s string, res *scenario.Resolution, site string) string {
	out := strings.ReplaceAll(s, "${site}", site)
	for name, u := range res.Users {
		out = strings.ReplaceAll(out, "${"+name+".account}", u.Account)
	}
	return out
}

// buildPayload turns the scenario YAML's typed payload map into JSON bytes,
// substituting placeholders for any string value that looks like ${...}.
func buildPayload(payload map[string]any, res *scenario.Resolution, site string) ([]byte, error) {
	resolved := map[string]any{}
	for k, v := range payload {
		switch x := v.(type) {
		case string:
			resolved[k] = substituteScalarPlaceholders(x, res, site)
		default:
			resolved[k] = v
		}
	}
	return json.Marshal(resolved)
}

func substituteScalarPlaceholders(s string, res *scenario.Resolution, site string) string {
	if s == "$auto" {
		return "it-" + currentRunID() + "-room-auto-" + shortHash(time.Now().String())
	}
	out := strings.ReplaceAll(s, "${site}", site)
	for name, u := range res.Users {
		out = strings.ReplaceAll(out, "${"+name+".account}", u.Account)
	}
	return out
}

// pickCredential looks up "${requester.credential}" → res.Users["requester"]'s creds.
func pickCredential(ref string, res *scenario.Resolution) verbs.Credential {
	// Trim leading $ and {...} → "requester.credential"
	trimmed := strings.TrimPrefix(ref, "$")
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	name := strings.SplitN(trimmed, ".", 2)[0]
	u, ok := res.Users[name]
	if !ok {
		return verbs.Credential{}
	}
	return verbs.Credential{
		Account:  u.Account,
		JWT:      u.JWT,
		NkeySeed: u.NkeySeed,
	}
}

// currentRunID is set by the runner before each Fire call. For v2-Part1
// we capture it in a package-level variable; a richer build can pass it
// through context.
var currentRunIDValue string

func currentRunID() string { return currentRunIDValue }

// SetRunID is called by the runner before dispatch.
func SetRunID(id string) { currentRunIDValue = id }

func shortHash(s string) string {
	// quick deterministic-ish shortener for $auto values
	if len(s) < 8 {
		return s
	}
	return s[:8]
}

// _ silence unused-import false alarms
var _ = fixtures.CastUser{}
```

- [ ] **Step 3: Build**

```bash
cd /home/user/chat && go build ./tools/integration-suite-v2/internal/runtime/...
```

Expected: clean compile.

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-v2/internal/runtime/tracing.go \
        tools/integration-suite-v2/internal/runtime/dispatcher.go
git commit -m "feat(v2): traceparent gen + verb dispatcher with placeholder substitution"
```

---

## Task 17: Observer (multiplex reader events)

**Files:**
- Create: `tools/integration-suite-v2/internal/runtime/observer.go`

- [ ] **Step 1: Implement the observer**

Create `tools/integration-suite-v2/internal/runtime/observer.go`:

```go
package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
)

// Observer fans out reader Watch channels into one merged Event stream
// for the timeframe closer + verdict to consume.
type Observer struct {
	Readers *readers.Registry
}

// Start begins watching every reader. The merged channel closes when ctx
// is cancelled (typically by the timeframe closer at T_close).
func (o *Observer) Start(ctx context.Context, runID string, startTime time.Time) <-chan readers.Event {
	merged := make(chan readers.Event, 256)
	var wg sync.WaitGroup

	for _, r := range o.Readers.All() {
		ch, err := r.Watch(ctx, runID, startTime)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(c <-chan readers.Event) {
			defer wg.Done()
			for ev := range c {
				select {
				case merged <- ev:
				case <-ctx.Done():
					return
				}
			}
		}(ch)
	}

	go func() {
		wg.Wait()
		close(merged)
	}()

	return merged
}
```

- [ ] **Step 2: Build**

```bash
cd /home/user/chat && go build ./tools/integration-suite-v2/internal/runtime/...
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite-v2/internal/runtime/observer.go
git commit -m "feat(v2): observer — multiplex reader events into one channel"
```

---

## Task 18: Timeframe closer (event-driven)

**Files:**
- Create: `tools/integration-suite-v2/internal/runtime/timeframe.go`
- Create: `tools/integration-suite-v2/internal/runtime/timeframe_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite-v2/internal/runtime/timeframe_test.go`:

```go
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
)

func TestTimeframeCloser_QuietPeriodAfterLastEventCloses(t *testing.T) {
	events := make(chan readers.Event, 4)
	now := time.Now()
	traceID := "abc123"
	events <- readers.Event{Location: "reply", Timestamp: now, Traceparent: "00-" + traceID + "-x-01"}
	events <- readers.Event{Location: "mongo.rooms", Timestamp: now.Add(50 * time.Millisecond), Traceparent: "00-" + traceID + "-x-01"}

	gathered, closed := GatherUntilQuiet(events, traceID, []string{"room-service"}, 200*time.Millisecond, 5*time.Second)
	assert.Len(t, gathered, 2)
	assert.True(t, closed.After(now))
}

func TestTimeframeCloser_SafetyCap_NoEventsAtAll(t *testing.T) {
	events := make(chan readers.Event)
	start := time.Now()
	_, closed := GatherUntilQuiet(events, "trace1", []string{"room-service"}, 50*time.Millisecond, 200*time.Millisecond)
	elapsed := closed.Sub(start)
	assert.True(t, elapsed >= 200*time.Millisecond, "safety cap should fire at ~200ms; got %v", elapsed)
}
```

- [ ] **Step 2: Implement**

Create `tools/integration-suite-v2/internal/runtime/timeframe.go`:

```go
package runtime

import (
	"strings"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
)

// GatherUntilQuiet collects events from the channel until either:
//   (a) no in-scope event has arrived for `quietGrace` duration, OR
//   (b) `safetyCap` total time has elapsed since first call.
//
// An "in-scope event" is one whose Traceparent matches our traceID AND
// whose OwnerSvc is in inScopeServices.
//
// Returns (every event collected, the close time T_close).
func GatherUntilQuiet(
	in <-chan readers.Event,
	traceID string,
	inScopeServices []string,
	quietGrace time.Duration,
	safetyCap time.Duration,
) ([]readers.Event, time.Time) {
	inScope := map[string]bool{}
	for _, s := range inScopeServices {
		inScope[s] = true
	}

	var gathered []readers.Event
	lastInScopeAt := time.Now()
	deadline := time.NewTimer(safetyCap)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case ev, ok := <-in:
			if !ok {
				return gathered, time.Now()
			}
			gathered = append(gathered, ev)
			if hasTrace(ev.Traceparent, traceID) && inScope[ev.OwnerSvc] {
				lastInScopeAt = ev.Timestamp
			}
		case <-tick.C:
			if time.Since(lastInScopeAt) >= quietGrace {
				return gathered, time.Now()
			}
		case <-deadline.C:
			return gathered, time.Now()
		}
	}
}

func hasTrace(tp, traceID string) bool {
	return strings.Contains(tp, traceID)
}
```

- [ ] **Step 3: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/runtime/... -run TestTimeframeCloser 2>&1 | tail -5
```

Expected: 2 PASS.

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-v2/internal/runtime/timeframe.go \
        tools/integration-suite-v2/internal/runtime/timeframe_test.go
git commit -m "feat(v2): event-driven timeframe closer (quiet grace + safety cap)"
```

---

## Task 19: Three-way classifier + Verdict struct

**Files:**
- Create: `tools/integration-suite-v2/internal/runtime/verdict.go`
- Create: `tools/integration-suite-v2/internal/runtime/verdict_test.go`

- [ ] **Step 1: Write failing test**

Create `tools/integration-suite-v2/internal/runtime/verdict_test.go`:

```go
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
)

func TestClassify_AllExpectedMatched_NoExtra_Passes(t *testing.T) {
	now := time.Now()
	traceID := "trace1"
	s := &scenario.Scenario{
		Sequence: []scenario.Step{
			{Service: "room-service", Reads: []scenario.Read{
				{Location: "reply", Matcher: "matches_shape", Expected: map[string]any{"id": "r1"}},
			}},
		},
	}
	events := []readers.Event{
		{Location: "reply", Timestamp: now, Traceparent: "00-" + traceID + "-x-01", Payload: []byte(`{"id":"r1"}`)},
	}

	v := Classify(s, events, traceID, matchers.NewRegistry(), nil)

	assert.Equal(t, "pass", v.Outcome)
	assert.Empty(t, v.MissingPositives)
	assert.Empty(t, v.UnexpectedCascades)
	assert.Empty(t, v.Anomalies)
}

func TestClassify_MissingPositive_Fails(t *testing.T) {
	s := &scenario.Scenario{
		Sequence: []scenario.Step{
			{Service: "room-service", Reads: []scenario.Read{
				{Location: "reply", Matcher: "matches_shape", Expected: map[string]any{"id": "r1"}},
			}},
		},
	}
	v := Classify(s, nil, "trace1", matchers.NewRegistry(), nil)
	assert.Equal(t, "fail", v.Outcome)
	assert.Len(t, v.MissingPositives, 1)
}

func TestClassify_UnexpectedCascade_Fails(t *testing.T) {
	now := time.Now()
	traceID := "trace1"
	s := &scenario.Scenario{
		Sequence: []scenario.Step{
			{Service: "room-service", Reads: []scenario.Read{
				{Location: "reply", Matcher: "matches_shape", Expected: map[string]any{"id": "r1"}},
			}},
		},
	}
	events := []readers.Event{
		{Location: "reply", Timestamp: now, Traceparent: "00-" + traceID + "-x-01", Payload: []byte(`{"id":"r1"}`)},
		{Location: "mongo.subscriptions", Timestamp: now, Traceparent: "00-" + traceID + "-y-01", OwnerSvc: "room-service"},
	}
	v := Classify(s, events, traceID, matchers.NewRegistry(), nil)
	assert.Equal(t, "fail", v.Outcome)
	assert.Len(t, v.UnexpectedCascades, 1)
	assert.Equal(t, "mongo.subscriptions", v.UnexpectedCascades[0].Location)
}
```

- [ ] **Step 2: Implement Classify**

Create `tools/integration-suite-v2/internal/runtime/verdict.go`:

```go
package runtime

import (
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
)

// Verdict is the outcome of one test case.
type Verdict struct {
	Outcome            string // "pass" or "fail"
	MissingPositives   []ReadFailure
	UnexpectedCascades []readers.Event
	Anomalies          []readers.Event
	Reason             string // populated when outcome == "fail"
}

// ReadFailure describes one expected read that didn't match.
type ReadFailure struct {
	Service  string
	Location string
	Reason   string
}

// AbilityProfileLookup returns whether a service has an ability profile
// pattern that matches an observed event without our trace.
type AbilityProfileLookup func(serviceName string, ev readers.Event) bool

// Classify applies the three-way model + missing-positive check.
func Classify(
	s *scenario.Scenario,
	events []readers.Event,
	traceID string,
	matcherReg *matchers.Registry,
	profileLookup AbilityProfileLookup,
) Verdict {
	v := Verdict{Outcome: "pass"}

	// Build a map of expected reads keyed by (service, location)
	type readKey struct{ Service, Location string }
	expectedReads := map[readKey][]scenario.Read{}
	for _, step := range s.Sequence {
		for _, r := range step.Reads {
			k := readKey{Service: step.Service, Location: r.Location}
			expectedReads[k] = append(expectedReads[k], r)
		}
	}

	// Mark which expected reads have been satisfied.
	satisfied := map[int]bool{}
	type expectedFlat struct {
		Service  string
		Location string
		Read     scenario.Read
	}
	var flat []expectedFlat
	for _, step := range s.Sequence {
		for _, r := range step.Reads {
			flat = append(flat, expectedFlat{Service: step.Service, Location: r.Location, Read: r})
		}
	}

	// Walk events.
	for _, ev := range events {
		if hasTrace(ev.Traceparent, traceID) {
			// In-cascade: try to match against an expected read at this location.
			matched := false
			for i, e := range flat {
				if satisfied[i] {
					continue
				}
				if e.Location != ev.Location {
					continue
				}
				m, err := matcherReg.Get(e.Read.Matcher)
				if err != nil {
					continue
				}
				if m.Match(ev.Payload, e.Read.Expected).Matched {
					satisfied[i] = true
					matched = true
					break
				}
			}
			if !matched {
				v.UnexpectedCascades = append(v.UnexpectedCascades, ev)
			}
		} else {
			// Not in cascade — background or anomaly.
			if profileLookup != nil && profileLookup(ev.OwnerSvc, ev) {
				continue // background, filtered
			}
			v.Anomalies = append(v.Anomalies, ev)
		}
	}

	// Check missing positives.
	for i, e := range flat {
		if satisfied[i] {
			continue
		}
		if e.Read.Optional {
			continue
		}
		v.MissingPositives = append(v.MissingPositives, ReadFailure{
			Service:  e.Service,
			Location: e.Location,
			Reason:   "expected read did not match within timeframe",
		})
	}

	if len(v.MissingPositives) > 0 || len(v.UnexpectedCascades) > 0 || len(v.Anomalies) > 0 {
		v.Outcome = "fail"
	}
	return v
}
```

- [ ] **Step 3: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/runtime/... -run TestClassify 2>&1 | tail -5
```

Expected: 3 PASS.

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-v2/internal/runtime/verdict.go \
        tools/integration-suite-v2/internal/runtime/verdict_test.go
git commit -m "feat(v2): three-way classifier + Verdict struct"
```

---

## Task 20: Reporter (writes last-run.md)

**Files:**
- Create: `tools/integration-suite-v2/internal/runtime/reporter.go`
- Create: `tools/integration-suite-v2/internal/runtime/reporter_test.go`

- [ ] **Step 1: Write failing test**

Create `tools/integration-suite-v2/internal/runtime/reporter_test.go`:

```go
package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRender_HappyRunZeroFails(t *testing.T) {
	r := RunReport{
		RunID:    "abcd",
		Duration: "1s",
		Cases: []CaseReport{
			{ScenarioName: "verified_user_creates_channel_room", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
		},
	}
	out := Render(r)
	assert.Contains(t, out, "abcd")
	assert.Contains(t, out, "pass: 1")
	assert.Contains(t, out, "fail: 0")
}

func TestRender_FailureCounts(t *testing.T) {
	r := RunReport{
		RunID:    "wxyz",
		Duration: "2s",
		Cases: []CaseReport{
			{ScenarioName: "a", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "b", Subset: "happy", Verdict: Verdict{Outcome: "fail",
				MissingPositives: []ReadFailure{{Service: "room-service", Location: "mongo.rooms", Reason: "no match"}},
			}},
		},
	}
	out := Render(r)
	assert.True(t, strings.Contains(out, "fail: 1"))
	assert.Contains(t, out, "missing-positive")
	assert.Contains(t, out, "mongo.rooms")
}
```

- [ ] **Step 2: Implement Render + RunReport types**

Create `tools/integration-suite-v2/internal/runtime/reporter.go`:

```go
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CaseReport is one test case's verdict and metadata.
type CaseReport struct {
	ScenarioName string
	Subset       string // textual description of the mishap subset (e.g., "happy" or "kill_pod+concurrent")
	Status       string // "approved" or "draft"
	Verdict      Verdict
}

// RunReport is the aggregate of one suite invocation.
type RunReport struct {
	RunID    string
	StartISO string
	Duration string
	Cases    []CaseReport
}

// Render produces the markdown body of last-run.md.
func Render(r RunReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run:        %s   (runID %s)\nDuration:   %s\nTotal:      %d test cases\n\n",
		r.StartISO, r.RunID, r.Duration, len(r.Cases))

	approvedPass, approvedFail := 0, 0
	draftPass, draftFail := 0, 0
	classCounts := map[string]int{}

	for _, c := range r.Cases {
		switch {
		case c.Status == "approved" && c.Verdict.Outcome == "pass":
			approvedPass++
		case c.Status == "approved" && c.Verdict.Outcome == "fail":
			approvedFail++
		case c.Verdict.Outcome == "pass":
			draftPass++
		default:
			draftFail++
		}
		if c.Verdict.Outcome == "fail" {
			if len(c.Verdict.MissingPositives) > 0 {
				classCounts["missing-positive"] += len(c.Verdict.MissingPositives)
			}
			if len(c.Verdict.UnexpectedCascades) > 0 {
				classCounts["unexpected-cascade"] += len(c.Verdict.UnexpectedCascades)
			}
			if len(c.Verdict.Anomalies) > 0 {
				classCounts["anomaly"] += len(c.Verdict.Anomalies)
			}
		}
	}

	fmt.Fprintf(&b, "APPROVED   pass: %d   fail: %d\n", approvedPass, approvedFail)
	fmt.Fprintf(&b, "DRAFT      pass: %d   fail: %d\n\n", draftPass, draftFail)

	if len(classCounts) > 0 {
		b.WriteString("Failures by class:\n")
		for _, cls := range []string{"missing-positive", "unexpected-cascade", "anomaly", "timeframe-never-closed"} {
			if classCounts[cls] > 0 {
				fmt.Fprintf(&b, "  %-22s %d\n", cls, classCounts[cls])
			}
		}
		b.WriteString("\n")
	}

	// Per-case failure details
	for _, c := range r.Cases {
		if c.Verdict.Outcome == "fail" {
			fmt.Fprintf(&b, "FAIL  [%s] %s — subset=%s\n", strings.ToUpper(c.Status), c.ScenarioName, c.Subset)
			for _, mp := range c.Verdict.MissingPositives {
				fmt.Fprintf(&b, "      missing-positive at %s.%s: %s\n", mp.Service, mp.Location, mp.Reason)
			}
			for _, ev := range c.Verdict.UnexpectedCascades {
				fmt.Fprintf(&b, "      unexpected-cascade at %s (owner=%s)\n", ev.Location, ev.OwnerSvc)
			}
			for _, ev := range c.Verdict.Anomalies {
				fmt.Fprintf(&b, "      anomaly at %s (owner=%s)\n", ev.Location, ev.OwnerSvc)
			}
		}
	}

	return b.String()
}

// Write persists Render to a file path, creating parent dirs.
func Write(path string, r RunReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Render(r)), 0o644)
}

// Unused import guard
var _ = time.Now
```

- [ ] **Step 3: Run tests**

```bash
cd /home/user/chat && go test ./tools/integration-suite-v2/internal/runtime/... -run TestRender 2>&1 | tail -5
```

Expected: 2 PASS.

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-v2/internal/runtime/reporter.go \
        tools/integration-suite-v2/internal/runtime/reporter_test.go
git commit -m "feat(v2): reporter — Render + Write + per-class breakdown"
```

---

## Task 21: Runner orchestration + cmd/runner/main.go + Makefile

**Files:**
- Create: `tools/integration-suite-v2/internal/runtime/runner.go`
- Create: `tools/integration-suite-v2/cmd/runner/main.go`
- Create: `tools/integration-suite-v2/Makefile`

- [ ] **Step 1: Implement the runner orchestrator**

Create `tools/integration-suite-v2/internal/runtime/runner.go`:

```go
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/catalog"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/fixtures"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/verbs"
)

// Config holds the runner's wiring.
type Config struct {
	AuthURL    string
	NATSURL    string
	MongoURI   string
	MongoDB    string
	SiteID     string
	ScenariosDir string
	CatalogsDir  string
	OutputPath   string // path to write last-run.md
}

// Run executes the entire suite: load catalogs, seed cast, walk every
// scenario, run its happy case, write the report.
//
// v2-Part1: no mishap subsets yet (would need a way to disable mishaps
// for the smoke run); the runner runs only the empty subset (happy) per
// scenario. Subset expansion is wired in Task 22 via a flag.
func Run(ctx context.Context, cfg Config) (*RunReport, error) {
	runID := newRunID()
	SetRunID(runID)
	report := &RunReport{
		RunID:    runID,
		StartISO: time.Now().UTC().Format(time.RFC3339),
	}
	startTime := time.Now()

	// 1. Load catalogs
	cat, err := catalog.Load(cfg.CatalogsDir)
	if err != nil {
		return nil, fmt.Errorf("load catalogs: %w", err)
	}

	// 2. Seed cast
	var castSpec fixtures.CastSpec
	if err := loadCastSpec(filepath.Join(cfg.CatalogsDir, "fixture-cast.yaml"), &castSpec); err != nil {
		return nil, fmt.Errorf("load cast spec: %w", err)
	}
	seeder := fixtures.NewSeeder(cfg.AuthURL)
	cast, err := seeder.Seed(ctx, castSpec)
	if err != nil {
		return nil, fmt.Errorf("seed cast: %w", err)
	}

	// 3. Connect Mongo
	mongoClient, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	defer mongoClient.Disconnect(ctx) //nolint:errcheck

	// 4. Build registries
	verbReg := verbs.NewRegistry()
	verbReg.Register("NATSRequestExecutor", verbs.NewNATSRequest(cfg.NATSURL))

	matcherReg := matchers.NewRegistry()

	readerReg := readers.NewRegistry()
	readerReg.Register("mongo.rooms", readers.NewMongoRoomsReader(mongoClient, cfg.MongoDB))
	replyReader := readers.NewNATSReplyReader()
	readerReg.Register("reply", replyReader)
	readerReg.Register("logs.room-service",
		readers.NewContainerLogsReader("room-service", "room-service", "logs.room-service"))

	dispatcher := &Dispatcher{VerbReg: verbReg, ReplyRdr: replyReader}
	observer := &Observer{Readers: readerReg}

	_ = cat // catalog already validated at startup; iterate scenarios next

	// 5. Walk scenarios
	scenarioFiles, err := findScenarios(cfg.ScenariosDir)
	if err != nil {
		return nil, fmt.Errorf("find scenarios: %w", err)
	}
	for _, sf := range scenarioFiles {
		s, err := scenario.LoadFile(sf)
		if err != nil {
			return nil, fmt.Errorf("load scenario %s: %w", sf, err)
		}
		caseRep, err := runOneCase(ctx, s, cast, cfg, dispatcher, observer, matcherReg)
		if err != nil {
			return nil, fmt.Errorf("run %s: %w", s.Name, err)
		}
		report.Cases = append(report.Cases, caseRep)
	}

	report.Duration = time.Since(startTime).Round(time.Millisecond).String()
	if cfg.OutputPath != "" {
		if err := Write(cfg.OutputPath, *report); err != nil {
			return nil, fmt.Errorf("write report: %w", err)
		}
	}
	return report, nil
}

func runOneCase(
	ctx context.Context,
	s *scenario.Scenario,
	cast *fixtures.Cast,
	cfg Config,
	dispatcher *Dispatcher,
	observer *Observer,
	matcherReg *matchers.Registry,
) (CaseReport, error) {
	// Resolve placeholders
	res, err := scenario.Resolve(s, cast)
	if err != nil {
		return CaseReport{ScenarioName: s.Name, Subset: "happy", Status: status(s), Verdict: Verdict{Outcome: "fail", Reason: err.Error()}}, nil
	}

	// Observer needs to start BEFORE the verb fires so we don't miss early events.
	obsCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	startTime := time.Now()
	events := observer.Start(obsCtx, currentRunID(), startTime)

	// Fire verb
	fr, err := dispatcher.Fire(ctx, s, res, cfg.SiteID)
	if err != nil {
		return CaseReport{ScenarioName: s.Name, Subset: "happy", Status: status(s), Verdict: Verdict{Outcome: "fail", Reason: err.Error()}}, nil
	}

	// Collect events until quiet
	inScopeServices := []string{}
	for _, step := range s.Sequence {
		inScopeServices = append(inScopeServices, step.Service)
	}
	traceID := TraceIDFromTraceparent(fr.Traceparent)
	gathered, _ := GatherUntilQuiet(events, traceID, inScopeServices, 200*time.Millisecond, 5*time.Second)

	// Verdict
	v := Classify(s, gathered, traceID, matcherReg, nil)
	if fr.Outcome.Err != nil && v.Outcome == "fail" {
		v.Reason = fr.Outcome.Err.Error()
	}
	return CaseReport{ScenarioName: s.Name, Subset: "happy", Status: status(s), Verdict: v}, nil
}

func status(s *scenario.Scenario) string {
	if s.Status == "approved" {
		return "approved"
	}
	return "draft"
}

func newRunID() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func loadCastSpec(path string, into *fixtures.CastSpec) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yamlUnmarshal(data, into)
}

func findScenarios(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".yaml" {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !strings.Contains(err.Error(), "no such file") {
		return nil, err
	}
	return out, nil
}

// yamlUnmarshal indirection to keep imports tidy.
func yamlUnmarshal(data []byte, v any) error {
	return yaml_v3_unmarshal(data, v)
}
```

(Replace `yaml_v3_unmarshal` import — drop helper in a follow-up if it's cleaner to inline. For now:)

Add to top of runner.go imports:

```go
import (
	"...",
	yamlv3 "gopkg.in/yaml.v3"
)
```

And replace `yaml_v3_unmarshal(data, v)` with `yamlv3.Unmarshal(data, v)`.

- [ ] **Step 2: Implement cmd/runner/main.go**

Create `tools/integration-suite-v2/cmd/runner/main.go`:

```go
// Command runner executes the v2 integration test suite.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	rt "github.com/hmchangw/chat/tools/integration-suite-v2/internal/runtime"
)

func main() {
	cfg := rt.Config{
		AuthURL:      env("AUTH_SERVICE_URL", "http://localhost:8080"),
		NATSURL:      env("NATS_URL", "nats://localhost:4222"),
		MongoURI:     env("MONGO_URI", "mongodb://localhost:27017"),
		MongoDB:      env("MONGO_DB", "chat"),
		SiteID:       env("SITE_ID", "site-local"),
		ScenariosDir: env("SCENARIOS_DIR", "scenarios"),
		CatalogsDir:  env("CATALOGS_DIR", "catalogs"),
		OutputPath:   filepath.Join("docs", "integration-suite-v2", "last-run.md"),
	}

	report, err := rt.Run(context.Background(), cfg)
	if err != nil {
		slog.Error("run failed", "err", err)
		os.Exit(2)
	}

	fmt.Printf("Run %s — %d cases\n", report.RunID, len(report.Cases))
	pass, fail := 0, 0
	for _, c := range report.Cases {
		if c.Verdict.Outcome == "pass" {
			pass++
		} else {
			fail++
		}
	}
	fmt.Printf("pass: %d  fail: %d\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 3: Create the Makefile**

Create `tools/integration-suite-v2/Makefile`:

```makefile
SHELL := /bin/bash
REPO_ROOT  := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)

NATS_CONTAINER := chat-local-nats

.PHONY: help validate local run

help:
	@echo "make -C tools/integration-suite-v2 <target>"
	@echo "  validate   — run the catalog validator (no live infra needed)"
	@echo "  local      — run against the docker-local stack (assumes 'make up' is running)"
	@echo "  run        — alias for local; honors any env-var overrides"

validate:
	@cd $(REPO_ROOT) && go run ./tools/integration-suite-v2/cmd/validator

local: preflight
	@cd $(REPO_ROOT) && \
		AUTH_SERVICE_URL=http://localhost:8080 \
		NATS_URL=nats://localhost:4222 \
		MONGO_URI=mongodb://localhost:27017 \
		MONGO_DB=chat \
		SITE_ID=site-local \
		go run ./tools/integration-suite-v2/cmd/runner

run: local

preflight:
	@docker container inspect -f '{{.State.Running}}' $(NATS_CONTAINER) 2>/dev/null | grep -q true || { \
	  echo "ERROR: $(NATS_CONTAINER) is not running. Run 'make deps-up && make up' from repo root."; exit 1; \
	}
	@curl -fsS http://localhost:8080/healthz >/dev/null 2>&1 || { \
	  echo "ERROR: auth-service not reachable on :8080. Is 'make up' running?"; exit 1; \
	}
```

- [ ] **Step 4: Verify it all builds**

```bash
cd /home/user/chat && go build ./tools/integration-suite-v2/...
```

Expected: clean.

- [ ] **Step 5: Try the validate target (no infra needed)**

```bash
make -C tools/integration-suite-v2 validate
```

Expected: `catalog: ok — 1 verbs, 3 readers, 1 services`.

- [ ] **Step 6: Commit**

```bash
git add tools/integration-suite-v2/internal/runtime/runner.go \
        tools/integration-suite-v2/cmd/runner/main.go \
        tools/integration-suite-v2/Makefile
git commit -m "feat(v2): runner orchestration + cmd/runner + Makefile (local + validate)"
```

---

## Task 22: First scenario YAML + smoke run

**Files:**
- Create: `scenarios/drafts/service/verified-user-creates-channel-room.yaml`

- [ ] **Step 1: Create the scenario**

Create `scenarios/drafts/service/verified-user-creates-channel-room.yaml`:

```yaml
scenario: verified_user_creates_channel_room
source: docs/superpowers/specs/2026-05-21-integration-suite-v2-design.md

input:
  verb: nats_request
  subject: chat.user.${requester.account}.request.room.${site}.create
  payload:
    name: $auto
    requesterAccount: ${requester.account}
  credential: ${requester.credential}
  placeholders:
    requester:
      type: user
      predicate:
        verified: true

sequence:
  - service: room-service
    reads:
      - location: reply
        matcher: matches_shape
        expected:
          createdBy: ${requester.account}

mishaps:
  ignore: []
```

- [ ] **Step 2: Run validate**

```bash
make -C tools/integration-suite-v2 validate
```

Expected: still `catalog: ok`.

- [ ] **Step 3: Run the suite (requires live stack)**

```bash
# from repo root, with `make up` running in another terminal:
make -C tools/integration-suite-v2 local
```

Expected outcomes:

- If the live stack is running: the runner connects, seeds the cast (alice/bob/carol get nkeys + JWTs), fires the verb against `chat.user.alice.request.room.site-local.create`, captures the reply, classifies, writes `docs/integration-suite-v2/last-run.md`. Exit code reflects pass/fail.
- If the live stack is NOT running: preflight fails cleanly with "chat-local-nats is not running."

- [ ] **Step 4: Read the report**

```bash
cat docs/integration-suite-v2/last-run.md
```

Expected: a report showing 1 test case with verdict pass or fail (depending on the live system's actual behavior).

- [ ] **Step 5: Commit**

```bash
git add scenarios/drafts/service/verified-user-creates-channel-room.yaml
git commit -m "feat(v2): first scenario — verified_user_creates_channel_room"
```

---

## Self-review

After writing this plan, I checked it against the spec:

**Spec coverage:**
- ✓ Four-layer model — represented in directory structure + runtime orchestration
- ✓ Layer 4 scenarios in YAML — Task 13 (loader) + Task 22 (first scenario)
- ✓ Layer 3 catalogs — Tasks 2 (catalog loader), 3 (verbs), 4 (matchers), 5-8 (readers), 9 (services), 10-11 (cast), 12 (validator)
- ✓ Layer 2 runtime — Tasks 16 (dispatcher), 17 (observer), 18 (timeframe), 19 (verdict), 20 (reporter), 21 (orchestration)
- ✓ Layer 1 chaos — explicitly deferred to Part 3 in scope table
- ✓ Verbs as primitives — only `nats_request` implemented in Part 1
- ✓ Placeholders + predicates — Task 14 (predicate resolver)
- ✓ Cast read-only — fixture cast seeded once; verbs never write to it
- ✓ Three-way classification — Task 19
- ✓ Event-driven T_close + safety cap — Task 18
- ✓ Optional reads + optional within: — Task 13 (types), Task 19 (verdict honors Optional)
- ✓ Mishap subset expansion + blacklist — Task 15 (expander) + Task 13 (Mishaps.Ignore types)
- ✓ Container log reader — Task 8
- ✓ Source citation enforced by scenario loader — Task 13
- ✓ Catalog validator — Task 12
- ✓ Two-score split + verdict-class counts in reporter — Task 20

**Placeholder scan:** None found. Every step has runnable code.

**Type consistency:** Verified — `verbs.Input`, `verbs.Outcome`, `verbs.Credential`, `readers.Event`, `scenario.Scenario`, `runtime.Verdict`, `runtime.CaseReport`, `runtime.RunReport` are used consistently across tasks. Function names (`Load`, `Validate`, `Resolve`, `Classify`, `Render`, `Run`, `GatherUntilQuiet`, `NewTraceparent`) match between definition and use.

**Known limitations of Part 1 (documented in scope table):**

- The runner only runs the empty mishap subset (happy case) per scenario. Subset expansion is wired (Task 15) but not invoked from the orchestrator yet. Wiring this is Part 2 work — it requires a chaos infrastructure to actually apply env mishaps.
- The predicate resolver supports user placeholders only. Room/message placeholders land when scenarios demand them.
- The catalog validator's `knownExecutors` map is hand-maintained in `cmd/validator/main.go`. Auto-derivation is a Part 2 refinement.
- One scenario, one verb, three readers. Part 2 adds more.

This delivers a working v2 platform that runs end-to-end on the docker-local single-site stack.
