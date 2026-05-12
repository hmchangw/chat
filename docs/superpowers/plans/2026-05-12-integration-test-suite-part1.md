# Integration Test Suite — Part 1: Framework — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the godog-based integration test suite framework at `tools/integration-suite/` with the full scoring/audit/status/blindspot machinery, and prove it works end-to-end with one passing scenario against `room-service`'s HTTP API.

**Architecture:** A new tool directory invoked via `go test` from Makefile targets. godog drives Gherkin scenarios from `features/`; step definitions live in `*_test.go` files in the same package. Config comes from per-site env vars. Hooks tag every scenario with status (`@status:approved` vs default-draft) and blindspot (`@blindspot:<slug>`) handling. After the suite runs, a post-processor reads cucumber JSON and writes a Markdown summary that splits APPROVED and DRAFT scores side by side and tallies failures by error class.

**Tech Stack:** Go 1.25, [godog](https://github.com/cucumber/godog) v0.14+, [Resty](https://github.com/go-resty/resty) v2 (HTTP), `github.com/caarlos0/env/v11`, `github.com/stretchr/testify`. Reuses `pkg/idgen` for ID generation. No new dependencies beyond godog.

**Spec:** `docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`

---

## Revision — 2026-05-12 (after Task 10)

**Architectural finding during Task 10 execution:** the only service in this repo with HTTP routes is `auth-service`. Every other service — `room-service`, `room-worker`, `history-service`, `search-service`, all workers — is **NATS-only** (handlers registered as `natsCreateRoom(m otelnats.Msg)` etc.). The original plan's Tasks 11–14 incorrectly assumed `room-service` had HTTP endpoints.

**Resolution (per user directive):** The integration suite is a **platform**, not a service-specific harness. The framework provides primitives for whatever transport a service speaks; per-scenario steps choose the right primitive. So:

- **Tasks 1–10** (config, world, fixtures, status, classifier, tracing, HTTP helper, godog wiring, blindspot hook) stay as-is — all transport-agnostic.
- **Task 11** is reworked: auth flow is HTTP because that's how `auth-service` speaks. Mints a NATS JWT via `POST /auth` after generating an nkey keypair locally.
- **New Task 11b** adds NATS primitives to the platform: per-user NATS connection, NATS request/reply with traceparent in headers, unified `LastResponse` extension.
- **Task 12** is reworked: room steps use NATS request/reply on subjects like `chat.user.<account>.room.create` — because that's how `room-service` speaks.
- **Task 13** is generalized: `Then the response is a <Class> error` dispatches by transport via `LastResponse.Class()`. Adds `ClassifyNATS`.
- **Task 14** smoke scenario uses HTTP auth + NATS room ops — exactly what the architecture says.

Tasks 15+ are unaffected. Detailed reworked specs follow inline in this document.

---

## Scope of THIS plan (Part 1)

| Area | In | Out (Part 2) |
|---|---|---|
| Directory + Go wiring | ✓ | |
| Config (env per site) | ✓ (auth, room) | other services |
| World, fixtures, run-prefix IDs | ✓ | |
| Status tag parsing | ✓ | |
| Error classifier (HTTP only) | ✓ | NATS error classification |
| Two-score reporter (markdown + cucumber JSON + JUnit XML) | ✓ | |
| Blindspot hook | ✓ | |
| Tracing helpers (traceparent) | ✓ | |
| HTTP step primitives | ✓ | |
| `auth_steps_test.go` (real JWT mint) | ✓ | |
| `room_steps_test.go` (room HTTP verbs) | ✓ | |
| `error_steps_test.go` (error class assertions) | ✓ | |
| `service/room.feature` smoke scenarios | ✓ (2 scenarios) | |
| Audit checklist + tally commands | ✓ | |
| `integration-suite-steps` command | ✓ | |
| `integration-suite-lint` command | ✓ | |
| README + AUTHORING skeleton | ✓ | |
| Makefile targets | ✓ | `integration-suite-purge` |
| NATS / JetStream / Mongo / Cassandra step files | | ✓ |
| `pipeline/`, `federation/`, `resilience/`, `regional-resilience/` features | | ✓ |
| Chaos integration (chaos-mesh + docker compose) | | ✓ |
| Data purge target | | ✓ |

---

## Background — context the executor needs

Read once before starting. Later tasks reference these.

- **Monorepo layout:** single `go.mod` at the repo root, services are flat `package main` directories at repo root (`room-service/`, `auth-service/`, ...). Shared code in `pkg/`. The integration suite goes at `tools/integration-suite/` matching `tools/loadgen/` and `tools/nats-debug/`. See `CLAUDE.md` §1.
- **Coding rules (CLAUDE.md §3):** wrap errors with `fmt.Errorf("...: %w", err)`. Use `log/slog` (JSON format) for logging. Use Resty for HTTP, not net/http directly. Use `caarlos0/env` for config parsing. Use `pkg/idgen` for IDs.
- **godog basics:** scenarios live in `.feature` files. Step definitions are Go functions registered in `ScenarioInitializer`. Hooks (`Before`, `After`) run at the scenario level. Output formats are configured via `Options.Format` as a comma-separated list (e.g., `"pretty,cucumber:reports/cucumber.json,junit:reports/junit.xml"`).
- **`pkg/idgen` exports** (verify by `grep -n '^func ' pkg/idgen/*.go`):
  - `GenerateUUIDv7() string` — 32-char hex, time-ordered
  - `GenerateID() string` — 17-char base62
  - `GenerateRequestID() string` — 36-char hyphenated UUIDv7
- **`auth-service` interface:** verify by reading `auth-service/handler.go` and `auth-service/routes.go`. Plan assumes there's an HTTP endpoint that mints a JWT for a given account (`POST /token` or similar). If the endpoint shape differs, adjust Task 13 accordingly.
- **`room-service` interface:** verify by reading `room-service/handler.go` and `room-service/routes.go`. Plan assumes HTTP endpoints for creating a channel room and reading a room by ID. Verify before Task 14.
- **W3C Trace Context:** `traceparent` header format is `00-<32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags>`. We generate it ourselves; we don't import an OTel SDK for v1.

---

## File structure (decomposition)

All files live at `tools/integration-suite/`. Package name: `integrationsuite`.

| File | Responsibility |
|---|---|
| `config.go` | Env-var parsing into typed Config (per-site URL maps) |
| `world.go` | World struct: run ID, scenario ID, captured trace IDs, HTTP/NATS clients, last response |
| `fixtures.go` | ID generation with `it-<runID>-<scenarioID>-<entity>` prefix |
| `status.go` | Status tag parsing (approved vs draft) |
| `classifier.go` | 8-class error enum + HTTP response → class mapping |
| `tracing.go` | `traceparent` generation + capture |
| `reporter.go` | Post-processes `reports/cucumber.json` → `docs/integration-suite/last-run.md` with two-score split |
| `audit.go` | Audit checklist generator + tally helpers |
| `main_test.go` | godog `TestFeatures` entry, `TestSuiteInitializer`, `ScenarioInitializer`, post-run reporter call |
| `auth_steps_test.go` | Auth step defs (JWT minting) |
| `room_steps_test.go` | Room HTTP step defs |
| `error_steps_test.go` | Error class assertion step defs |
| `http_helpers_test.go` | Resty client factory + trace-propagating wrapper (test-only) |
| `features/service/room.feature` | Smoke scenarios |
| `cmd/audit/main.go` | `make integration-suite-audit` driver |
| `cmd/audit-tally/main.go` | `make integration-suite-audit-tally` driver |
| `cmd/steps/main.go` | `make integration-suite-steps` driver (list registered steps) |
| `cmd/lint/main.go` | `make integration-suite-lint` driver (blindspot consistency) |
| `README.md` | User flow |
| `AUTHORING.md` | AI + human authoring playbook |

Files outside `tools/integration-suite/`:

| File | Responsibility |
|---|---|
| `Makefile` (root, modify) | Add `integration-suite*` targets |
| `docs/integration-suite/blindspots.md` (create) | Living register, initially empty header only |
| `docs/integration-suite/audit-log.md` (create) | Audit history, initially empty header only |
| `docs/integration-suite/.gitignore` (create) | Ignore `last-run.md`, `audit-*.md` working files |

---

## Task 1: Scaffold directory and prove `go test` runs

**Files:**
- Create: `tools/integration-suite/main_test.go`
- Create: `tools/integration-suite/doc.go`

- [ ] **Step 1: Create the directory and a package doc file**

```bash
mkdir -p tools/integration-suite/features/service
mkdir -p tools/integration-suite/cmd
mkdir -p docs/integration-suite
```

Create `tools/integration-suite/doc.go`:

```go
// Package integrationsuite is a scenario-driven black-box integration
// test suite for the chat backend. See README.md and AUTHORING.md.
package integrationsuite
```

- [ ] **Step 2: Add godog to go.mod**

Run from repo root:

```bash
go get github.com/cucumber/godog@latest
```

Expected: `go.mod` updated with godog dependency.

- [ ] **Step 3: Write a minimal godog runner in `main_test.go`**

Create `tools/integration-suite/main_test.go`:

```go
package integrationsuite

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
)

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		Name:                 "integration-suite",
		ScenarioInitializer:  InitializeScenario,
		TestSuiteInitializer: InitializeTestSuite,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}
	if status := suite.Run(); status != 0 {
		os.Exit(status)
	}
}

// InitializeTestSuite runs once before any scenario.
func InitializeTestSuite(ctx *godog.TestSuiteContext) {}

// InitializeScenario runs once per scenario.
func InitializeScenario(ctx *godog.ScenarioContext) {}
```

- [ ] **Step 4: Run the test to prove it boots with zero features**

```bash
cd tools/integration-suite && go test -v -run TestFeatures
```

Expected: PASS. Output shows `0 scenarios`, `0 steps`. No error.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/ go.mod go.sum
git commit -m "feat(integration-suite): scaffold godog test runner"
```

---

## Task 2: Config — env-var parsing with per-site URLs

**Files:**
- Create: `tools/integration-suite/config.go`
- Create: `tools/integration-suite/config_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/config_test.go`:

```go
package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_RequiresPrimarySite(t *testing.T) {
	t.Setenv("SITES", "tw")
	// PRIMARY_SITE intentionally unset

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PRIMARY_SITE")
}

func TestLoadConfig_TwoSitesParsed(t *testing.T) {
	t.Setenv("SITES", "tw,us")
	t.Setenv("PRIMARY_SITE", "tw")
	t.Setenv("AUTH_SERVICE_URL_TW", "http://auth-tw:8080")
	t.Setenv("ROOM_SERVICE_URL_TW", "http://room-tw:8080")
	t.Setenv("AUTH_SERVICE_URL_US", "http://auth-us:8080")
	t.Setenv("ROOM_SERVICE_URL_US", "http://room-us:8080")

	cfg, err := LoadConfig()
	require.NoError(t, err)

	assert.Equal(t, []string{"tw", "us"}, cfg.Sites)
	assert.Equal(t, "tw", cfg.PrimarySite)
	assert.Equal(t, "http://auth-tw:8080", cfg.AuthServiceURL("tw"))
	assert.Equal(t, "http://room-us:8080", cfg.RoomServiceURL("us"))
}

func TestLoadConfig_MissingServiceURLForSite(t *testing.T) {
	t.Setenv("SITES", "tw")
	t.Setenv("PRIMARY_SITE", "tw")
	// AUTH_SERVICE_URL_TW intentionally unset

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUTH_SERVICE_URL_TW")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run TestLoadConfig
```

Expected: FAIL — `LoadConfig` undefined.

- [ ] **Step 3: Implement `config.go`**

Create `tools/integration-suite/config.go`:

```go
package integrationsuite

import (
	"fmt"
	"os"
	"strings"
)

// Config holds suite configuration loaded from env vars.
// Per-site URLs use uppercase-site-id suffixes: AUTH_SERVICE_URL_TW.
type Config struct {
	Sites       []string
	PrimarySite string

	authURLs map[string]string
	roomURLs map[string]string
}

// LoadConfig parses env vars and validates that every site has the
// service URLs the v1 suite requires (auth + room).
func LoadConfig() (*Config, error) {
	sitesRaw := strings.TrimSpace(os.Getenv("SITES"))
	if sitesRaw == "" {
		return nil, fmt.Errorf("config: SITES is required (comma-separated list)")
	}
	sites := strings.Split(sitesRaw, ",")
	for i, s := range sites {
		sites[i] = strings.TrimSpace(s)
	}

	primary := strings.TrimSpace(os.Getenv("PRIMARY_SITE"))
	if primary == "" {
		return nil, fmt.Errorf("config: PRIMARY_SITE is required")
	}

	cfg := &Config{
		Sites:       sites,
		PrimarySite: primary,
		authURLs:    map[string]string{},
		roomURLs:    map[string]string{},
	}

	for _, site := range sites {
		up := strings.ToUpper(site)

		auth := os.Getenv("AUTH_SERVICE_URL_" + up)
		if auth == "" {
			return nil, fmt.Errorf("config: AUTH_SERVICE_URL_%s is required", up)
		}
		cfg.authURLs[site] = auth

		room := os.Getenv("ROOM_SERVICE_URL_" + up)
		if room == "" {
			return nil, fmt.Errorf("config: ROOM_SERVICE_URL_%s is required", up)
		}
		cfg.roomURLs[site] = room
	}

	return cfg, nil
}

// AuthServiceURL returns the auth-service URL for the given site.
// Panics on unknown site — caller error, not a runtime condition.
func (c *Config) AuthServiceURL(site string) string {
	u, ok := c.authURLs[site]
	if !ok {
		panic(fmt.Sprintf("config: unknown site %q", site))
	}
	return u
}

// RoomServiceURL returns the room-service URL for the given site.
func (c *Config) RoomServiceURL(site string) string {
	u, ok := c.roomURLs[site]
	if !ok {
		panic(fmt.Sprintf("config: unknown site %q", site))
	}
	return u
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run TestLoadConfig
```

Expected: all three tests PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/config.go tools/integration-suite/config_test.go
git commit -m "feat(integration-suite): config — env-var parsing with per-site URLs"
```

---

## Task 3: Run-prefix ID generation

**Files:**
- Create: `tools/integration-suite/fixtures.go`
- Create: `tools/integration-suite/fixtures_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/fixtures_test.go`:

```go
package integrationsuite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewIDPrefixer_FormatIsItRunScenarioEntity(t *testing.T) {
	p := NewIDPrefixer("7a2c", "room-create-01")

	got := p.ID("alice")
	assert.Equal(t, "it-7a2c-room-create-01-alice", got)
}

func TestNewIDPrefixer_DifferentScenariosDoNotCollide(t *testing.T) {
	p1 := NewIDPrefixer("7a2c", "scenario-a")
	p2 := NewIDPrefixer("7a2c", "scenario-b")

	assert.NotEqual(t, p1.ID("alice"), p2.ID("alice"))
}

func TestGenerateRunID_Is4HexChars(t *testing.T) {
	id := GenerateRunID()
	assert.Len(t, id, 4)
	for _, r := range id {
		assert.True(t, (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'),
			"non-hex char in run ID: %q", string(r))
	}
}

func TestGenerateRunID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := GenerateRunID()
		if seen[id] {
			// Collisions in 16^4 = 65536 are possible; just ensure not all collide.
			continue
		}
		seen[id] = true
	}
	assert.Greater(t, len(seen), 900, "expected near-unique run IDs over 1000 generations")
}

func TestScenarioIDFromName_KebabCasedFromGherkinName(t *testing.T) {
	cases := map[string]string{
		"Adding a member persists subscription": "adding-a-member-persists-subscription",
		"DM room is deterministic":              "dm-room-is-deterministic",
		"Outline: row 3":                        "outline-row-3",
	}
	for input, want := range cases {
		got := ScenarioIDFromName(input)
		assert.Equal(t, want, got, "input: %q", input)
		assert.False(t, strings.Contains(got, " "), "must not contain spaces")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd tools/integration-suite && go test -v -run "TestNewIDPrefixer|TestGenerateRunID|TestScenarioIDFromName"
```

Expected: FAIL — types/functions undefined.

- [ ] **Step 3: Implement `fixtures.go`**

Create `tools/integration-suite/fixtures.go`:

```go
package integrationsuite

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// IDPrefixer generates scenario-scoped IDs that carry a stable prefix:
//   it-<runID>-<scenarioID>-<entity>
// All scenario data uses these so it never collides with real data
// and can be cleaned up by `make integration-suite-purge`.
type IDPrefixer struct {
	runID      string
	scenarioID string
}

// NewIDPrefixer creates a prefixer for one scenario in one run.
func NewIDPrefixer(runID, scenarioID string) *IDPrefixer {
	return &IDPrefixer{runID: runID, scenarioID: scenarioID}
}

// ID returns "it-<runID>-<scenarioID>-<entity>".
func (p *IDPrefixer) ID(entity string) string {
	return fmt.Sprintf("it-%s-%s-%s", p.runID, p.scenarioID, entity)
}

// GenerateRunID returns 4 hex chars. Collision-tolerant: 16^4 = 65,536
// distinct values is sufficient for human-distinguishable run labels.
func GenerateRunID() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("fixtures: GenerateRunID: %v", err))
	}
	return hex.EncodeToString(b[:])
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// ScenarioIDFromName converts a Gherkin scenario name to a kebab-case slug.
// "Adding a Member" → "adding-a-member".
func ScenarioIDFromName(name string) string {
	lower := strings.ToLower(name)
	dashed := nonAlnum.ReplaceAllString(lower, "-")
	return strings.Trim(dashed, "-")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run "TestNewIDPrefixer|TestGenerateRunID|TestScenarioIDFromName"
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/fixtures.go tools/integration-suite/fixtures_test.go
git commit -m "feat(integration-suite): fixtures — run-prefix ID generation"
```

---

## Task 4: World struct (scenario-scoped state)

**Files:**
- Create: `tools/integration-suite/world.go`
- Create: `tools/integration-suite/world_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/world_test.go`:

```go
package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorld_PrefixForScenario_GeneratesUniquePrefixerPerScenario(t *testing.T) {
	w := NewWorld("7a2c")

	w.BeginScenario("Adding a member")
	p1 := w.Prefix()
	assert.NotNil(t, p1)
	assert.Equal(t, "it-7a2c-adding-a-member-alice", p1.ID("alice"))

	w.BeginScenario("Removing a member")
	p2 := w.Prefix()
	require.NotNil(t, p2)
	assert.NotEqual(t, p1.ID("alice"), p2.ID("alice"))
}

func TestWorld_StoresLastResponse(t *testing.T) {
	w := NewWorld("7a2c")
	w.BeginScenario("Scenario A")

	w.SetLastResponse(&LastResponse{
		StatusCode: 404,
		Body:       []byte(`{"code":"ROOM_NOT_FOUND"}`),
		TraceID:    "abc123",
	})

	got := w.LastResponse()
	require.NotNil(t, got)
	assert.Equal(t, 404, got.StatusCode)
	assert.Equal(t, "abc123", got.TraceID)
}

func TestWorld_ResponseClearsBetweenScenarios(t *testing.T) {
	w := NewWorld("7a2c")
	w.BeginScenario("Scenario A")
	w.SetLastResponse(&LastResponse{StatusCode: 200})

	w.BeginScenario("Scenario B")
	assert.Nil(t, w.LastResponse(), "last response must reset between scenarios")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run TestWorld
```

Expected: FAIL — `World`, `LastResponse` undefined.

- [ ] **Step 3: Implement `world.go`**

Create `tools/integration-suite/world.go`:

```go
package integrationsuite

// LastResponse captures the most recent HTTP response in a scenario,
// so a `Then` step can assert on status, body, and trace.
type LastResponse struct {
	StatusCode int
	Body       []byte
	TraceID    string
}

// World is the per-scenario shared state passed to step definitions.
// One World is created per `go test` invocation; BeginScenario resets
// scenario-scoped fields.
type World struct {
	runID string

	scenarioName string
	prefix       *IDPrefixer
	lastResponse *LastResponse
}

// NewWorld creates a world for a single suite invocation.
func NewWorld(runID string) *World {
	return &World{runID: runID}
}

// BeginScenario resets per-scenario state and installs a new IDPrefixer.
func (w *World) BeginScenario(name string) {
	w.scenarioName = name
	w.prefix = NewIDPrefixer(w.runID, ScenarioIDFromName(name))
	w.lastResponse = nil
}

// Prefix returns the IDPrefixer for the current scenario.
func (w *World) Prefix() *IDPrefixer { return w.prefix }

// RunID returns the run-level prefix.
func (w *World) RunID() string { return w.runID }

// ScenarioName returns the Gherkin name of the current scenario.
func (w *World) ScenarioName() string { return w.scenarioName }

// SetLastResponse records the most recent HTTP response.
func (w *World) SetLastResponse(r *LastResponse) { w.lastResponse = r }

// LastResponse returns the most recent HTTP response, or nil.
func (w *World) LastResponse() *LastResponse { return w.lastResponse }
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run TestWorld
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/world.go tools/integration-suite/world_test.go
git commit -m "feat(integration-suite): world — scenario-scoped shared state"
```

---

## Task 5: Status tag parsing

**Files:**
- Create: `tools/integration-suite/status.go`
- Create: `tools/integration-suite/status_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/status_test.go`:

```go
package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusFromTags_DefaultIsDraft(t *testing.T) {
	assert.Equal(t, StatusDraft, StatusFromTags([]string{"@smoke"}))
	assert.Equal(t, StatusDraft, StatusFromTags([]string{}))
}

func TestStatusFromTags_ApprovedTagWins(t *testing.T) {
	assert.Equal(t, StatusApproved, StatusFromTags([]string{"@smoke", "@status:approved"}))
}

func TestStatusFromTags_UnknownStatusTagIgnored(t *testing.T) {
	// Defensive: unknown @status:xxx falls back to draft, no panic.
	assert.Equal(t, StatusDraft, StatusFromTags([]string{"@status:weirdvalue"}))
}

func TestBlindspotsFromTags(t *testing.T) {
	tags := []string{"@smoke", "@blindspot:partial-history", "@blindspot:another-thing"}
	got := BlindspotsFromTags(tags)
	assert.Equal(t, []string{"partial-history", "another-thing"}, got)
}

func TestBlindspotsFromTags_NoBlindspot(t *testing.T) {
	got := BlindspotsFromTags([]string{"@smoke"})
	assert.Empty(t, got)
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run "TestStatus|TestBlindspots"
```

Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement `status.go`**

Create `tools/integration-suite/status.go`:

```go
package integrationsuite

import "strings"

// Status is the review state of a scenario.
type Status string

const (
	// StatusDraft is the default for scenarios without @status:approved.
	// Their pass/fail does not count toward the authoritative score.
	StatusDraft Status = "draft"

	// StatusApproved marks scenarios the team has accepted as system
	// contracts. They count toward the authoritative score and gate CI.
	StatusApproved Status = "approved"
)

const (
	tagStatusApproved = "@status:approved"
	tagBlindspotPfx   = "@blindspot:"
)

// StatusFromTags returns the scenario status from its Gherkin tags.
// Default is StatusDraft.
func StatusFromTags(tags []string) Status {
	for _, t := range tags {
		if t == tagStatusApproved {
			return StatusApproved
		}
	}
	return StatusDraft
}

// BlindspotsFromTags returns the list of blindspot slugs declared on
// the scenario. Empty if none.
func BlindspotsFromTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if strings.HasPrefix(t, tagBlindspotPfx) {
			out = append(out, strings.TrimPrefix(t, tagBlindspotPfx))
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run "TestStatus|TestBlindspots"
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/status.go tools/integration-suite/status_test.go
git commit -m "feat(integration-suite): status — @status:approved and @blindspot: tag parsing"
```

---

## Task 6: Error classifier (HTTP)

**Files:**
- Create: `tools/integration-suite/classifier.go`
- Create: `tools/integration-suite/classifier_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/classifier_test.go`:

```go
package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyHTTP_2xxIsNoError(t *testing.T) {
	assert.Equal(t, ClassNone, ClassifyHTTP(200, nil))
	assert.Equal(t, ClassNone, ClassifyHTTP(204, nil))
}

func TestClassifyHTTP_401And403AreAuth(t *testing.T) {
	assert.Equal(t, ClassAuth, ClassifyHTTP(401, nil))
	assert.Equal(t, ClassAuth, ClassifyHTTP(403, nil))
}

func TestClassifyHTTP_400IsValidation(t *testing.T) {
	assert.Equal(t, ClassValidation, ClassifyHTTP(400, nil))
}

func TestClassifyHTTP_404Or409IsHandlerError(t *testing.T) {
	assert.Equal(t, ClassHandlerError, ClassifyHTTP(404, nil))
	assert.Equal(t, ClassHandlerError, ClassifyHTTP(409, nil))
}

func TestClassifyHTTP_5xxIsDownstreamOrPersistence(t *testing.T) {
	// Default 5xx is Downstream. Body-shape upgrade to Persistence is tested separately.
	assert.Equal(t, ClassDownstream, ClassifyHTTP(500, nil))
	assert.Equal(t, ClassDownstream, ClassifyHTTP(502, nil))
}

func TestClassifyHTTP_5xxWithPersistenceBodyIsPersistence(t *testing.T) {
	body := []byte(`{"code":"DB_WRITE_FAILED","error":"mongo write conflict"}`)
	assert.Equal(t, ClassPersistence, ClassifyHTTP(500, body))
}

func TestClassifyHTTP_TimeoutHintInBodyIsTimeout(t *testing.T) {
	body := []byte(`{"code":"REQUEST_TIMEOUT"}`)
	assert.Equal(t, ClassTimeout, ClassifyHTTP(504, body))
}

func TestClassifyHTTP_UnknownIsUnclassified(t *testing.T) {
	assert.Equal(t, ClassUnclassified, ClassifyHTTP(418, nil))
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run TestClassifyHTTP
```

Expected: FAIL — classifier undefined.

- [ ] **Step 3: Implement `classifier.go`**

Create `tools/integration-suite/classifier.go`:

```go
package integrationsuite

import (
	"bytes"
	"strings"
)

// Class is one of the 8 error categories used to break down failures.
// Source: spec §"Traceability and error classification".
type Class string

const (
	ClassNone           Class = "None"
	ClassRouteNotFound  Class = "RouteNotFound"
	ClassValidation     Class = "Validation"
	ClassAuth           Class = "Auth"
	ClassHandlerError   Class = "HandlerError"
	ClassTimeout        Class = "Timeout"
	ClassUnreachable    Class = "Unreachable"
	ClassPersistence    Class = "Persistence"
	ClassDownstream     Class = "Downstream"
	ClassUnclassified   Class = "Unclassified"
)

// ClassifyHTTP returns the Class for an HTTP response.
// `body` may be nil; classification falls back to status-code-only rules.
func ClassifyHTTP(statusCode int, body []byte) Class {
	if statusCode >= 200 && statusCode < 300 {
		return ClassNone
	}

	if statusCode == 401 || statusCode == 403 {
		return ClassAuth
	}
	if statusCode == 400 {
		return ClassValidation
	}
	if statusCode == 404 || statusCode == 409 || statusCode == 422 {
		return ClassHandlerError
	}

	if statusCode >= 500 && statusCode < 600 {
		// 5xx: inspect body for hints to narrow the class.
		if statusCode == 504 || bodyContainsCode(body, "REQUEST_TIMEOUT", "TIMEOUT") {
			return ClassTimeout
		}
		if bodyContainsCode(body, "DB_", "MONGO_", "CASSANDRA_") {
			return ClassPersistence
		}
		return ClassDownstream
	}

	return ClassUnclassified
}

// bodyContainsCode returns true if any of the substrings appears
// inside a JSON "code" field in the body. Case-insensitive contains —
// good enough for v1, since "code" values are uppercase by convention.
func bodyContainsCode(body []byte, needles ...string) bool {
	if len(body) == 0 {
		return false
	}
	upper := bytes.ToUpper(body)
	for _, n := range needles {
		if bytes.Contains(upper, []byte(strings.ToUpper(n))) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run TestClassifyHTTP
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/classifier.go tools/integration-suite/classifier_test.go
git commit -m "feat(integration-suite): classifier — HTTP response to 8-class error enum"
```

---

## Task 7: Tracing helpers

**Files:**
- Create: `tools/integration-suite/tracing.go`
- Create: `tools/integration-suite/tracing_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/tracing_test.go`:

```go
package integrationsuite

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTraceparent_FormatMatchesW3C(t *testing.T) {
	tp := NewTraceparent()
	// Format: 00-<32hex>-<16hex>-<2hex>
	re := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)
	assert.True(t, re.MatchString(tp), "traceparent %q does not match format", tp)
}

func TestTraceIDFromTraceparent(t *testing.T) {
	tp := "00-4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7-1234567890abcdef-01"
	id, err := TraceIDFromTraceparent(tp)
	require.NoError(t, err)
	assert.Equal(t, "4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7", id)
}

func TestTraceIDFromTraceparent_Malformed(t *testing.T) {
	_, err := TraceIDFromTraceparent("garbage")
	assert.Error(t, err)
}

func TestNewTraceparent_UniqueAcrossCalls(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		seen[NewTraceparent()] = true
	}
	assert.Len(t, seen, 100, "expected 100 unique traceparents")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run "TestNewTraceparent|TestTraceIDFromTraceparent"
```

Expected: FAIL — `NewTraceparent`/`TraceIDFromTraceparent` undefined.

- [ ] **Step 3: Implement `tracing.go`**

Create `tools/integration-suite/tracing.go`:

```go
package integrationsuite

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// TraceparentHeader is the W3C Trace Context header name.
const TraceparentHeader = "traceparent"

// NewTraceparent generates a fresh W3C traceparent header value:
//   00-<32hex trace id>-<16hex span id>-01
// We do not depend on an OTel SDK in v1; this is enough for the
// receiving services to include the trace ID in their spans.
func NewTraceparent() string {
	var traceID [16]byte
	var spanID [8]byte
	if _, err := rand.Read(traceID[:]); err != nil {
		panic(fmt.Sprintf("tracing: NewTraceparent: %v", err))
	}
	if _, err := rand.Read(spanID[:]); err != nil {
		panic(fmt.Sprintf("tracing: NewTraceparent: %v", err))
	}
	return fmt.Sprintf("00-%s-%s-01",
		hex.EncodeToString(traceID[:]),
		hex.EncodeToString(spanID[:]))
}

// TraceIDFromTraceparent extracts the 32-hex trace ID from a W3C
// traceparent header value.
func TraceIDFromTraceparent(tp string) (string, error) {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return "", fmt.Errorf("tracing: traceparent has %d parts, expected 4", len(parts))
	}
	if len(parts[1]) != 32 {
		return "", fmt.Errorf("tracing: trace ID has length %d, expected 32", len(parts[1]))
	}
	return parts[1], nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run "TestNewTraceparent|TestTraceIDFromTraceparent"
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/tracing.go tools/integration-suite/tracing_test.go
git commit -m "feat(integration-suite): tracing — W3C traceparent generation and extraction"
```

---

## Task 8: HTTP client helper (test-only)

**Files:**
- Create: `tools/integration-suite/http_helpers_test.go`

- [ ] **Step 1: Add resty to go.mod**

```bash
go get github.com/go-resty/resty/v2@latest
```

- [ ] **Step 2: Create `http_helpers_test.go`**

This is a test-only helper (`_test.go`) so it doesn't ship in production builds. It centralizes Resty client creation with traceparent propagation.

Create `tools/integration-suite/http_helpers_test.go`:

```go
package integrationsuite

import (
	"github.com/go-resty/resty/v2"
)

// newHTTPClient returns a Resty client with a 10s timeout and a
// traceparent generator that runs on every request, recording the
// generated value into world.
func newHTTPClient(w *World) *resty.Client {
	c := resty.New().
		SetTimeout(httpTimeout).
		SetHeader("Accept", "application/json")

	c.OnBeforeRequest(func(_ *resty.Client, r *resty.Request) error {
		tp := NewTraceparent()
		r.SetHeader(TraceparentHeader, tp)
		// Stash the traceparent on the request context so AfterResponse can read it.
		r.SetContext(withTraceparent(r.Context(), tp))
		return nil
	})

	c.OnAfterResponse(func(_ *resty.Client, r *resty.Response) error {
		tp, _ := traceparentFromContext(r.Request.Context())
		traceID, _ := TraceIDFromTraceparent(tp)
		w.SetLastResponse(&LastResponse{
			StatusCode: r.StatusCode(),
			Body:       r.Body(),
			TraceID:    traceID,
		})
		return nil
	})

	return c
}

const httpTimeout = 10 * 1_000_000_000 // 10s in nanoseconds, avoiding "time" import bloat in helpers
```

Add a small context helper at the bottom of the file:

```go
import "context"

type traceparentKey struct{}

func withTraceparent(ctx context.Context, tp string) context.Context {
	return context.WithValue(ctx, traceparentKey{}, tp)
}

func traceparentFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(traceparentKey{}).(string)
	return v, ok
}
```

(Consolidate the two import blocks into one when finalizing.)

- [ ] **Step 3: Build to verify it compiles**

```bash
cd tools/integration-suite && go test -run NonExistent ./...
```

Expected: PASS with `no tests to run` (proves the helper compiles).

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite/http_helpers_test.go go.mod go.sum
git commit -m "feat(integration-suite): HTTP helper with traceparent propagation"
```

---

## Task 9: World wiring + InitializeScenario hook

**Files:**
- Modify: `tools/integration-suite/main_test.go`

- [ ] **Step 1: Update `main_test.go` to create the World once and hook BeforeScenario**

Replace the body of `tools/integration-suite/main_test.go` with:

```go
package integrationsuite

import (
	"log/slog"
	"os"
	"testing"

	"github.com/cucumber/godog"
)

var (
	suiteConfig *Config
	suiteRunID  string
	suiteWorld  *World
)

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		Name:                 "integration-suite",
		ScenarioInitializer:  InitializeScenario,
		TestSuiteInitializer: InitializeTestSuite,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}
	if status := suite.Run(); status != 0 {
		os.Exit(status)
	}
}

// InitializeTestSuite loads config and run ID once before any scenario.
func InitializeTestSuite(ctx *godog.TestSuiteContext) {
	ctx.BeforeSuite(func() {
		cfg, err := LoadConfig()
		if err != nil {
			slog.Error("integration-suite: config load failed", "err", err)
			os.Exit(2)
		}
		suiteConfig = cfg
		suiteRunID = GenerateRunID()
		suiteWorld = NewWorld(suiteRunID)
		slog.Info("integration-suite: starting", "runID", suiteRunID, "sites", cfg.Sites)
	})
}

// InitializeScenario installs per-scenario state on the World.
func InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		suiteWorld.BeginScenario(sc.Name)
		return c, nil
	})
}
```

Add the `context` import to the import block.

- [ ] **Step 2: Run the suite to confirm it still boots clean**

```bash
cd tools/integration-suite && SITES=tw PRIMARY_SITE=tw \
  AUTH_SERVICE_URL_TW=http://example.invalid \
  ROOM_SERVICE_URL_TW=http://example.invalid \
  go test -v -run TestFeatures
```

Expected: PASS. Suite logs `integration-suite: starting` with a 4-hex runID, then `0 scenarios`.

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite/main_test.go
git commit -m "feat(integration-suite): wire World into godog scenario lifecycle"
```

---

## Task 10: Blindspot hook

**Files:**
- Create: `tools/integration-suite/blindspot_test.go`
- Modify: `tools/integration-suite/main_test.go`

- [ ] **Step 1: Write the failing test**

The blindspot hook is wired into godog; the testable unit is the *intent* to mark blindspot scenarios as failed regardless of step outcome. We test that via a helper function `BlindspotFailure(slugs)` returning a `*BlindspotErr` that godog will surface as a step failure.

Create `tools/integration-suite/blindspot_test.go`:

```go
package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlindspotFailure_NilWhenNoSlugs(t *testing.T) {
	assert.Nil(t, BlindspotFailure(nil))
	assert.Nil(t, BlindspotFailure([]string{}))
}

func TestBlindspotFailure_ContainsAllSlugs(t *testing.T) {
	err := BlindspotFailure([]string{"partial-history", "another"})
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "undocumented behavior")
	assert.Contains(t, err.Error(), "partial-history")
	assert.Contains(t, err.Error(), "another")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run TestBlindspot
```

Expected: FAIL — `BlindspotFailure` undefined.

- [ ] **Step 3: Implement `blindspot.go`**

Create `tools/integration-suite/blindspot.go`:

```go
package integrationsuite

import (
	"errors"
	"fmt"
	"strings"
)

// BlindspotFailure returns a non-nil error for scenarios tagged with
// any @blindspot:<slug>. Returns nil when slugs is empty.
//
// Returned by the After hook so godog records the scenario as a
// failure with a clear reason; the actual step outcomes are also
// visible in the report.
func BlindspotFailure(slugs []string) error {
	if len(slugs) == 0 {
		return nil
	}
	return fmt.Errorf("undocumented behavior: %s", strings.Join(slugs, ", "))
}

// ErrBlindspot is the sentinel returned (wrapped) by BlindspotFailure.
// Useful if callers want to differentiate via errors.Is later.
var ErrBlindspot = errors.New("blindspot")
```

- [ ] **Step 4: Wire the hook into `main_test.go`**

In `tools/integration-suite/main_test.go`, replace `InitializeScenario` with:

```go
func InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		suiteWorld.BeginScenario(sc.Name)
		return c, nil
	})

	ctx.After(func(c context.Context, sc *godog.Scenario, stepErr error) (context.Context, error) {
		tagNames := make([]string, 0, len(sc.Tags))
		for _, t := range sc.Tags {
			tagNames = append(tagNames, t.Name)
		}
		if err := BlindspotFailure(BlindspotsFromTags(tagNames)); err != nil {
			// Force the scenario to count as failed by returning the error,
			// regardless of stepErr.
			return c, err
		}
		return c, stepErr
	})
}
```

- [ ] **Step 5: Run all unit tests + the empty suite**

```bash
cd tools/integration-suite && go test -v ./...
```

Expected: all unit tests PASS; `TestFeatures` PASSes with 0 scenarios (no feature files yet).

- [ ] **Step 6: Commit**

```bash
git add tools/integration-suite/blindspot.go tools/integration-suite/blindspot_test.go tools/integration-suite/main_test.go
git commit -m "feat(integration-suite): blindspot hook — @blindspot:<slug> forces failure"
```

---

## Task 11: Auth step definitions

**Files:**
- Create: `tools/integration-suite/auth_steps_test.go`

- [ ] **Step 1: Verify the auth-service token endpoint**

Before writing the step, confirm the auth-service endpoint shape:

```bash
grep -rn "POST\|router\.\|gin\." auth-service/*.go | head -20
```

Verify the endpoint that mints a JWT. The plan assumes
`POST /token` with body `{"account": "<name>"}` returning
`{"token": "<jwt>"}`. **If the actual endpoint differs, adjust the
request body, URL path, and response parsing in this step before
running it.**

- [ ] **Step 2: Implement the step definitions**

Create `tools/integration-suite/auth_steps_test.go`:

```go
package integrationsuite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"
)

// authenticatedTokens keeps JWTs per scenario, keyed by account name.
// Reset on BeginScenario via the World.
var authenticatedTokens = map[string]string{}

// registerAuthSteps is called from InitializeScenario to wire the
// auth-related step definitions.
func registerAuthSteps(ctx *godog.ScenarioContext) {
	authenticatedTokens = map[string]string{}

	ctx.Step(`^user "([^"]+)" is authenticated$`, userIsAuthenticated)
	ctx.Step(`^user "([^"]+)" is authenticated in site "([^"]+)"$`, userIsAuthenticatedInSite)
}

// userIsAuthenticated mints a JWT against the primary site's auth-service.
func userIsAuthenticated(ctx context.Context, name string) error {
	return userIsAuthenticatedInSite(ctx, name, suiteConfig.PrimarySite)
}

func userIsAuthenticatedInSite(_ context.Context, name, site string) error {
	prefixedAccount := suiteWorld.Prefix().ID(name)

	client := newHTTPClient(suiteWorld)
	resp, err := client.R().
		SetBody(map[string]string{"account": prefixedAccount}).
		SetHeader("Content-Type", "application/json").
		Post(suiteConfig.AuthServiceURL(site) + "/token")
	if err != nil {
		return fmt.Errorf("auth: minting token for %q: %w", prefixedAccount, err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("auth: minting token for %q: status %d body %s",
			prefixedAccount, resp.StatusCode(), string(resp.Body()))
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		return fmt.Errorf("auth: parsing token response: %w", err)
	}
	if body.Token == "" {
		return fmt.Errorf("auth: token response has empty token field")
	}

	// Store keyed by the *unprefixed* name so room_steps can resolve
	// "alice" → JWT without knowing the prefix mechanics.
	authenticatedTokens[name] = body.Token
	return nil
}

// tokenFor returns the JWT for a previously-authenticated user, or
// an error if the scenario forgot the Given step.
func tokenFor(name string) (string, error) {
	t, ok := authenticatedTokens[name]
	if !ok {
		return "", fmt.Errorf("auth: user %q is not authenticated in this scenario "+
			"(missing `Given user %q is authenticated`)", name, name)
	}
	return t, nil
}
```

- [ ] **Step 3: Register auth steps from `InitializeScenario`**

In `tools/integration-suite/main_test.go`, update `InitializeScenario`:

```go
func InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		suiteWorld.BeginScenario(sc.Name)
		return c, nil
	})

	registerAuthSteps(ctx)

	ctx.After(func(c context.Context, sc *godog.Scenario, stepErr error) (context.Context, error) {
		tagNames := make([]string, 0, len(sc.Tags))
		for _, t := range sc.Tags {
			tagNames = append(tagNames, t.Name)
		}
		if err := BlindspotFailure(BlindspotsFromTags(tagNames)); err != nil {
			return c, err
		}
		return c, stepErr
	})
}
```

- [ ] **Step 4: Run go build to verify it compiles**

```bash
cd tools/integration-suite && go build ./...
go vet ./...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/auth_steps_test.go tools/integration-suite/main_test.go
git commit -m "feat(integration-suite): auth step definitions — mint JWT via auth-service"
```

---

## Task 12: Room step definitions

**Files:**
- Create: `tools/integration-suite/room_steps_test.go`

- [ ] **Step 1: Verify room-service endpoints**

Confirm the endpoint shape:

```bash
grep -rn "POST\|GET\|router\.\|gin\." room-service/routes.go room-service/handler.go | head -30
```

Plan assumes:
- `POST /rooms` body `{"id": "<id>", "type": "channel", "name": "<name>"}` returning the created room
- `GET /rooms/{id}` returning the room

**Adjust if actual shapes differ.**

- [ ] **Step 2: Implement step definitions**

Create `tools/integration-suite/room_steps_test.go`:

```go
package integrationsuite

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerRoomSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^"([^"]+)" creates channel room "([^"]+)"$`, createsChannelRoom)
	ctx.Step(`^"([^"]+)" reads room "([^"]+)"$`, readsRoom)
}

func createsChannelRoom(_ context.Context, actor, roomName string) error {
	tok, err := tokenFor(actor)
	if err != nil {
		return err
	}

	prefixedID := suiteWorld.Prefix().ID(roomName)

	client := newHTTPClient(suiteWorld)
	resp, err := client.R().
		SetAuthToken(tok).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]any{
			"id":   prefixedID,
			"type": "channel",
			"name": roomName,
		}).
		Post(suiteConfig.RoomServiceURL(suiteConfig.PrimarySite) + "/rooms")
	if err != nil {
		return fmt.Errorf("room: create channel %q: %w", roomName, err)
	}
	// Result captured into world.LastResponse via the OnAfterResponse hook.
	_ = resp
	return nil
}

func readsRoom(_ context.Context, actor, roomName string) error {
	tok, err := tokenFor(actor)
	if err != nil {
		return err
	}

	prefixedID := suiteWorld.Prefix().ID(roomName)

	client := newHTTPClient(suiteWorld)
	_, err = client.R().
		SetAuthToken(tok).
		Get(suiteConfig.RoomServiceURL(suiteConfig.PrimarySite) + "/rooms/" + prefixedID)
	if err != nil {
		return fmt.Errorf("room: read %q: %w", roomName, err)
	}
	return nil
}
```

- [ ] **Step 3: Register from `InitializeScenario`**

In `tools/integration-suite/main_test.go`, add after `registerAuthSteps(ctx)`:

```go
	registerRoomSteps(ctx)
```

- [ ] **Step 4: Build & vet**

```bash
cd tools/integration-suite && go build ./... && go vet ./...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/room_steps_test.go tools/integration-suite/main_test.go
git commit -m "feat(integration-suite): room HTTP step definitions"
```

---

## Task 13: Error class assertion steps

**Files:**
- Create: `tools/integration-suite/error_steps_test.go`

- [ ] **Step 1: Implement**

Create `tools/integration-suite/error_steps_test.go`:

```go
package integrationsuite

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerErrorSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the response is a (\w+) error$`, responseIsErrorClass)
	ctx.Step(`^the response status is (\d+)$`, responseStatusIs)
	ctx.Step(`^the response is successful$`, responseIsSuccessful)
}

func responseIsErrorClass(_ context.Context, want string) error {
	last := suiteWorld.LastResponse()
	if last == nil {
		return fmt.Errorf("error-class assertion: no response captured (did a When step run?)")
	}
	got := ClassifyHTTP(last.StatusCode, last.Body)
	if string(got) != want {
		return fmt.Errorf("error-class: want %s, got %s (status %d body %s, trace %s)",
			want, got, last.StatusCode, string(last.Body), last.TraceID)
	}
	return nil
}

func responseStatusIs(_ context.Context, want int) error {
	last := suiteWorld.LastResponse()
	if last == nil {
		return fmt.Errorf("status assertion: no response captured")
	}
	if last.StatusCode != want {
		return fmt.Errorf("status: want %d, got %d (body %s, trace %s)",
			want, last.StatusCode, string(last.Body), last.TraceID)
	}
	return nil
}

func responseIsSuccessful(_ context.Context) error {
	last := suiteWorld.LastResponse()
	if last == nil {
		return fmt.Errorf("successful assertion: no response captured")
	}
	if last.StatusCode < 200 || last.StatusCode >= 300 {
		return fmt.Errorf("successful: status %d, body %s, trace %s",
			last.StatusCode, string(last.Body), last.TraceID)
	}
	return nil
}
```

- [ ] **Step 2: Register from `InitializeScenario`**

In `tools/integration-suite/main_test.go`, add after `registerRoomSteps(ctx)`:

```go
	registerErrorSteps(ctx)
```

- [ ] **Step 3: Build & vet**

```bash
cd tools/integration-suite && go build ./... && go vet ./...
```

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite/error_steps_test.go tools/integration-suite/main_test.go
git commit -m "feat(integration-suite): error-class assertion steps"
```

---

## Task 14: First smoke feature file

**Files:**
- Create: `tools/integration-suite/features/service/room.feature`

- [ ] **Step 1: Write the feature file**

Create `tools/integration-suite/features/service/room.feature`:

```gherkin
Feature: Room service — basic HTTP behavior
  # Source: room-service/handler.go, room-service/routes.go

  @status:approved
  Scenario: Authenticated user creates a channel room
    Given user "alice" is authenticated
    When "alice" creates channel room "general"
    Then the response is successful

  Scenario: Reading a non-existent room is a HandlerError
    Given user "alice" is authenticated
    When "alice" reads room "definitely-does-not-exist"
    Then the response is a HandlerError error
```

The second scenario has no `@status:approved` tag — it is draft by default, demonstrating the two-score split.

- [ ] **Step 2: List the registered steps (sanity check)**

```bash
cd tools/integration-suite && SITES=tw PRIMARY_SITE=tw \
  AUTH_SERVICE_URL_TW=http://example.invalid \
  ROOM_SERVICE_URL_TW=http://example.invalid \
  go test -v -run TestFeatures -- --godog.format=pretty --godog.dry-run
```

Expected: godog parses both scenarios, lists each step as matched (no "undefined step" errors). (Adjust flag passing if godog dry-run syntax differs in your installed version.)

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite/features/service/room.feature
git commit -m "feat(integration-suite): first smoke feature — room service HTTP basics"
```

---

## Task 15: Reporter — markdown output with two-score split

**Files:**
- Create: `tools/integration-suite/reporter.go`
- Create: `tools/integration-suite/reporter_test.go`
- Modify: `tools/integration-suite/main_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/reporter_test.go`:

```go
package integrationsuite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSummary_TwoScoreSplit(t *testing.T) {
	summary := &RunSummary{
		RunID:    "7a2c",
		StartISO: "2026-05-12T14:22:11Z",
		Duration: "4m12s",
		Approved: ScopeSummary{
			Passed:    108,
			Failed:    5,
			Blindspot: 3,
			FailureByClass: map[Class]int{
				ClassHandlerError: 2,
				ClassTimeout:      2,
				ClassPersistence:  1,
			},
		},
		Draft: ScopeSummary{
			Passed:    30,
			Failed:    7,
			Blindspot: 5,
			FailureByClass: map[Class]int{
				ClassHandlerError: 2,
				ClassTimeout:      3,
				ClassUnreachable:  2,
			},
		},
	}

	out := RenderSummary(summary)

	assert.Contains(t, out, "APPROVED")
	assert.Contains(t, out, "DRAFT")
	assert.Contains(t, out, "93.1%")   // 108 / (108+5+3) = 0.9310...
	assert.Contains(t, out, "71.4%")   // 30 / (30+7+5)   = 0.7142...
	assert.Contains(t, out, "7a2c")
	assert.Contains(t, out, "HandlerError")
}

func TestRenderSummary_EmptyApprovedReportsZero(t *testing.T) {
	summary := &RunSummary{
		RunID:    "0000",
		StartISO: "2026-05-12T00:00:00Z",
		Duration: "0s",
		Approved: ScopeSummary{},
		Draft:    ScopeSummary{},
	}

	out := RenderSummary(summary)
	require.NotEmpty(t, out)
	assert.Contains(t, out, "0 scenarios")
}

func TestRenderSummary_FailureRowsIncludeStatusAndTrace(t *testing.T) {
	summary := &RunSummary{
		RunID:    "7a2c",
		StartISO: "2026-05-12T14:22:11Z",
		Duration: "1s",
		Approved: ScopeSummary{Passed: 1},
		Failures: []FailureRow{
			{
				Status:      StatusApproved,
				FeatureFile: "service/room.feature",
				Line:        12,
				Name:        "Reading a missing room",
				Class:       ClassHandlerError,
				TraceID:     "4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7",
			},
		},
	}

	out := RenderSummary(summary)
	assert.True(t, strings.Contains(out, "[APPROVED]"))
	assert.Contains(t, out, "service/room.feature:12")
	assert.Contains(t, out, "HandlerError")
	assert.Contains(t, out, "4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run TestRenderSummary
```

Expected: FAIL — `RunSummary`/`RenderSummary` undefined.

- [ ] **Step 3: Implement `reporter.go`**

Create `tools/integration-suite/reporter.go`:

```go
package integrationsuite

import (
	"fmt"
	"sort"
	"strings"
)

// ScopeSummary aggregates pass/fail/blindspot counts for one status group.
type ScopeSummary struct {
	Passed         int
	Failed         int
	Blindspot      int
	FailureByClass map[Class]int
}

func (s ScopeSummary) Total() int { return s.Passed + s.Failed + s.Blindspot }

// ScorePct returns the conformance score for this scope as a percent.
// Returns 0.0 when there are no scenarios in the scope.
func (s ScopeSummary) ScorePct() float64 {
	t := s.Total()
	if t == 0 {
		return 0.0
	}
	return 100.0 * float64(s.Passed) / float64(t)
}

// FailureRow describes one failing or blindspot scenario for inclusion
// in the human summary.
type FailureRow struct {
	Status      Status
	FeatureFile string
	Line        int
	Name        string
	Class       Class
	TraceID     string
	Reason      string // populated for blindspots
}

// RunSummary is the input to RenderSummary.
type RunSummary struct {
	RunID    string
	StartISO string
	Duration string
	Approved ScopeSummary
	Draft    ScopeSummary
	Failures []FailureRow
	// Last audit (optional): set zero values to omit.
	LastAuditISO string
	AuditN       int
	AuditAccPct  float64
	AuditFPPct   float64
	AuditFNPct   float64
}

// RenderSummary returns the markdown body of last-run.md.
func RenderSummary(s *RunSummary) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Run:        %s   (runID %s)\n", s.StartISO, s.RunID)
	total := s.Approved.Total() + s.Draft.Total()
	fmt.Fprintf(&b, "Total:      %d scenarios\n", total)
	fmt.Fprintf(&b, "Duration:   %s\n\n", s.Duration)

	writeScope(&b, "APPROVED", s.Approved)
	b.WriteString("\n")
	writeScope(&b, "DRAFT", s.Draft)

	if s.LastAuditISO != "" {
		fmt.Fprintf(&b, "\nLast audit: %s (n=%d) — accuracy %.1f%%, FP %.1f%%, FN %.1f%%\n",
			s.LastAuditISO, s.AuditN, s.AuditAccPct, s.AuditFPPct, s.AuditFNPct)
	}

	failures := nonBlindspotFailures(s.Failures)
	if len(failures) > 0 {
		b.WriteString("\nFailures (behavior diverged from design)\n")
		for _, f := range failures {
			fmt.Fprintf(&b, "  [%s] %s:%d %q\n", strings.ToUpper(string(f.Status)), f.FeatureFile, f.Line, f.Name)
			fmt.Fprintf(&b, "    class: %s\n", f.Class)
			if f.TraceID != "" {
				fmt.Fprintf(&b, "    trace: %s\n", f.TraceID)
			}
		}
	}

	blindspots := blindspotFailures(s.Failures)
	if len(blindspots) > 0 {
		b.WriteString("\nBlindspots (undocumented behavior — design owes an answer)\n")
		for _, f := range blindspots {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", strings.ToUpper(string(f.Status)), f.FeatureFile, f.Reason)
		}
	}

	return b.String()
}

func writeScope(b *strings.Builder, label string, s ScopeSummary) {
	fmt.Fprintf(b, "%s   %d scenarios\n", label, s.Total())
	fmt.Fprintf(b, "  Passed:        %d\n", s.Passed)
	fmt.Fprintf(b, "  Failed:        %d\n", s.Failed)
	if s.Failed > 0 {
		classes := sortedClasses(s.FailureByClass)
		for _, c := range classes {
			fmt.Fprintf(b, "    %-14s %d\n", string(c)+":", s.FailureByClass[c])
		}
	}
	fmt.Fprintf(b, "  Blindspot:     %d\n", s.Blindspot)
	fmt.Fprintf(b, "  Score:       %5.1f%%   (%d / %d)\n", s.ScorePct(), s.Passed, s.Total())
}

func sortedClasses(m map[Class]int) []Class {
	out := make([]Class, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

func nonBlindspotFailures(f []FailureRow) []FailureRow {
	var out []FailureRow
	for _, r := range f {
		if r.Reason == "" {
			out = append(out, r)
		}
	}
	return out
}

func blindspotFailures(f []FailureRow) []FailureRow {
	var out []FailureRow
	for _, r := range f {
		if r.Reason != "" {
			out = append(out, r)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run TestRenderSummary
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/reporter.go tools/integration-suite/reporter_test.go
git commit -m "feat(integration-suite): reporter — two-score markdown summary"
```

---

## Task 16: Cucumber JSON post-processor

**Files:**
- Create: `tools/integration-suite/cucumber_parse.go`
- Create: `tools/integration-suite/cucumber_parse_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/cucumber_parse_test.go`:

```go
package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCucumber_CountsPassedAndFailedPerStatus(t *testing.T) {
	// Minimal cucumber JSON: two scenarios, one approved-passed, one draft-failed.
	doc := []byte(`[
		{
			"uri": "features/service/room.feature",
			"elements": [
				{
					"id": "feat;scen-1",
					"name": "Approved scenario",
					"line": 5,
					"type": "scenario",
					"tags": [{"name": "@status:approved"}],
					"steps": [
						{"name": "step 1", "result": {"status": "passed"}}
					]
				},
				{
					"id": "feat;scen-2",
					"name": "Draft failing",
					"line": 12,
					"type": "scenario",
					"tags": [],
					"steps": [
						{"name": "step a", "result": {"status": "failed", "error_message": "boom"}}
					]
				}
			]
		}
	]`)

	summary, failures, err := ParseCucumber(doc)
	require.NoError(t, err)

	assert.Equal(t, 1, summary.Approved.Passed)
	assert.Equal(t, 0, summary.Approved.Failed)
	assert.Equal(t, 0, summary.Draft.Passed)
	assert.Equal(t, 1, summary.Draft.Failed)

	require.Len(t, failures, 1)
	assert.Equal(t, StatusDraft, failures[0].Status)
	assert.Equal(t, "Draft failing", failures[0].Name)
	assert.Equal(t, "features/service/room.feature", failures[0].FeatureFile)
	assert.Equal(t, 12, failures[0].Line)
}

func TestParseCucumber_BlindspotCountsAsFailureWithReason(t *testing.T) {
	doc := []byte(`[
		{
			"uri": "features/regional-resilience/x.feature",
			"elements": [
				{
					"id": "feat;scen-1",
					"name": "Has blindspot",
					"line": 8,
					"type": "scenario",
					"tags": [{"name": "@status:approved"}, {"name": "@blindspot:foo"}],
					"steps": [
						{"name": "step", "result": {"status": "failed", "error_message": "undocumented behavior: foo"}}
					]
				}
			]
		}
	]`)

	summary, failures, err := ParseCucumber(doc)
	require.NoError(t, err)

	assert.Equal(t, 0, summary.Approved.Failed)
	assert.Equal(t, 1, summary.Approved.Blindspot)

	require.Len(t, failures, 1)
	assert.Equal(t, "foo", failures[0].Reason)
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run TestParseCucumber
```

Expected: FAIL — `ParseCucumber` undefined.

- [ ] **Step 3: Implement `cucumber_parse.go`**

Create `tools/integration-suite/cucumber_parse.go`:

```go
package integrationsuite

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Cucumber JSON minimal schema. Only the fields we need.
type cukeFeature struct {
	URI      string         `json:"uri"`
	Elements []cukeScenario `json:"elements"`
}

type cukeScenario struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Line  int        `json:"line"`
	Type  string     `json:"type"`
	Tags  []cukeTag  `json:"tags"`
	Steps []cukeStep `json:"steps"`
}

type cukeTag struct {
	Name string `json:"name"`
}

type cukeStep struct {
	Name   string     `json:"name"`
	Result cukeResult `json:"result"`
}

type cukeResult struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
}

// ParseCucumber reads a cucumber JSON document and returns the
// aggregated RunSummary (Approved + Draft) plus the flat list of
// failure/blindspot rows for the human report.
//
// The returned RunSummary does NOT set RunID / StartISO / Duration —
// the caller (main_test.go post-run) fills those in.
func ParseCucumber(doc []byte) (*RunSummary, []FailureRow, error) {
	var features []cukeFeature
	if err := json.Unmarshal(doc, &features); err != nil {
		return nil, nil, fmt.Errorf("cucumber: parse: %w", err)
	}

	summary := &RunSummary{
		Approved: ScopeSummary{FailureByClass: map[Class]int{}},
		Draft:    ScopeSummary{FailureByClass: map[Class]int{}},
	}
	var failures []FailureRow

	for _, f := range features {
		for _, sc := range f.Elements {
			if sc.Type != "scenario" {
				continue
			}
			tagNames := make([]string, 0, len(sc.Tags))
			for _, t := range sc.Tags {
				tagNames = append(tagNames, t.Name)
			}
			status := StatusFromTags(tagNames)
			blindspots := BlindspotsFromTags(tagNames)

			scope := scopeFor(summary, status)

			scenarioFailed, errMsg := scenarioOutcome(sc)

			switch {
			case len(blindspots) > 0:
				scope.Blindspot++
				failures = append(failures, FailureRow{
					Status:      status,
					FeatureFile: relURI(f.URI),
					Line:        sc.Line,
					Name:        sc.Name,
					Reason:      strings.Join(blindspots, ", "),
				})
			case scenarioFailed:
				scope.Failed++
				cls := ClassUnclassified // v1: HTTP class detection happens upstream; default here.
				scope.FailureByClass[cls]++
				failures = append(failures, FailureRow{
					Status:      status,
					FeatureFile: relURI(f.URI),
					Line:        sc.Line,
					Name:        sc.Name,
					Class:       cls,
					// TraceID and refined Class would be set by upstream
					// hooks in a richer integration. v1 reports what
					// godog gave us.
				})
				_ = errMsg
			default:
				scope.Passed++
			}
		}
	}

	return summary, failures, nil
}

func scopeFor(s *RunSummary, status Status) *ScopeSummary {
	if status == StatusApproved {
		return &s.Approved
	}
	return &s.Draft
}

// scenarioOutcome returns (failed, lastErrorMessage). A scenario fails
// if any step failed.
func scenarioOutcome(sc cukeScenario) (bool, string) {
	for _, step := range sc.Steps {
		if step.Result.Status == "failed" {
			return true, step.Result.ErrorMessage
		}
	}
	return false, ""
}

// relURI strips a leading "./" from cucumber's URIs.
func relURI(u string) string { return strings.TrimPrefix(u, "./") }
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run TestParseCucumber
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite/cucumber_parse.go tools/integration-suite/cucumber_parse_test.go
git commit -m "feat(integration-suite): cucumber JSON parser produces RunSummary + failure rows"
```

---

## Task 17: Wire reporter into TestFeatures post-run

**Files:**
- Modify: `tools/integration-suite/main_test.go`

- [ ] **Step 1: Update `main_test.go` to use cucumber + junit + post-run reporter**

Replace `TestFeatures` and `InitializeTestSuite`:

```go
func TestFeatures(t *testing.T) {
	if err := os.MkdirAll("reports", 0o755); err != nil {
		t.Fatalf("reports dir: %v", err)
	}

	suite := godog.TestSuite{
		Name:                 "integration-suite",
		ScenarioInitializer:  InitializeScenario,
		TestSuiteInitializer: InitializeTestSuite,
		Options: &godog.Options{
			Format:   "pretty,cucumber:reports/cucumber.json,junit:reports/junit.xml",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}

	startedAt := time.Now().UTC()
	exit := suite.Run()
	duration := time.Since(startedAt).Round(time.Second)

	// Post-run: build human summary regardless of exit code.
	if err := writeHumanSummary(startedAt, duration); err != nil {
		t.Logf("integration-suite: writing human summary: %v", err)
	}

	if exit != 0 {
		// Don't os.Exit here — let TestingT handle the failure signal cleanly.
		t.Fatalf("integration-suite: exit %d", exit)
	}
}

func writeHumanSummary(startedAt time.Time, duration time.Duration) error {
	doc, err := os.ReadFile("reports/cucumber.json")
	if err != nil {
		return fmt.Errorf("read cucumber.json: %w", err)
	}
	summary, failures, err := ParseCucumber(doc)
	if err != nil {
		return err
	}
	summary.RunID = suiteRunID
	summary.StartISO = startedAt.Format(time.RFC3339)
	summary.Duration = duration.String()
	summary.Failures = failures

	out := RenderSummary(summary)

	// Write to docs/integration-suite/last-run.md. The path is relative
	// to repo root, but tests run with CWD=tools/integration-suite — so
	// we walk up.
	targetDir := filepath.Join("..", "..", "docs", "integration-suite")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", targetDir, err)
	}
	target := filepath.Join(targetDir, "last-run.md")
	if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}
```

Add to the import block:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cucumber/godog"
)
```

- [ ] **Step 2: Build & vet**

```bash
cd tools/integration-suite && go build ./... && go vet ./...
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite/main_test.go
git commit -m "feat(integration-suite): write human summary after each run"
```

---

## Task 18: Audit checklist generator (`cmd/audit`)

**Files:**
- Create: `tools/integration-suite/cmd/audit/main.go`
- Create: `tools/integration-suite/audit.go`
- Create: `tools/integration-suite/audit_test.go`

- [ ] **Step 1: Write the failing test for the checklist generator**

Create `tools/integration-suite/audit_test.go`:

```go
package integrationsuite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderAuditChecklist_OnlyApprovedScenariosSampled(t *testing.T) {
	rows := []FailureRow{
		{Status: StatusApproved, FeatureFile: "service/room.feature", Line: 5, Name: "A passed"},
		{Status: StatusDraft, FeatureFile: "service/room.feature", Line: 12, Name: "Draft failed"},
	}
	approved := []AuditRow{
		{FeatureFile: "service/room.feature", Line: 5, Name: "A passed", Outcome: "Passed"},
		{FeatureFile: "service/room.feature", Line: 18, Name: "B passed", Outcome: "Passed"},
	}

	out := RenderAuditChecklist("7a2c", approved)

	assert.Contains(t, out, "7a2c")
	assert.Contains(t, out, "service/room.feature:5")
	assert.Contains(t, out, "service/room.feature:18")
	assert.NotContains(t, out, "Draft failed", "drafts must not be audited")
	_ = rows
}

func TestSampleApproved_NeverReturnsMoreThanN(t *testing.T) {
	all := []AuditRow{
		{FeatureFile: "a.feature", Line: 1, Name: "x"},
		{FeatureFile: "b.feature", Line: 2, Name: "y"},
		{FeatureFile: "c.feature", Line: 3, Name: "z"},
	}
	got := SampleApproved(all, 2, 1234)
	require.Len(t, got, 2)
}

func TestSampleApproved_NisLargerThanPopulation(t *testing.T) {
	all := []AuditRow{{FeatureFile: "a.feature", Line: 1, Name: "x"}}
	got := SampleApproved(all, 10, 1234)
	require.Len(t, got, 1)
}

func TestSampleApproved_DeterministicForSeed(t *testing.T) {
	all := []AuditRow{
		{FeatureFile: "a.feature", Line: 1, Name: "x"},
		{FeatureFile: "b.feature", Line: 2, Name: "y"},
		{FeatureFile: "c.feature", Line: 3, Name: "z"},
	}
	a := SampleApproved(all, 2, 99)
	b := SampleApproved(all, 2, 99)
	assert.Equal(t, a, b)
}

func TestRenderAuditChecklist_NoApprovedScenarios(t *testing.T) {
	out := RenderAuditChecklist("7a2c", nil)
	assert.True(t, strings.Contains(out, "no approved scenarios"))
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run "TestRenderAuditChecklist|TestSampleApproved"
```

Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement `audit.go`**

Create `tools/integration-suite/audit.go`:

```go
package integrationsuite

import (
	"fmt"
	"math/rand"
	"strings"
)

// AuditRow is one approved-scenario outcome eligible for sampling.
type AuditRow struct {
	FeatureFile string
	Line        int
	Name        string
	Outcome     string // "Passed" or "Failed"
}

// SampleApproved selects up to n rows from approved without replacement
// using a deterministic RNG seed (so re-runs are reproducible).
func SampleApproved(approved []AuditRow, n int, seed int64) []AuditRow {
	if n >= len(approved) {
		return approved
	}
	r := rand.New(rand.NewSource(seed))
	idx := r.Perm(len(approved))[:n]
	out := make([]AuditRow, 0, n)
	for _, i := range idx {
		out = append(out, approved[i])
	}
	return out
}

// RenderAuditChecklist returns the markdown checklist for human review.
// Two empty columns at the end are filled in by the reviewer:
// "Reviewer says" and "Class" (TP/TN/FP/FN).
func RenderAuditChecklist(runID string, sampled []AuditRow) string {
	if len(sampled) == 0 {
		return fmt.Sprintf("# Suite Audit — run %s\n\nno approved scenarios sampled.\n", runID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Suite Audit — run %s\n\n", runID)
	fmt.Fprintf(&b, "Sampled %d approved scenarios. Reviewer fills the last two columns.\n\n", len(sampled))
	b.WriteString("| Scenario | Outcome | Reviewer says | Class |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, r := range sampled {
		fmt.Fprintf(&b, "| %s:%d %s | %s |  |  |\n", r.FeatureFile, r.Line, r.Name, r.Outcome)
	}
	b.WriteString("\nFill Class as one of: TP, TN, FP, FN.\n")
	b.WriteString("Then run `make integration-suite-audit-tally`.\n")
	return b.String()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run "TestRenderAuditChecklist|TestSampleApproved"
```

Expected: all PASS.

- [ ] **Step 5: Implement the CLI driver**

Create `tools/integration-suite/cmd/audit/main.go`:

```go
// Command audit generates an audit checklist from the most recent
// cucumber JSON report. Output is written to
// docs/integration-suite/audit-<runID>.md.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	is "github.com/hmchangw/chat/tools/integration-suite"
)

func main() {
	sample := flag.Int("sample", 30, "number of approved scenarios to sample")
	cucumberPath := flag.String("cucumber", "tools/integration-suite/reports/cucumber.json", "path to cucumber.json")
	outDir := flag.String("out-dir", "docs/integration-suite", "output directory")
	seed := flag.Int64("seed", time.Now().UnixNano(), "RNG seed for sampling")
	flag.Parse()

	doc, err := os.ReadFile(*cucumberPath)
	if err != nil {
		slog.Error("audit: read cucumber.json", "err", err, "path", *cucumberPath)
		os.Exit(2)
	}
	_, failures, err := is.ParseCucumber(doc)
	if err != nil {
		slog.Error("audit: parse cucumber", "err", err)
		os.Exit(2)
	}

	approved := buildApprovedRows(doc, failures)
	sampled := is.SampleApproved(approved, *sample, *seed)

	runID := fmt.Sprintf("%d", *seed) // good enough — the runID would normally be passed via env
	out := is.RenderAuditChecklist(runID, sampled)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		slog.Error("audit: mkdir", "err", err)
		os.Exit(2)
	}
	target := filepath.Join(*outDir, fmt.Sprintf("audit-%s.md", runID))
	if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
		slog.Error("audit: write", "err", err)
		os.Exit(2)
	}
	fmt.Println(target)
}

// buildApprovedRows scans the cucumber JSON to find all approved
// scenarios (passing AND failing) and turns them into AuditRows.
// We do a second pass here because ParseCucumber currently returns
// only failure rows; the audit needs the whole approved set.
func buildApprovedRows(doc []byte, _ []is.FailureRow) []is.AuditRow {
	// Reuse the same minimal schema by re-parsing — keeping the
	// reporter and audit decoupled.
	var features []struct {
		URI      string `json:"uri"`
		Elements []struct {
			Name  string `json:"name"`
			Line  int    `json:"line"`
			Type  string `json:"type"`
			Tags  []struct {
				Name string `json:"name"`
			} `json:"tags"`
			Steps []struct {
				Result struct {
					Status string `json:"status"`
				} `json:"result"`
			} `json:"steps"`
		} `json:"elements"`
	}
	if err := jsonUnmarshal(doc, &features); err != nil {
		return nil
	}
	var out []is.AuditRow
	for _, f := range features {
		for _, sc := range f.Elements {
			if sc.Type != "scenario" {
				continue
			}
			tags := make([]string, 0, len(sc.Tags))
			for _, t := range sc.Tags {
				tags = append(tags, t.Name)
			}
			if is.StatusFromTags(tags) != is.StatusApproved {
				continue
			}
			outcome := "Passed"
			for _, step := range sc.Steps {
				if step.Result.Status == "failed" {
					outcome = "Failed"
					break
				}
			}
			out = append(out, is.AuditRow{
				FeatureFile: f.URI,
				Line:        sc.Line,
				Name:        sc.Name,
				Outcome:     outcome,
			})
		}
	}
	return out
}
```

Add a tiny indirection to satisfy the import without pulling in encoding/json in the package doc — at the top of the file, replace the `jsonUnmarshal` reference by adding:

```go
import "encoding/json"

var jsonUnmarshal = json.Unmarshal
```

(Combine with the other imports.)

- [ ] **Step 6: Build the binary**

```bash
go build ./tools/integration-suite/cmd/audit
```

Expected: produces a binary named `audit` in the working directory (or just compiles successfully if you don't redirect output).

- [ ] **Step 7: Commit**

```bash
git add tools/integration-suite/audit.go tools/integration-suite/audit_test.go tools/integration-suite/cmd/audit/
git commit -m "feat(integration-suite): audit checklist generator"
```

---

## Task 19: Audit tally command (`cmd/audit-tally`)

**Files:**
- Create: `tools/integration-suite/cmd/audit-tally/main.go`
- Extend: `tools/integration-suite/audit.go`
- Extend: `tools/integration-suite/audit_test.go`

- [ ] **Step 1: Write the failing tally tests**

Append to `tools/integration-suite/audit_test.go`:

```go
func TestTally_HappyPath(t *testing.T) {
	rows := []AuditClass{ClassTP, ClassTP, ClassFP, ClassFN, ClassTN, ClassTP}
	m := Tally(rows)

	assert.Equal(t, 3, m.TP)
	assert.Equal(t, 1, m.TN)
	assert.Equal(t, 1, m.FP)
	assert.Equal(t, 1, m.FN)
	assert.InDelta(t, 66.67, m.AccuracyPct(), 0.01)
}

func TestRenderTally_IncludesAllNumbers(t *testing.T) {
	m := ConfusionMatrix{TP: 22, TN: 5, FP: 2, FN: 1}
	out := RenderTally("7a2c", m)
	assert.Contains(t, out, "7a2c")
	assert.Contains(t, out, "TP=22")
	assert.Contains(t, out, "FP=2")
	assert.Contains(t, out, "FN=1")
	assert.Contains(t, out, "TN=5")
}

func TestParseChecklistClasses_ReadsClassColumn(t *testing.T) {
	md := "" +
		"# Suite Audit — run 7a2c\n\n" +
		"| Scenario | Outcome | Reviewer says | Class |\n" +
		"|---|---|---|---|\n" +
		"| f.feature:1 a | Passed | ok | TP |\n" +
		"| f.feature:2 b | Failed | bug | TN |\n" +
		"| f.feature:3 c | Passed | wrong | FP |\n"

	got, err := ParseChecklistClasses([]byte(md))
	require.NoError(t, err)
	assert.Equal(t, []AuditClass{ClassTP, ClassTN, ClassFP}, got)
}

func TestParseChecklistClasses_SkipsEmptyClassColumn(t *testing.T) {
	md := "" +
		"| Scenario | Outcome | Reviewer says | Class |\n" +
		"|---|---|---|---|\n" +
		"| f.feature:1 a | Passed | ok |  |\n" +
		"| f.feature:2 b | Failed | bug | TN |\n"

	got, err := ParseChecklistClasses([]byte(md))
	require.NoError(t, err)
	assert.Equal(t, []AuditClass{ClassTN}, got)
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run "TestTally|TestRenderTally|TestParseChecklistClasses"
```

Expected: FAIL — types undefined.

- [ ] **Step 3: Extend `audit.go` with tally code**

Append to `tools/integration-suite/audit.go`:

```go
// AuditClass is one of TP / TN / FP / FN.
type AuditClass string

const (
	ClassTP AuditClass = "TP"
	ClassTN AuditClass = "TN"
	ClassFP AuditClass = "FP"
	ClassFN AuditClass = "FN"
)

// ConfusionMatrix aggregates TP/TN/FP/FN counts.
type ConfusionMatrix struct {
	TP, TN, FP, FN int
}

// Tally computes the matrix from a flat list of classifications.
func Tally(rows []AuditClass) ConfusionMatrix {
	var m ConfusionMatrix
	for _, c := range rows {
		switch c {
		case ClassTP:
			m.TP++
		case ClassTN:
			m.TN++
		case ClassFP:
			m.FP++
		case ClassFN:
			m.FN++
		}
	}
	return m
}

// Total returns the sample size.
func (m ConfusionMatrix) Total() int { return m.TP + m.TN + m.FP + m.FN }

// AccuracyPct returns (TP+TN)/total as a percent. Zero when total is 0.
func (m ConfusionMatrix) AccuracyPct() float64 {
	t := m.Total()
	if t == 0 {
		return 0
	}
	return 100.0 * float64(m.TP+m.TN) / float64(t)
}

// FalsePositiveRatePct returns FP / (FP + TN) as a percent.
func (m ConfusionMatrix) FalsePositiveRatePct() float64 {
	d := m.FP + m.TN
	if d == 0 {
		return 0
	}
	return 100.0 * float64(m.FP) / float64(d)
}

// FalseNegativeRatePct returns FN / (FN + TP) as a percent.
func (m ConfusionMatrix) FalseNegativeRatePct() float64 {
	d := m.FN + m.TP
	if d == 0 {
		return 0
	}
	return 100.0 * float64(m.FN) / float64(d)
}

// RenderTally returns the markdown summary of a confusion matrix run.
func RenderTally(runID string, m ConfusionMatrix) string {
	return fmt.Sprintf(`# Audit Tally — run %s

Confusion matrix (n=%d):
              | actually correct | actually broken
test passed   |       TP=%d      |      FP=%d
test failed   |       FN=%d      |      TN=%d

Accuracy:                 %.1f%%
False-positive rate:      %.1f%%
False-negative rate:      %.1f%%
`, runID, m.Total(), m.TP, m.FP, m.FN, m.TN,
		m.AccuracyPct(), m.FalsePositiveRatePct(), m.FalseNegativeRatePct())
}

// ParseChecklistClasses extracts the Class column from a checklist
// markdown that the reviewer has filled in. Empty Class cells are
// skipped (sampled but un-classified rows do not count).
func ParseChecklistClasses(md []byte) ([]AuditClass, error) {
	lines := strings.Split(string(md), "\n")
	var out []AuditClass
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cols := strings.Split(ln, "|")
		// Expect at least: | scenario | outcome | reviewer | class |
		if len(cols) < 6 {
			continue
		}
		raw := strings.TrimSpace(cols[4])
		switch raw {
		case "TP", "TN", "FP", "FN":
			out = append(out, AuditClass(raw))
		case "Class", "---", "":
			// header / separator / unfilled
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run "TestTally|TestRenderTally|TestParseChecklistClasses"
```

Expected: all PASS.

- [ ] **Step 5: Implement the CLI driver**

Create `tools/integration-suite/cmd/audit-tally/main.go`:

```go
// Command audit-tally reads a reviewer-filled audit checklist and
// appends a tally + confusion matrix to docs/integration-suite/audit-log.md.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	is "github.com/hmchangw/chat/tools/integration-suite"
)

func main() {
	input := flag.String("in", "", "path to the audit checklist markdown (required)")
	logFile := flag.String("log", "docs/integration-suite/audit-log.md", "path to audit log markdown")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "audit-tally: --in is required")
		os.Exit(2)
	}

	md, err := os.ReadFile(*input)
	if err != nil {
		slog.Error("audit-tally: read input", "err", err)
		os.Exit(2)
	}
	classes, err := is.ParseChecklistClasses(md)
	if err != nil {
		slog.Error("audit-tally: parse", "err", err)
		os.Exit(2)
	}

	runID := runIDFromFilename(filepath.Base(*input))
	matrix := is.Tally(classes)
	body := is.RenderTally(runID, matrix)

	entry := fmt.Sprintf("\n## %s (run %s)\n\n%s\n",
		time.Now().UTC().Format(time.RFC3339), runID, body)

	if err := appendToFile(*logFile, entry); err != nil {
		slog.Error("audit-tally: append log", "err", err)
		os.Exit(2)
	}
	fmt.Println(body)
}

func runIDFromFilename(name string) string {
	// expects "audit-<runID>.md"
	s := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".md")
	if s == "" {
		return "unknown"
	}
	return s
}

func appendToFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}
```

- [ ] **Step 6: Build and commit**

```bash
go build ./tools/integration-suite/cmd/audit-tally
git add tools/integration-suite/audit.go tools/integration-suite/audit_test.go tools/integration-suite/cmd/audit-tally/
git commit -m "feat(integration-suite): audit tally — confusion matrix from reviewer checklist"
```

---

## Task 20: `integration-suite-steps` command (list registered steps)

**Files:**
- Create: `tools/integration-suite/cmd/steps/main.go`

- [ ] **Step 1: Implement**

The simplest approach: invoke `go test -godog.format=pretty -godog.dry-run` against the features-empty mode, capturing the step list godog prints. But a cleaner solution is a small helper that registers steps and prints the regexes.

For v1, use a pragmatic approach: this command runs `go test` with a special env flag that triggers a steps-only mode. Simpler still: implement it as a thin wrapper that runs the test binary with the right format.

Create `tools/integration-suite/cmd/steps/main.go`:

```go
// Command steps prints all currently registered godog step regexes,
// grouped by source file. It does so by running `go test` against
// the integration suite in dry-run mode and parsing the output.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command("go", "test",
		"-run", "TestFeatures",
		"-v",
		"./tools/integration-suite/...",
	)
	cmd.Env = append(os.Environ(),
		"GODOG_DRY_RUN=1",
		// Provide stub URLs so config loads.
		"SITES=tw",
		"PRIMARY_SITE=tw",
		"AUTH_SERVICE_URL_TW=http://example.invalid",
		"ROOM_SERVICE_URL_TW=http://example.invalid",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "steps:", err)
		os.Exit(1)
	}
}
```

A future refinement (v2) can replace this with a `--print-steps` flag on the test binary directly.

- [ ] **Step 2: Build and commit**

```bash
go build ./tools/integration-suite/cmd/steps
git add tools/integration-suite/cmd/steps/
git commit -m "feat(integration-suite): integration-suite-steps command (dry-run wrapper)"
```

---

## Task 21: `integration-suite-lint` command (blindspot consistency)

**Files:**
- Create: `tools/integration-suite/cmd/lint/main.go`
- Create: `tools/integration-suite/lint.go`
- Create: `tools/integration-suite/lint_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/integration-suite/lint_test.go`:

```go
package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractBlindspotSlugs_FromFeatureText(t *testing.T) {
	feat := `Feature: X
  @status:approved @blindspot:foo @smoke
  Scenario: A
    Given y

  @blindspot:bar
  Scenario: B
    Given z
`
	got := ExtractBlindspotSlugs(feat)
	assert.ElementsMatch(t, []string{"foo", "bar"}, got)
}

func TestExtractRegisteredSlugs_FromRegisterMarkdown(t *testing.T) {
	reg := `# Blindspots

## foo

text…

## bar

more text
`
	got := ExtractRegisteredSlugs(reg)
	assert.ElementsMatch(t, []string{"foo", "bar"}, got)
}

func TestDiffSlugs_Symmetric(t *testing.T) {
	inFeatures := []string{"foo", "bar"}
	inRegister := []string{"foo", "baz"}

	missingInRegister, missingInFeatures := DiffSlugs(inFeatures, inRegister)
	require.Len(t, missingInRegister, 1)
	assert.Contains(t, missingInRegister, "bar")
	require.Len(t, missingInFeatures, 1)
	assert.Contains(t, missingInFeatures, "baz")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd tools/integration-suite && go test -v -run "TestExtractBlindspot|TestExtractRegistered|TestDiffSlugs"
```

Expected: FAIL.

- [ ] **Step 3: Implement `lint.go`**

Create `tools/integration-suite/lint.go`:

```go
package integrationsuite

import (
	"regexp"
	"strings"
)

var blindspotInFeatureRE = regexp.MustCompile(`@blindspot:([a-z0-9][a-z0-9-]*)`)
var blindspotInRegisterRE = regexp.MustCompile(`(?m)^##\s+([a-z0-9][a-z0-9-]*)\s*$`)

// ExtractBlindspotSlugs returns all @blindspot:<slug> slugs found in
// a feature file's text.
func ExtractBlindspotSlugs(featureText string) []string {
	return uniq(matchAll(blindspotInFeatureRE, featureText))
}

// ExtractRegisteredSlugs returns slugs declared as level-2 headings
// in the blindspots.md register.
func ExtractRegisteredSlugs(registerText string) []string {
	return uniq(matchAll(blindspotInRegisterRE, registerText))
}

// DiffSlugs returns (slugs in features but not register, slugs in
// register but not features).
func DiffSlugs(features, register []string) (missingInRegister, missingInFeatures []string) {
	fset := setOf(features)
	rset := setOf(register)
	for s := range fset {
		if !rset[s] {
			missingInRegister = append(missingInRegister, s)
		}
	}
	for s := range rset {
		if !fset[s] {
			missingInFeatures = append(missingInFeatures, s)
		}
	}
	return
}

func matchAll(re *regexp.Regexp, s string) []string {
	matches := re.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func setOf(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}

// JoinLines is a tiny helper for the CLI to format slug lists.
func JoinLines(slugs []string) string {
	return "  - " + strings.Join(slugs, "\n  - ")
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd tools/integration-suite && go test -v -run "TestExtractBlindspot|TestExtractRegistered|TestDiffSlugs"
```

Expected: all PASS.

- [ ] **Step 5: Implement the CLI driver**

Create `tools/integration-suite/cmd/lint/main.go`:

```go
// Command lint verifies that every @blindspot:<slug> in a feature
// file has a matching entry in blindspots.md, and vice versa.
package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	is "github.com/hmchangw/chat/tools/integration-suite"
)

func main() {
	featuresRoot := "tools/integration-suite/features"
	registerPath := "docs/integration-suite/blindspots.md"

	var inFeatures []string
	err := filepath.WalkDir(featuresRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".feature") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		inFeatures = append(inFeatures, is.ExtractBlindspotSlugs(string(body))...)
		return nil
	})
	if err != nil {
		slog.Error("lint: walk features", "err", err)
		os.Exit(2)
	}

	regBody, err := os.ReadFile(registerPath)
	if err != nil {
		slog.Error("lint: read register", "err", err, "path", registerPath)
		os.Exit(2)
	}
	inRegister := is.ExtractRegisteredSlugs(string(regBody))

	missingInRegister, missingInFeatures := is.DiffSlugs(inFeatures, inRegister)

	if len(missingInRegister) == 0 && len(missingInFeatures) == 0 {
		fmt.Println("integration-suite-lint: ok — blindspot slugs are consistent")
		return
	}

	if len(missingInRegister) > 0 {
		fmt.Printf("\nBlindspots used in features but missing from %s:\n%s\n",
			registerPath, is.JoinLines(missingInRegister))
	}
	if len(missingInFeatures) > 0 {
		fmt.Printf("\nBlindspots registered in %s but no longer referenced by any feature:\n%s\n",
			registerPath, is.JoinLines(missingInFeatures))
	}
	os.Exit(1)
}
```

- [ ] **Step 6: Build and commit**

```bash
go build ./tools/integration-suite/cmd/lint
git add tools/integration-suite/lint.go tools/integration-suite/lint_test.go tools/integration-suite/cmd/lint/
git commit -m "feat(integration-suite): integration-suite-lint — blindspot register consistency"
```

---

## Task 22: Scaffold the docs/integration-suite/ directory and registers

**Files:**
- Create: `docs/integration-suite/blindspots.md`
- Create: `docs/integration-suite/audit-log.md`
- Create: `docs/integration-suite/.gitignore`

- [ ] **Step 1: Create the empty registers**

Create `docs/integration-suite/blindspots.md`:

```markdown
# Blindspots Register

Every `@blindspot:<slug>` used in any feature file under
`tools/integration-suite/features/` must have a matching level-2
heading (`## <slug>`) in this file.

Run `make integration-suite-lint` to verify consistency.

<!-- Entries follow this template:

## <slug>

**Found in:** features/<scope>/<feature>.feature
**Question:** What is the documented expected behavior for ...?
**Candidates:**
  - …
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future>.md
**Status:** open
**Added:** YYYY-MM-DD

-->
```

Create `docs/integration-suite/audit-log.md`:

```markdown
# Audit Log

Each `make integration-suite-audit-tally` run appends a section here.

```

Create `docs/integration-suite/.gitignore`:

```
# Per-run working files — regenerated each invocation.
last-run.md
audit-*.md
```

- [ ] **Step 2: Commit**

```bash
git add docs/integration-suite/
git commit -m "feat(integration-suite): scaffold blindspots and audit-log registers"
```

---

## Task 23: README

**Files:**
- Create: `tools/integration-suite/README.md`

- [ ] **Step 1: Write the README**

Create `tools/integration-suite/README.md`:

````markdown
# integration-suite

A scenario-driven black-box integration test suite for the chat
backend. See `docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`
for the full design.

This tool does NOT own infrastructure. It connects to whatever NATS /
MongoDB / Cassandra / service URLs are provided via env vars.

## Quick start

### 1. Bring up infra (kind) and services (docker-compose) yourself

The suite does not manage either. Bring them up the way you already do.

### 2. Export config

```bash
export SITES=tw
export PRIMARY_SITE=tw
export AUTH_SERVICE_URL_TW=http://localhost:8081
export ROOM_SERVICE_URL_TW=http://localhost:8082
```

For multi-site (Part 2), add `_US` variants and update `SITES`.

### 3. Run the suite

```bash
make integration-suite                 # everything
make integration-suite SCOPE=service   # one scope folder
make integration-suite TAGS=@smoke     # filter by tag
make integration-suite FEATURE=tools/integration-suite/features/service/room.feature
```

### 4. Read the report

```bash
cat docs/integration-suite/last-run.md
```

The report shows two scores side by side:

- **APPROVED** — `@status:approved` scenarios; this gates CI
- **DRAFT** — everything else; informational

### 5. Periodically audit (calibration)

```bash
make integration-suite-audit SAMPLE=30
# Review the generated markdown file, fill in Reviewer says / Class
make integration-suite-audit-tally IN=docs/integration-suite/audit-<runID>.md
```

The tally is appended to `docs/integration-suite/audit-log.md`.

## Authoring new scenarios

See `AUTHORING.md`. Both humans and AI agents follow the same rules.

## Available targets

| Target | Purpose |
|---|---|
| `make integration-suite [SCOPE=… TAGS=… FEATURE=…]` | Run scenarios |
| `make integration-suite-steps` | List registered step regexes |
| `make integration-suite-lint` | Verify blindspot register consistency |
| `make integration-suite-audit SAMPLE=<n>` | Generate audit checklist |
| `make integration-suite-audit-tally IN=<file>` | Tally a filled checklist |

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `config: AUTH_SERVICE_URL_TW is required` | Env var unset for a listed site |
| `auth: minting token: status 404` | `auth-service` not reachable, or endpoint shape differs from what `auth_steps_test.go` expects |
| `0 scenarios` | Feature files not in `tools/integration-suite/features/<scope>/` |
| Tests pass but `last-run.md` is empty | `reports/cucumber.json` failed to write — check `reports/` is writable |

## Status & maturity

This is Part 1 of the suite. Part 2 will add:

- NATS / JetStream / Mongo / Cassandra step libraries
- `pipeline/`, `federation/`, `resilience/`, `regional-resilience/` scopes
- Chaos integration (chaos-mesh, docker-compose pause/kill)
- `integration-suite-purge` data cleanup
````

- [ ] **Step 2: Commit**

```bash
git add tools/integration-suite/README.md
git commit -m "docs(integration-suite): README — user flow and quick start"
```

---

## Task 24: AUTHORING.md

**Files:**
- Create: `tools/integration-suite/AUTHORING.md`

- [ ] **Step 1: Write the authoring playbook**

Create `tools/integration-suite/AUTHORING.md`:

````markdown
# Authoring scenarios for the integration suite

This playbook applies to BOTH humans and AI agents adding scenarios.
Every rule is mandatory. Spec reference:
`docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`.

## Hard rules

```
RULE 1: Expected behavior comes from the design, not from training data.
RULE 2: If the design is silent, tag the scenario @blindspot:<slug>
        and add an entry to docs/integration-suite/blindspots.md.
        Do not invent expectations.
RULE 3: Reuse step phrasing exactly. Use `make integration-suite-steps`
        to list registered steps before writing new ones.
RULE 4: Every scenario is independently true. Unique IDs per scenario,
        no shared fixtures, no execution-order dependence.
RULE 5: Every scenario starts as draft (no @status:approved tag).
        Promotion to approved is a separate, human-reviewed PR.
```

## Sources of expected behavior (priority order)

1. `docs/superpowers/specs/*.md`
2. `CLAUDE.md`
3. `<service>/README.md`
4. `pkg/<pkg>/README.md`
5. Design comments in source code

A scenario MUST cite its source as a `# Source:` comment at the top of
the `Feature` or `Scenario`. Without a citation it cannot be promoted
to `@status:approved`.

## Scope decision tree

```
Faults involved?  yes  ──┬─ multi-site? yes → regional-resilience/
                         │                no → resilience/
                         no
                         │
Multi-site coordination? yes → federation/
                         no
                         │
Multiple flows composed? yes → journey/
                         no
                         │
Crosses request → stream → worker? yes → pipeline/
                                    no → service/
```

## Phrasing rules

- Lowercase verbs after Gherkin keywords (`Given user "alice" is authenticated`).
- Quote every parameter (`"alice"`, `"general"`).
- Multi-site scenarios append `in site "<id>"`. Single-site omit it.
- Async assertions declare a budget: `Then within 5s ...`.
- No implementation details. Read as user-visible behavior.

## Step vocabulary

Discover what's already registered:

```bash
make integration-suite-steps
```

Search the output for a matching phrase BEFORE adding a new step.
Adding a near-duplicate (`is logged in` vs `is authenticated`)
fragments the vocabulary; godog treats them as different steps.

## Tags

- Scope is implicit from the folder.
- `@status:approved` — human-reviewed and ratified. CI-gating.
- `@blindspot:<slug>` — undocumented behavior. Slug is kebab-case, unique.
- `@smoke` — fast, included in CI smoke runs.
- `@slow` — nightly only.
- `@multi-room` — uses multiple rooms.

## Scenario template

```gherkin
# Source: docs/superpowers/specs/2026-04-14-add-member-design.md
Feature: Room member operations
  Behavior driven by the add-member design.

  Background:
    Given user "alice" is authenticated
    And channel room "general" exists with member "alice"

  @status:approved @smoke
  Scenario: Adding a new member persists a subscription and replies success
    Given user "bob" is authenticated
    When "alice" requests to add member "bob" to room "general"
    Then within 3s "bob" is a member of room "general"
    And "alice" receives a success reply

  Scenario Outline: Adding to a missing room replies <class>
    Given user "<requester>" is authenticated
    When "<requester>" requests to add member "<target>" to room "nonexistent"
    Then the response is a <class> error

    Examples:
      | requester | target | class        |
      | alice     | bob    | HandlerError |
```

## Anti-patterns

- Inventing a near-duplicate step (`"is logged in"` vs `"is authenticated"`).
- Asserting on implementation (`handler.X was called`).
- Hard-coded IDs (`"room-1"`) instead of fixture-generated ones.
- Pre-seeded fixtures referenced across scenarios.
- Skipping `@blindspot` for "obvious" behavior the design doesn't state.
- Filling expected values from training data (model defaults, typical API conventions).
- `@smoke` on a scenario that takes more than 2 seconds.
- Omitting `within <duration>` on an async assertion.
- Adding `@status:approved` in the same PR that introduces the scenario.
  Approval is always a separate, deliberate human action.

## Author checklist (before commit)

- [ ] `# Source:` comment cites a real document and section
- [ ] Scope folder matches the decision tree
- [ ] All steps match registered phrasings (`make integration-suite-steps`)
- [ ] All fixture IDs created in `Given` steps; no hard-coded names
- [ ] Every async assertion has `within <duration>`
- [ ] No implementation details in step text
- [ ] Tags applied (no implicit-scope duplication; no premature `@status:approved`)
- [ ] If `@blindspot:<slug>`, matching entry exists in `docs/integration-suite/blindspots.md`
- [ ] Scenario runs locally and produces the expected result
- [ ] `make integration-suite-lint` passes

## AI agent prompt template

Copy this into Claude / Cursor / Copilot. Fill in `<…>` placeholders.

```
You are adding an integration test scenario to the chat backend suite.

Behavior to test: <one sentence>
Documented source: <file path + section>
Target scope: <one of: service, pipeline, journey, federation, resilience, regional-resilience>

Before writing:
1. Run `make integration-suite-steps` and read the output.
2. Read the documented source in full. If it does not actually specify
   expected behavior for this case, STOP and follow the blindspot
   workflow (do NOT invent expectations).

Now write the scenario by:
- Selecting the right feature file under tools/integration-suite/features/<scope>/
- Reusing existing step phrasings exactly (no invented variants)
- Adding a `# Source:` comment citing the documented source
- Following all rules in tools/integration-suite/AUTHORING.md
- Tagging as draft (do NOT add @status:approved — that is a separate human PR)

Output: a unified diff containing only the .feature change (plus any
necessary new step in *_test.go if no existing step matches).
```
````

- [ ] **Step 2: Commit**

```bash
git add tools/integration-suite/AUTHORING.md
git commit -m "docs(integration-suite): AUTHORING.md — playbook for humans and AI agents"
```

---

## Task 25: Root Makefile integration

**Files:**
- Modify: `Makefile` (root)

- [ ] **Step 1: Read the root Makefile to find a good insertion point**

```bash
head -50 Makefile
grep -n "^[a-z][a-z-]*:" Makefile | head -20
```

Insert the new targets at a sensible location (e.g., after existing `test-integration` or near `loadgen` targets if present).

- [ ] **Step 2: Add the targets**

Append the following block to the end of `Makefile`:

```makefile

# ── integration-suite ─────────────────────────────────────────────
# A scenario-driven black-box integration test suite. See
# tools/integration-suite/README.md.

INTEGRATION_SUITE_DIR := tools/integration-suite

.PHONY: integration-suite integration-suite-steps integration-suite-lint integration-suite-audit integration-suite-audit-tally

integration-suite:
	@cd $(INTEGRATION_SUITE_DIR) && \
		go test -v -run TestFeatures $(if $(SCOPE),-godog.paths=features/$(SCOPE)) \
		                              $(if $(TAGS),-godog.tags="$(TAGS)") \
		                              $(if $(FEATURE),-godog.paths=$(FEATURE))

integration-suite-steps:
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/steps

integration-suite-lint:
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/lint

integration-suite-audit:
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/audit -sample=$(or $(SAMPLE),30)

integration-suite-audit-tally:
	@if [ -z "$(IN)" ]; then echo "usage: make integration-suite-audit-tally IN=<path>"; exit 2; fi
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/audit-tally -in=$(IN)
```

- [ ] **Step 3: Test the targets compile**

```bash
make -n integration-suite SCOPE=service
make -n integration-suite-steps
make -n integration-suite-lint
```

Expected: prints the commands without errors (dry-run via `make -n`).

- [ ] **Step 4: Run the lint to verify it works against the live repo**

```bash
make integration-suite-lint
```

Expected: prints `integration-suite-lint: ok — blindspot slugs are consistent` (since v1 has no blindspots).

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "feat(integration-suite): Makefile targets for run, steps, lint, audit"
```

---

## Task 26: End-to-end smoke verification (manual)

This task is the user's verification gate. No code changes. The agent
should hand off after completing the prior tasks; the user runs the
smoke test against their real environment.

- [ ] **Step 1: Hand-off message**

After Task 25 commits, post a summary to the user with these
verification instructions:

```
Implementation Part 1 complete. To smoke-test against your live stack:

1. Bring up your kind infra + docker-compose services as you normally do.
2. Export the suite config:
   export SITES=tw PRIMARY_SITE=tw
   export AUTH_SERVICE_URL_TW=<your auth-service host port>
   export ROOM_SERVICE_URL_TW=<your room-service host port>
3. Run the suite:
   make integration-suite SCOPE=service
4. Read the report:
   cat docs/integration-suite/last-run.md

Expected:
- APPROVED section shows 1 scenario, 1 passed (or 1 failed if the
  endpoint shape differs from what auth_steps_test.go and
  room_steps_test.go assume).
- DRAFT section shows 1 scenario.

If the test fails due to endpoint shape mismatch, update the bodies/
paths in:
- tools/integration-suite/auth_steps_test.go (Step 11)
- tools/integration-suite/room_steps_test.go (Step 12)
```

---

## Self-review

Before handing the plan off, the writer ran the following checks:

**Spec coverage:**
- Status tags (`@status:approved` vs draft) — Task 5
- Blindspot mechanics — Task 10, register in Task 22, lint in Task 21
- 8-class error classifier (HTTP) — Task 6
- Tracing (traceparent generation + capture) — Task 7, wired in Task 8
- Two-score reporter — Tasks 15, 16, 17
- godog runner + per-site config — Tasks 1, 2, 9
- Audit (checklist gen + tally) — Tasks 18, 19
- AUTHORING playbook — Task 24
- Makefile targets — Task 25

**Out of scope (Part 2):** NATS/JetStream/Mongo/Cassandra step libraries, chaos integration, the four other scope feature files, data purge target. These are explicitly listed in the "Scope of THIS plan" table above.

**Placeholder scan:** None found. Every step has runnable code or a concrete file/command.

**Type consistency:** `Class`, `Status`, `LastResponse`, `World`, `IDPrefixer`, `RunSummary`, `ScopeSummary`, `FailureRow`, `AuditRow`, `AuditClass`, `ConfusionMatrix` are defined once, used consistently across tasks. Function names (`LoadConfig`, `NewWorld`, `NewIDPrefixer`, `StatusFromTags`, `ClassifyHTTP`, `NewTraceparent`, `RenderSummary`, `ParseCucumber`, `SampleApproved`, `Tally`, `RenderTally`, `ParseChecklistClasses`, `ExtractBlindspotSlugs`, `ExtractRegisteredSlugs`, `DiffSlugs`) match between definition and use.
