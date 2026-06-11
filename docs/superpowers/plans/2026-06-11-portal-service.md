# portal-service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `portal-service` — a stateless post-login broker that gates NATS-credential issuance on a directory readiness check, routes to the user's home-site `auth-service`, redirects wrong-site logins home, and returns `{natsJwt, natsUrl, user}` — then repoint chat-frontend at it.

**Architecture:** New flat Go service at the repo root (Gin HTTP, per-site single pod). It verifies the browser's SSO token via `pkg/oidc` (JWKS only — NOT an OIDC client), looks the account up in an in-memory **bulk-loaded** directory map (Mongo is read only at startup and on `POST /admin/cache/reload`; a map miss is terminal `account_not_ready`), optionally returns `{redirectTo}` when the request `Origin` is a known foreign frontend, and otherwise forwards `{ssoToken, natsPublicKey}` to the home site's `auth-service /auth` (Resty), relaying the response plus the resolved `natsUrl`. auth-service code is untouched; its local compose stops publishing a host port (lockdown mirror). chat-frontend keeps Keycloak login + nkey generation and only repoints its two credential calls.

**Tech Stack:** Go 1.25, Gin, Resty, `caarlos0/env/v11`, `go.mongodb.org/mongo-driver/v2`, `pkg/oidc` (go-oidc), `pkg/errcode` + `errhttp`, `go.uber.org/mock`, testify, `pkg/testutil` (testcontainers Mongo). Frontend: React + vitest.

**Spec:** `docs/superpowers/specs/2026-06-09-portal-service-design.md` (approved).

---

## Context you need before starting

- **Branch:** work on `claude/dreamy-bell-cqdibt`. Never push elsewhere.
- **Commands are Makefile-wrapped** (run from repo root): `make test SERVICE=portal-service`, `make test-integration SERVICE=portal-service`, `make generate SERVICE=portal-service`, `make build SERVICE=portal-service`, `make lint`, `make fmt`, `make sast`. A pre-commit hook runs lint + tests — if a commit fails, fix and retry; do not `--no-verify`.
- **TDD is mandatory**: every task below is Red → Green → Commit. If a "verify it fails" step passes unexpectedly, STOP — the test is wrong.
- **Reference implementations** (read before coding if anything is unclear): `auth-service/main.go`, `auth-service/handler.go`, `auth-service/middleware.go`, `auth-service/handler_test.go`. The portal deliberately mirrors their style.
- **No new go.mod dependencies.** Everything needed (gin, resty, env/v11, mongo-driver, go-oidc via `pkg/oidc`, mock, testify, testcontainers via `pkg/testutil`) is already a direct dependency.
- All `errcode` usage is Tier 1 + the one-line `errhttp.Write` adapter, plus exactly one sanctioned Tier-3 use: `errcode.Parse` for relaying upstream auth-service envelopes. Never `slog.Error` an error you also return/write — `errhttp.Write` classifies-and-logs once.
- **Reuse before writing:** lean on existing helpers everywhere — `pkg/oidc.Validator` (token verify) + `Claims.Account()` (Task 1b), `pkg/restyutil.New` (Resty with OTel + slog), `pkg/errcode/errtest` (`AssertCode`/`AssertReason` in tests), `natsutil.RequestIDHeader`/`WithRequestID`/`RequestIDFromContext`, `idgen.ResolveRequestID`/`IsValidUUID`, `mongoutil.Connect`/`Disconnect`, `shutdown.Wait`, `testutil.MongoDB`/`RunTests`. Do not re-implement any of these.
- **Comment style:** every code comment is 1–2 precise lines max. No paragraph comments.

### Final file map

```
pkg/errcode/codes_portal.go            (create)  PortalAccountNotReady reason
pkg/errcode/codes_test.go              (modify)  register the new reason
pkg/oidc/oidc.go                       (modify)  Claims.Account() — shared derivation
pkg/oidc/oidc_test.go                  (modify)  Account() fallback-chain test
auth-service/handler.go                (modify)  use Claims.Account() (no behavior change)
portal-service/store.go                (create)  directoryRecord + DirectoryStore + go:generate
portal-service/mock_store_test.go      (generated)
portal-service/dirmap.go               (create)  atomic bulk-loaded map
portal-service/dirmap_test.go          (create)
portal-service/forwarder.go            (create)  AuthForwarder + Resty impl
portal-service/forwarder_test.go       (create)
portal-service/middleware.go           (create)  requestID + accessLog + CORS
portal-service/middleware_test.go      (create)
portal-service/handler.go              (create)  PortalHandler (session, reload, health)
portal-service/handler_test.go         (create)
portal-service/routes.go               (create)
portal-service/main.go                 (create)  config + wiring + shutdown
portal-service/main_test.go            (create)  validateConfig tests
portal-service/store_mongo.go          (create)  Mongo LoadAll impl
portal-service/integration_test.go     (create)  //go:build integration
portal-service/deploy/Dockerfile       (create)
portal-service/deploy/docker-compose.yml (create)
portal-service/deploy/azure-pipelines.yml (create)
docker-local/compose.services.yaml     (modify)  include portal-service
auth-service/deploy/docker-compose.yml (modify)  remove host port publish (lockdown)
docs/client-api.md                     (modify)  §2.2 → portal endpoint; §6 catalog
chat-frontend/src/lib/runtimeConfig.js (modify)  PORTAL_URL replaces AUTH_URL/NATS_URL
chat-frontend/src/lib/portalRedirect.js       (create)  shared redirectTo navigation
chat-frontend/src/lib/portalRedirect.test.js  (create)
chat-frontend/vite.config.js           (modify)  drop dead /auth dev proxy
chat-frontend/src/context/NatsContext/NatsContext.jsx        (modify)
chat-frontend/src/context/NatsContext/NatsContext.test.jsx   (modify)
chat-frontend/src/context/NatsContext/useJwtRefresh.js       (modify)
chat-frontend/src/context/NatsContext/useJwtRefresh.test.js  (modify)
chat-frontend/src/api/_transport/asyncJob.ts                 (modify)  REASON_COPY entry
chat-frontend/CLAUDE.md                (modify)  reason catalog line
chat-frontend/deploy/config.js.template      (modify)
chat-frontend/deploy/30-render-config.sh     (modify)
chat-frontend/deploy/docker-compose.yml      (modify)
chat-frontend/smoke-test.mjs           (modify)
chat-frontend/scripts/liveStack.smoke.mjs    (modify)
```

---

### Task 1: `account_not_ready` reason in pkg/errcode

**Files:**
- Create: `pkg/errcode/codes_portal.go`
- Modify: `pkg/errcode/codes_test.go`

- [ ] **Step 1: Register the reason in the test list first (Red)**

In `pkg/errcode/codes_test.go`, the `allReasons` slice currently ends:

```go
	AuthTokenExpired, AuthInvalidToken, AuthInvalidRequest, AuthInvalidNKey, AuthMissingFields,
	RequestIDRequired,
```

Change to:

```go
	AuthTokenExpired, AuthInvalidToken, AuthInvalidRequest, AuthInvalidNKey, AuthMissingFields,
	PortalAccountNotReady,
	RequestIDRequired,
```

- [ ] **Step 2: Verify it fails to compile**

Run: `make test SERVICE=pkg/errcode`
Expected: FAIL — `undefined: PortalAccountNotReady`

- [ ] **Step 3: Create the codes file (Green)**

Create `pkg/errcode/codes_portal.go`:

```go
package errcode

// Reasons emitted by portal-service.
const (
	// PortalAccountNotReady: no ready directory record (no record, empty
	// siteId, or unconfigured siteId). Server log carries the deny_case.
	PortalAccountNotReady Reason = "account_not_ready"
)
```

- [ ] **Step 4: Run the package tests**

Run: `make test SERVICE=pkg/errcode`
Expected: PASS (including `TestReasons_SnakeCase` and `TestReasons_Unique`)

- [ ] **Step 5: Commit**

```bash
git add pkg/errcode/codes_portal.go pkg/errcode/codes_test.go
git commit -m "feat(errcode): add account_not_ready reason for portal-service"
```

---

### Task 1b: shared account derivation — `Claims.Account()` in pkg/oidc

**Files:**
- Modify: `pkg/oidc/oidc.go`
- Modify: `pkg/oidc/oidc_test.go`
- Modify: `auth-service/handler.go:152-155`

The portal must derive the account from claims with the **exact** chain auth-service uses (`preferred_username` → `name`), or it gates on a different account than auth-service mints for. A copy in two services drifts; the chain belongs on `Claims` itself, used by both.

- [ ] **Step 1: Write the failing test**

Append to `pkg/oidc/oidc_test.go`:

```go
func TestClaims_Account(t *testing.T) {
	cases := []struct {
		name, preferred, fallback, want string
	}{
		{"preferred_username wins", "alice", "Alice Wang", "alice"},
		{"falls back to name", "", "Alice Wang", "Alice Wang"},
		{"both empty yields empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Claims{PreferredUsername: tc.preferred, Name: tc.fallback}
			assert.Equal(t, tc.want, c.Account())
		})
	}
}
```

- [ ] **Step 2: Verify it fails**

Run: `make test SERVICE=pkg/oidc`
Expected: FAIL — `c.Account undefined`

- [ ] **Step 3: Implement (Green)** — append to `pkg/oidc/oidc.go` after the `Claims` struct:

```go
// Account derives the chat account name: preferred_username, else name.
// Callers must reject a blank result before minting or gating.
func (c Claims) Account() string {
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	return c.Name
}
```

- [ ] **Step 4: Run the package tests**

Run: `make test SERVICE=pkg/oidc`
Expected: PASS

- [ ] **Step 5: Point auth-service at the helper** (no behavior change; its existing tests are the guard)

In `auth-service/handler.go`, replace lines 152–155:

```go
	account := claims.PreferredUsername
	if account == "" {
		account = claims.Name
	}
```

with:

```go
	account := claims.Account()
```

- [ ] **Step 6: Run the auth-service tests**

Run: `make test SERVICE=auth-service`
Expected: PASS (all existing tests — `TestHandleAuth_ValidToken` etc. pin the behavior)

- [ ] **Step 7: Commit**

```bash
git add pkg/oidc/oidc.go pkg/oidc/oidc_test.go auth-service/handler.go
git commit -m "refactor(oidc): shared Claims.Account derivation used by auth-service"
```

---

### Task 2: directory record, store interface, and the in-memory dirMap

**Files:**
- Create: `portal-service/store.go`
- Create: `portal-service/dirmap.go`
- Test: `portal-service/dirmap_test.go`
- Generated: `portal-service/mock_store_test.go`

The map is a **bulk load, not a lazy cache**: `Reload` calls `DirectoryStore.LoadAll` and atomically swaps the whole map; `Lookup` never touches Mongo; a reload failure keeps the old map serving.

- [ ] **Step 1: Create `portal-service/store.go`** (types must exist for the test file to reference; the *behavior* under test — dirMap — does not exist yet)

```go
package main

import "context"

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

// directoryRecord is one account→home-site row; employeeId is informational only.
type directoryRecord struct {
	Account    string `json:"account" bson:"_id"`
	EmployeeID string `json:"employeeId" bson:"employeeId"`
	SiteID     string `json:"siteId" bson:"siteId"`
}

// DirectoryStore bulk-loads the directory; read only at startup and on reload,
// never on the request path.
type DirectoryStore interface {
	LoadAll(ctx context.Context) ([]directoryRecord, error)
}
```

- [ ] **Step 2: Generate the mock**

Run: `make generate SERVICE=portal-service`
Expected: `portal-service/mock_store_test.go` created, containing `MockDirectoryStore` with a `LoadAll` method.

- [ ] **Step 3: Write the failing dirMap tests**

Create `portal-service/dirmap_test.go`:

```go
package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestDirMap_LookupOnFreshMapMisses(t *testing.T) {
	d := newDirMap()
	_, ok := d.Lookup("alice")
	assert.False(t, ok)
	assert.Equal(t, 0, d.Len())
}

func TestDirMap_ReloadPopulatesAndLookupHits(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
		{Account: "alice", EmployeeID: "E001", SiteID: "site-a"},
		{Account: "bob", EmployeeID: "E002", SiteID: "site-b"},
	}, nil)

	d := newDirMap()
	n, err := d.Reload(context.Background(), store)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, 2, d.Len())

	rec, ok := d.Lookup("alice")
	require.True(t, ok)
	assert.Equal(t, "site-a", rec.SiteID)
	assert.Equal(t, "E001", rec.EmployeeID)

	_, ok = d.Lookup("charlie")
	assert.False(t, ok)
}

func TestDirMap_ReloadFailureKeepsOldMap(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	gomock.InOrder(
		store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
			{Account: "alice", SiteID: "site-a"},
		}, nil),
		store.EXPECT().LoadAll(gomock.Any()).Return(nil, fmt.Errorf("mongo down")),
	)

	d := newDirMap()
	_, err := d.Reload(context.Background(), store)
	require.NoError(t, err)

	_, err = d.Reload(context.Background(), store)
	require.Error(t, err)

	// Old map still serves.
	rec, ok := d.Lookup("alice")
	require.True(t, ok)
	assert.Equal(t, "site-a", rec.SiteID)
}

func TestDirMap_ReloadReplacesRemovedAccounts(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	gomock.InOrder(
		store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
			{Account: "alice", SiteID: "site-a"},
			{Account: "bob", SiteID: "site-b"},
		}, nil),
		store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
			{Account: "alice", SiteID: "site-a"},
		}, nil),
	)

	d := newDirMap()
	_, err := d.Reload(context.Background(), store)
	require.NoError(t, err)
	_, err = d.Reload(context.Background(), store)
	require.NoError(t, err)

	_, ok := d.Lookup("bob") // removed by the second sync
	assert.False(t, ok)
	assert.Equal(t, 1, d.Len())
}

// Concurrent lookups during a swap must be race-free (validated by -race,
// which `make test` always enables).
func TestDirMap_ConcurrentLookupAndReload(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
		{Account: "alice", SiteID: "site-a"},
	}, nil).AnyTimes()

	d := newDirMap()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = d.Reload(context.Background(), store) }()
		go func() { defer wg.Done(); _, _ = d.Lookup("alice") }()
	}
	wg.Wait()
}
```

- [ ] **Step 4: Verify the tests fail**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `undefined: newDirMap`

- [ ] **Step 5: Implement `portal-service/dirmap.go` (Green)**

```go
package main

import (
	"context"
	"fmt"
	"sync/atomic"
)

// dirMap is the bulk-loaded account directory, atomically swapped on Reload.
// A lookup miss is terminal (account_not_ready) until the next reload.
type dirMap struct {
	recs atomic.Pointer[map[string]directoryRecord]
}

func newDirMap() *dirMap {
	d := &dirMap{}
	empty := map[string]directoryRecord{}
	d.recs.Store(&empty)
	return d
}

func (d *dirMap) Lookup(account string) (directoryRecord, bool) {
	m := *d.recs.Load()
	rec, ok := m[account]
	return rec, ok
}

func (d *dirMap) Len() int {
	return len(*d.recs.Load())
}

// Reload bulk-loads the directory and atomically swaps the map. On failure
// the previous map keeps serving.
func (d *dirMap) Reload(ctx context.Context, store DirectoryStore) (int, error) {
	recs, err := store.LoadAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("load directory: %w", err)
	}
	m := make(map[string]directoryRecord, len(recs))
	for _, r := range recs {
		m[r.Account] = r
	}
	d.recs.Store(&m)
	return len(m), nil
}
```

- [ ] **Step 6: Run the tests**

Run: `make test SERVICE=portal-service`
Expected: PASS (5 tests)

- [ ] **Step 7: Commit**

```bash
git add portal-service/store.go portal-service/dirmap.go portal-service/dirmap_test.go portal-service/mock_store_test.go
git commit -m "feat(portal-service): directory store interface and atomic bulk-loaded dirMap"
```

---

### Task 3: Mongo store implementation (+ its integration test)

**Files:**
- Create: `portal-service/store_mongo.go`
- Create: `portal-service/integration_test.go` (TestMain + store test; the end-to-end test is added in Task 9)

- [ ] **Step 1: Write the failing integration test**

Create `portal-service/integration_test.go`:

```go
//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTests(m) }

func TestMongoDirectoryStore_LoadAll(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	ctx := context.Background()

	_, err := db.Collection("directory").InsertMany(ctx, []any{
		bson.D{{Key: "_id", Value: "alice"}, {Key: "employeeId", Value: "E001"}, {Key: "siteId", Value: "site-a"}},
		bson.D{{Key: "_id", Value: "bob"}, {Key: "employeeId", Value: "E002"}, {Key: "siteId", Value: ""}},
	})
	require.NoError(t, err)

	store := newMongoDirectoryStore(db, "directory")
	recs, err := store.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, recs, 2)

	byAccount := map[string]directoryRecord{}
	for _, r := range recs {
		byAccount[r.Account] = r
	}
	assert.Equal(t, directoryRecord{Account: "alice", EmployeeID: "E001", SiteID: "site-a"}, byAccount["alice"])
	assert.Equal(t, "", byAccount["bob"].SiteID) // not-ready rows load too; the gate decides
}

func TestMongoDirectoryStore_LoadAll_EmptyCollection(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	store := newMongoDirectoryStore(db, "directory")
	recs, err := store.LoadAll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, recs)
}
```

- [ ] **Step 2: Verify it fails**

Run: `make test-integration SERVICE=portal-service`
Expected: FAIL — `undefined: newMongoDirectoryStore` (compile error under the integration tag)

- [ ] **Step 3: Implement `portal-service/store_mongo.go` (Green)**

```go
package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type mongoDirectoryStore struct {
	coll *mongo.Collection
}

func newMongoDirectoryStore(db *mongo.Database, collection string) *mongoDirectoryStore {
	return &mongoDirectoryStore{coll: db.Collection(collection)}
}

func (s *mongoDirectoryStore) LoadAll(ctx context.Context) ([]directoryRecord, error) {
	cur, err := s.coll.Find(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("find directory records: %w", err)
	}
	var recs []directoryRecord
	if err := cur.All(ctx, &recs); err != nil {
		return nil, fmt.Errorf("decode directory records: %w", err)
	}
	return recs, nil
}
```

- [ ] **Step 4: Run the integration tests** (requires Docker)

Run: `make test-integration SERVICE=portal-service`
Expected: PASS (2 tests). If Docker is unavailable in your environment, run `go vet ./portal-service/` + `make test SERVICE=portal-service` to confirm compilation, note the skip in the commit body, and let CI run them.

- [ ] **Step 5: Commit**

```bash
git add portal-service/store_mongo.go portal-service/integration_test.go
git commit -m "feat(portal-service): Mongo directory store with bulk LoadAll"
```

---

### Task 4: AuthForwarder (Resty)

**Files:**
- Create: `portal-service/forwarder.go`
- Test: `portal-service/forwarder_test.go`

The forwarder is a dumb pipe: POST raw bytes to `<authURL>/auth`, propagate `X-Request-ID` from ctx, return status + body. It never interprets the payload. The client comes from `pkg/restyutil.New` — that's the repo's Resty builder (OTel transport + slog request/response logging with request-ID, used by search-service); base URL stays empty because each call targets a per-site absolute URL.

- [ ] **Step 1: Write the failing tests**

Create `portal-service/forwarder_test.go`:

```go
package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestRestyForwarder_PostsBodyAndPropagatesRequestID(t *testing.T) {
	var gotPath, gotContentType, gotRequestID string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotRequestID = r.Header.Get(natsutil.RequestIDHeader)
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"natsJwt":"jwt-x","user":{"account":"alice"}}`))
	}))
	t.Cleanup(upstream.Close)

	ctx := natsutil.WithRequestID(context.Background(), "01970a4f-8c2d-7c9a-abcd-e0123456789f")
	f := newRestyForwarder(2*time.Second, false)

	status, body, err := f.Forward(ctx, upstream.URL, []byte(`{"ssoToken":"tok","natsPublicKey":"U..."}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"natsJwt":"jwt-x","user":{"account":"alice"}}`, string(body))
	assert.Equal(t, "/auth", gotPath)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "01970a4f-8c2d-7c9a-abcd-e0123456789f", gotRequestID)
	assert.JSONEq(t, `{"ssoToken":"tok","natsPublicKey":"U..."}`, string(gotBody))
}

func TestRestyForwarder_TrimsTrailingSlash(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	f := newRestyForwarder(2*time.Second, false)
	_, _, err := f.Forward(context.Background(), upstream.URL+"/", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "/auth", gotPath)
}

func TestRestyForwarder_ReturnsUpstreamErrorStatusAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid SSO token","code":"unauthenticated","reason":"invalid_sso_token"}`))
	}))
	t.Cleanup(upstream.Close)

	f := newRestyForwarder(2*time.Second, false)
	status, body, err := f.Forward(context.Background(), upstream.URL, []byte(`{}`))
	require.NoError(t, err) // non-2xx is NOT a transport error
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Contains(t, string(body), "invalid_sso_token")
}

func TestRestyForwarder_TransportErrorIsWrapped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstream.Close() // dead endpoint

	f := newRestyForwarder(500*time.Millisecond, false)
	_, _, err := f.Forward(context.Background(), upstream.URL, []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "post to auth-service")
}
```

- [ ] **Step 2: Verify the tests fail**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `undefined: newRestyForwarder`

- [ ] **Step 3: Implement `portal-service/forwarder.go` (Green)**

```go
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/restyutil"
)

// AuthForwarder posts an auth request body to a site auth-service and returns
// the upstream status and raw body. It never interprets the payload.
type AuthForwarder interface {
	Forward(ctx context.Context, authURL string, body []byte) (status int, respBody []byte, err error)
}

type restyForwarder struct {
	client *resty.Client
}

// Empty base URL: Forward targets a per-site absolute URL on every call.
func newRestyForwarder(timeout time.Duration, tlsSkipVerify bool) *restyForwarder {
	opts := []restyutil.Option{restyutil.WithTimeout(timeout)}
	if tlsSkipVerify {
		// #nosec G402 -- InsecureSkipVerify is opt-in via TLS_SKIP_VERIFY for dev environments
		opts = append(opts, restyutil.WithTransport(&http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec
		}))
	}
	return &restyForwarder{client: restyutil.New("", opts...)}
}

func (f *restyForwarder) Forward(ctx context.Context, authURL string, body []byte) (int, []byte, error) {
	resp, err := f.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader(natsutil.RequestIDHeader, natsutil.RequestIDFromContext(ctx)).
		SetBody(body).
		Post(strings.TrimRight(authURL, "/") + "/auth")
	if err != nil {
		return 0, nil, fmt.Errorf("post to auth-service: %w", err)
	}
	return resp.StatusCode(), resp.Body(), nil
}
```

- [ ] **Step 4: Run the tests**

Run: `make test SERVICE=portal-service`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add portal-service/forwarder.go portal-service/forwarder_test.go
git commit -m "feat(portal-service): Resty auth forwarder with request-ID propagation"
```

---

### Task 5: middleware (request ID, access log, allowlist CORS)

**Files:**
- Create: `portal-service/middleware.go`
- Test: `portal-service/middleware_test.go`

`requestIDMiddleware` and `accessLogMiddleware` are copied from `auth-service/middleware.go` (flat `package main` services cannot import each other — copying is the repo convention). CORS differs: the portal allows only configured frontend origins, echoing the origin back (allowlist ⇒ no wildcard). `normalizeOrigin` is defined here in `middleware.go` — CORS is its first consumer; the handler (Task 6) reuses it.

- [ ] **Step 1: Write the failing tests**

Create `portal-service/middleware_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
)

func newMiddlewareRouter(allowed map[string]struct{}) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestIDMiddleware())
	r.Use(corsMiddleware(allowed))
	r.POST("/probe", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"rid": c.GetString("request_id")}) })
	return r
}

func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"plain origin", "https://chat-a.example.com", "https://chat-a.example.com", false},
		{"uppercase host lowered", "HTTPS://Chat-A.Example.com", "https://chat-a.example.com", false},
		{"port kept", "http://localhost:5173", "http://localhost:5173", false},
		{"path stripped", "https://chat-a.example.com/rooms/1", "https://chat-a.example.com", false},
		{"trailing slash stripped", "https://chat-a.example.com/", "https://chat-a.example.com", false},
		{"whitespace trimmed", "  https://chat-a.example.com ", "https://chat-a.example.com", false},
		{"missing scheme rejected", "chat-a.example.com", "", true},
		{"empty rejected", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeOrigin(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRequestIDMiddleware_MintsWhenMissing(t *testing.T) {
	r := newMiddlewareRouter(nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	r.ServeHTTP(w, req)

	got := w.Header().Get(natsutil.RequestIDHeader)
	assert.True(t, idgen.IsValidUUID(got), "minted id %q must be a hyphenated UUID", got)
}

func TestRequestIDMiddleware_PassesValidInboundThrough(t *testing.T) {
	r := newMiddlewareRouter(nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set(natsutil.RequestIDHeader, "01970a4f-8c2d-7c9a-abcd-e0123456789f")
	r.ServeHTTP(w, req)

	assert.Equal(t, "01970a4f-8c2d-7c9a-abcd-e0123456789f", w.Header().Get(natsutil.RequestIDHeader))
}

func TestCORSMiddleware(t *testing.T) {
	allowed := map[string]struct{}{"https://chat-a.example.com": {}}

	t.Run("allowed origin echoed", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("Origin", "https://chat-a.example.com")
		r.ServeHTTP(w, req)
		assert.Equal(t, "https://chat-a.example.com", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "Origin", w.Header().Get("Vary"))
	})

	t.Run("unknown origin gets no CORS headers", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		r.ServeHTTP(w, req)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("preflight short-circuits with 204", func(t *testing.T) {
		r := newMiddlewareRouter(allowed)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
		req.Header.Set("Origin", "https://chat-a.example.com")
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, "https://chat-a.example.com", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "POST")
	})
}
```

- [ ] **Step 2: Verify the tests fail**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `undefined: requestIDMiddleware` (and `corsMiddleware`, `normalizeOrigin`)

- [ ] **Step 3: Implement `portal-service/middleware.go` (Green)**

```go
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// requestIDMiddleware resolves X-Request-ID via idgen.ResolveRequestID:
// valid inbound passes through, otherwise a fresh UUIDv7 is minted.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		inbound := c.GetHeader(natsutil.RequestIDHeader)
		id, replaced := idgen.ResolveRequestID(inbound)
		c.Set("request_id", id)
		c.Request = c.Request.WithContext(natsutil.WithRequestID(c.Request.Context(), id))
		c.Header(natsutil.RequestIDHeader, id)
		if replaced {
			slog.WarnContext(c.Request.Context(), "minted request_id (inbound invalid)", "inbound", inbound, "path", c.Request.URL.Path)
		}
		c.Next()
	}
}

// normalizeOrigin reduces a URL/origin to lowercase scheme://host[:port].
func normalizeOrigin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("parse origin %q: %w", s, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("origin %q must include scheme and host", s)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// corsMiddleware allows only the configured frontend origins (the token rides
// in the body, not a cookie, so no credentialed-CORS handling is needed).
func corsMiddleware(allowed map[string]struct{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			if norm, err := normalizeOrigin(origin); err == nil {
				if _, ok := allowed[norm]; ok {
					c.Header("Access-Control-Allow-Origin", origin)
					c.Header("Vary", "Origin")
					c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
					c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
					c.Header("Access-Control-Max-Age", "300")
				}
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// accessLogMiddleware logs method, path, status, and latency for each request.
func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		slog.Info("request",
			"request_id", c.GetString("request_id"),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `make test SERVICE=portal-service`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add portal-service/middleware.go portal-service/middleware_test.go
git commit -m "feat(portal-service): request-id, access-log, and allowlist CORS middleware"
```

---

### Task 6: PortalHandler + routes (the core)

**Files:**
- Create: `portal-service/handler.go`
- Create: `portal-service/routes.go`
- Test: `portal-service/handler_test.go`

Handler order (spec §6.1): bind/validate → verify token (`pkg/oidc`) → derive account via `claims.Account()` (Task 1b — the same code path auth-service now uses) → dirMap gate → cross-site redirect → forward → relay. Reuses: `errcode.AuthMissingFields`/`AuthInvalidNKey`/`AuthTokenExpired`/`AuthInvalidToken` (same wire reasons as auth-service), `errcode.Parse` for upstream relay, `errtest` assertions in tests.

- [ ] **Step 1: Write the failing tests**

Create `portal-service/handler_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errtest"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// fakeVerifier implements TokenVerifier (mirrors auth-service's fakeValidator).
type fakeVerifier struct {
	preferredUsername string
	name              string
	expired           bool
	invalid           bool
}

func (f *fakeVerifier) Validate(_ context.Context, _ string) (pkgoidc.Claims, error) {
	if f.expired {
		return pkgoidc.Claims{}, pkgoidc.ErrTokenExpired
	}
	if f.invalid {
		return pkgoidc.Claims{}, fmt.Errorf("oidc token verification failed: bad signature")
	}
	return pkgoidc.Claims{PreferredUsername: f.preferredUsername, Name: f.name}, nil
}

// fakeForwarder captures the forward call and returns a canned upstream reply.
type fakeForwarder struct {
	status  int
	body    []byte
	err     error
	called  bool
	gotURL  string
	gotBody []byte
}

func (f *fakeForwarder) Forward(_ context.Context, authURL string, body []byte) (int, []byte, error) {
	f.called = true
	f.gotURL = authURL
	f.gotBody = body
	if f.err != nil {
		return 0, nil, f.err
	}
	return f.status, f.body, nil
}

func mustUserNKey(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateUser()
	require.NoError(t, err, "create user key")
	pub, err := kp.PublicKey()
	require.NoError(t, err, "public key")
	return pub
}

const upstreamOK = `{"natsJwt":"jwt-from-upstream","user":{"email":"alice@example.com","account":"alice","employeeId":"E001","engName":"Alice","chineseName":"","deptName":"Eng","deptId":"ENG"}}`

// testParams returns a ready-to-mutate params set: alice→site-a, all maps populated.
func testParams(t *testing.T) PortalHandlerParams {
	t.Helper()
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
		{Account: "alice", EmployeeID: "E001", SiteID: "site-a"},
		{Account: "carol", EmployeeID: "E003", SiteID: ""},        // not ready: empty siteId
		{Account: "dave", EmployeeID: "E004", SiteID: "site-zzz"}, // not ready: unconfigured site
	}, nil).AnyTimes()
	dir := newDirMap()
	_, err := dir.Reload(context.Background(), store)
	require.NoError(t, err)

	return PortalHandlerParams{
		Verifier:  &fakeVerifier{preferredUsername: "alice"},
		Store:     store,
		Dir:       dir,
		Forwarder: &fakeForwarder{status: http.StatusOK, body: []byte(upstreamOK)},
		SiteAuthURLs: map[string]string{
			"site-a": "http://auth-a:8080",
			"site-b": "http://auth-b:8080",
		},
		SiteNATSURLs: map[string]string{
			"site-a": "wss://nats-a:9222",
			"site-b": "wss://nats-b:9222",
		},
		SiteFrontendURLs: map[string]string{
			"site-a": "https://chat-a.example.com",
			"site-b": "https://chat-b.example.com",
		},
		AdminToken: "test-admin-token",
		DevMode:    false,
	}
}

func newTestRouter(t *testing.T, p PortalHandlerParams) *gin.Engine {
	t.Helper()
	h, err := NewPortalHandler(p)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

func postSession(r *gin.Engine, body, origin string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/nats-jwt", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestHandleSessionNATSJWT_Success(t *testing.T) {
	p := testParams(t)
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, p)
	userPub := mustUserNKey(t)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+userPub+`"}`, "https://chat-a.example.com")
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		NATSJWT string          `json:"natsJwt"`
		NATSURL string          `json:"natsUrl"`
		User    json.RawMessage `json:"user"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "jwt-from-upstream", resp.NATSJWT)
	assert.Equal(t, "wss://nats-a:9222", resp.NATSURL)
	assert.Contains(t, string(resp.User), `"account":"alice"`)

	// Forwarded to the home site's auth-service with the prod body shape.
	assert.Equal(t, "http://auth-a:8080", fwd.gotURL)
	assert.JSONEq(t, `{"ssoToken":"tok","natsPublicKey":"`+userPub+`"}`, string(fwd.gotBody))
}

func TestHandleSessionNATSJWT_AccountFallsBackToName(t *testing.T) {
	p := testParams(t)
	p.Verifier = &fakeVerifier{preferredUsername: "", name: "alice"}
	r := newTestRouter(t, p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleSessionNATSJWT_BlankAccountRejected(t *testing.T) {
	p := testParams(t)
	p.Verifier = &fakeVerifier{}
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthInvalidToken)
	assert.False(t, fwd.called)
}

func TestHandleSessionNATSJWT_TokenErrors(t *testing.T) {
	cases := []struct {
		name       string
		verifier   *fakeVerifier
		wantReason errcode.Reason
	}{
		{"expired token", &fakeVerifier{expired: true}, errcode.AuthTokenExpired},
		{"invalid token", &fakeVerifier{invalid: true}, errcode.AuthInvalidToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			p.Verifier = tc.verifier
			r := newTestRouter(t, p)
			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeUnauthenticated)
			errtest.AssertReason(t, w.Body.Bytes(), tc.wantReason)
		})
	}
}

func TestHandleSessionNATSJWT_BadInput(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantReason errcode.Reason
	}{
		{"missing ssoToken", `{"natsPublicKey":"UWHATEVER"}`, errcode.AuthMissingFields},
		{"missing natsPublicKey", `{"ssoToken":"tok"}`, errcode.AuthMissingFields},
		{"empty body", `{}`, errcode.AuthMissingFields},
		{"invalid nkey", `{"ssoToken":"tok","natsPublicKey":"NOT-A-KEY"}`, errcode.AuthInvalidNKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter(t, testParams(t))
			w := postSession(r, tc.body, "")
			assert.Equal(t, http.StatusBadRequest, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
			errtest.AssertReason(t, w.Body.Bytes(), tc.wantReason)
		})
	}
}

func TestHandleSessionNATSJWT_NotReady(t *testing.T) {
	cases := []struct {
		name    string
		account string // which account the verifier yields
	}{
		{"no directory record", "mallory"},
		{"empty siteId", "carol"},
		{"siteId not configured", "dave"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			p.Verifier = &fakeVerifier{preferredUsername: tc.account}
			fwd := p.Forwarder.(*fakeForwarder)
			r := newTestRouter(t, p)

			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
			assert.Equal(t, http.StatusForbidden, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
			errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotReady)
			assert.False(t, fwd.called, "gate must stop the request before any forward")
		})
	}
}

func TestHandleSessionNATSJWT_CrossSiteRedirect(t *testing.T) {
	p := testParams(t) // alice's home is site-a
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "https://chat-b.example.com")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"redirectTo":"https://chat-a.example.com"}`, w.Body.String())
	assert.False(t, fwd.called, "redirect must not mint")
}

func TestHandleSessionNATSJWT_OriginVariantsProceed(t *testing.T) {
	cases := []struct{ name, origin string }{
		{"same-site origin", "https://chat-a.example.com"},
		{"unknown origin", "https://not-a-frontend.example.com"},
		{"no origin", ""},
		{"malformed origin", "not a url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			fwd := p.Forwarder.(*fakeForwarder)
			r := newTestRouter(t, p)
			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, tc.origin)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.True(t, fwd.called)
			assert.NotContains(t, w.Body.String(), "redirectTo")
		})
	}
}

func TestHandleSessionNATSJWT_UpstreamErrorEnvelopeRelayed(t *testing.T) {
	p := testParams(t)
	p.Forwarder = &fakeForwarder{
		status: http.StatusUnauthorized,
		body:   []byte(`{"error":"SSO token has expired, please re-login","code":"unauthenticated","reason":"sso_token_expired"}`),
	}
	r := newTestRouter(t, p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeUnauthenticated)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthTokenExpired)
}

func TestHandleSessionNATSJWT_UpstreamFailuresCollapseToInternal(t *testing.T) {
	cases := []struct {
		name string
		fwd  *fakeForwarder
	}{
		{"transport error", &fakeForwarder{err: fmt.Errorf("dial tcp: connection refused")}},
		{"non-envelope error body", &fakeForwarder{status: http.StatusBadGateway, body: []byte("oops")}},
		{"200 missing natsJwt", &fakeForwarder{status: http.StatusOK, body: []byte(`{"user":{}}`)}},
		{"200 garbage body", &fakeForwarder{status: http.StatusOK, body: []byte("not json")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			p.Forwarder = tc.fwd
			r := newTestRouter(t, p)
			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
			require.Equal(t, http.StatusInternalServerError, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeInternal)
			assert.NotContains(t, w.Body.String(), "connection refused", "cause must never reach the client")
		})
	}
}

func TestHandleSessionNATSJWT_DevMode(t *testing.T) {
	p := testParams(t)
	p.DevMode = true
	p.Verifier = nil // dev mode never verifies
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, p)
	userPub := mustUserNKey(t)

	w := postSession(r, `{"account":"alice","natsPublicKey":"`+userPub+`"}`, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"natsUrl":"wss://nats-a:9222"`)
	assert.JSONEq(t, `{"account":"alice","natsPublicKey":"`+userPub+`"}`, string(fwd.gotBody))
}

func TestHandleSessionNATSJWT_DevMode_BadInput(t *testing.T) {
	p := testParams(t)
	p.DevMode = true
	p.Verifier = nil
	r := newTestRouter(t, p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusBadRequest, w.Code, "dev mode requires the account-shape body")
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
}

func TestHandleSessionNATSJWT_ProdRejectsAccountOnlyBody(t *testing.T) {
	r := newTestRouter(t, testParams(t))
	w := postSession(r, `{"account":"alice","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
}

func TestHandleCacheReload(t *testing.T) {
	reload := func(r *gin.Engine, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/cache/reload", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("wrong token rejected", func(t *testing.T) {
		r := newTestRouter(t, testParams(t))
		w := reload(r, "wrong")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("missing token rejected", func(t *testing.T) {
		r := newTestRouter(t, testParams(t))
		w := reload(r, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("reload swaps the map", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockDirectoryStore(ctrl)
		gomock.InOrder(
			store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{}, nil),
			store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
				{Account: "alice", SiteID: "site-a"},
			}, nil),
		)
		p := testParams(t)
		p.Store = store
		p.Dir = newDirMap()
		_, err := p.Dir.Reload(context.Background(), store) // initial empty load
		require.NoError(t, err)
		r := newTestRouter(t, p)

		// Before reload: alice unknown → 403.
		w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
		require.Equal(t, http.StatusForbidden, w.Code)

		w = reload(r, "test-admin-token")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"records":1`)

		// After reload: alice resolves.
		w = postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("reload failure returns 500 and keeps old map", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockDirectoryStore(ctrl)
		gomock.InOrder(
			store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
				{Account: "alice", SiteID: "site-a"},
			}, nil),
			store.EXPECT().LoadAll(gomock.Any()).Return(nil, fmt.Errorf("mongo down")),
		)
		p := testParams(t)
		p.Store = store
		p.Dir = newDirMap()
		_, err := p.Dir.Reload(context.Background(), store)
		require.NoError(t, err)
		r := newTestRouter(t, p)

		w := reload(r, "test-admin-token")
		require.Equal(t, http.StatusInternalServerError, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeInternal)

		// Old map still serves alice.
		w = postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestNewPortalHandler_RejectsBadFrontendURL(t *testing.T) {
	p := testParams(t)
	p.SiteFrontendURLs = map[string]string{"site-a": "no-scheme.example.com"}
	_, err := NewPortalHandler(p)
	require.Error(t, err)
}

func TestHandleHealth(t *testing.T) {
	r := newTestRouter(t, testParams(t))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}
```

- [ ] **Step 2: Verify the tests fail**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `undefined: PortalHandlerParams`, `NewPortalHandler`, `registerRoutes`

- [ ] **Step 3: Implement `portal-service/handler.go` (Green)**

```go
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nkeys"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// TokenVerifier validates an SSO token and returns OIDC claims.
type TokenVerifier interface {
	Validate(ctx context.Context, rawToken string) (pkgoidc.Claims, error)
}

type portalAuthRequest struct {
	SSOToken      string `json:"ssoToken" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}

type devPortalAuthRequest struct {
	Account       string `json:"account" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}

// portalAuthResponse relays the upstream user untouched and adds natsUrl.
type portalAuthResponse struct {
	NATSJWT string          `json:"natsJwt"`
	NATSURL string          `json:"natsUrl"`
	User    json.RawMessage `json:"user"`
}

type redirectResponse struct {
	RedirectTo string `json:"redirectTo"`
}

// PortalHandler brokers credential issuance: verify SSO token, gate on the
// in-memory directory, route to the home site's auth-service.
type PortalHandler struct {
	verifier        TokenVerifier
	store           DirectoryStore
	dir             *dirMap
	forwarder       AuthForwarder
	siteAuth        map[string]string
	siteNATS        map[string]string
	siteFrontend    map[string]string   // siteID → normalized frontend origin
	frontendOrigins map[string]struct{} // normalized origin set (redirect check + CORS allowlist)
	adminToken      string
	devMode         bool
}

// PortalHandlerParams wires PortalHandler dependencies and site maps.
type PortalHandlerParams struct {
	Verifier         TokenVerifier
	Store            DirectoryStore
	Dir              *dirMap
	Forwarder        AuthForwarder
	SiteAuthURLs     map[string]string
	SiteNATSURLs     map[string]string
	SiteFrontendURLs map[string]string
	AdminToken       string
	DevMode          bool
}

func NewPortalHandler(p PortalHandlerParams) (*PortalHandler, error) {
	siteFrontend := make(map[string]string, len(p.SiteFrontendURLs))
	frontendOrigins := make(map[string]struct{}, len(p.SiteFrontendURLs))
	for siteID, raw := range p.SiteFrontendURLs {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return nil, fmt.Errorf("normalize frontend url for site %q: %w", siteID, err)
		}
		siteFrontend[siteID] = origin
		frontendOrigins[origin] = struct{}{}
	}
	return &PortalHandler{
		verifier:        p.Verifier,
		store:           p.Store,
		dir:             p.Dir,
		forwarder:       p.Forwarder,
		siteAuth:        p.SiteAuthURLs,
		siteNATS:        p.SiteNATSURLs,
		siteFrontend:    siteFrontend,
		frontendOrigins: frontendOrigins,
		adminToken:      p.AdminToken,
		devMode:         p.DevMode,
	}, nil
}

// HandleSessionNATSJWT is the client-facing credential entry (issue + refresh).
func (h *PortalHandler) HandleSessionNATSJWT(c *gin.Context) {
	if h.devMode {
		h.handleDevSession(c)
		return
	}
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req portalAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("ssoToken and natsPublicKey are required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	if !nkeys.IsValidPublicUserKey(req.NATSPublicKey) {
		errhttp.Write(ctx, c, errcode.BadRequest("invalid natsPublicKey format",
			errcode.WithReason(errcode.AuthInvalidNKey)))
		return
	}

	claims, err := h.verifier.Validate(ctx, req.SSOToken)
	if err != nil {
		if errors.Is(err, pkgoidc.ErrTokenExpired) {
			errhttp.Write(ctx, c, errcode.Unauthenticated("SSO token has expired, please re-login",
				errcode.WithReason(errcode.AuthTokenExpired)))
			return
		}
		errhttp.Write(ctx, c, errcode.Unauthenticated("invalid SSO token",
			errcode.WithReason(errcode.AuthInvalidToken),
			errcode.WithCause(err)))
		return
	}

	account := claims.Account()
	if account == "" {
		errhttp.Write(ctx, c, errcode.Unauthenticated("token missing account claim",
			errcode.WithReason(errcode.AuthInvalidToken)))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", account)

	body, err := json.Marshal(portalAuthRequest{SSOToken: req.SSOToken, NATSPublicKey: req.NATSPublicKey})
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("marshal forward body: %w", err))
		return
	}
	h.completeSession(ctx, c, account, body)
}

// handleDevSession accepts {account, natsPublicKey} without OIDC; local dev only.
func (h *PortalHandler) handleDevSession(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req devPortalAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("account and natsPublicKey are required",
			errcode.WithReason(errcode.AuthMissingFields)))
		return
	}
	if !nkeys.IsValidPublicUserKey(req.NATSPublicKey) {
		errhttp.Write(ctx, c, errcode.BadRequest("invalid natsPublicKey format",
			errcode.WithReason(errcode.AuthInvalidNKey)))
		return
	}
	ctx = errcode.WithLogValues(ctx, "account", req.Account)

	body, err := json.Marshal(req)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("marshal forward body: %w", err))
		return
	}
	h.completeSession(ctx, c, req.Account, body)
}

// completeSession runs the shared tail: gate → redirect → forward → relay.
func (h *PortalHandler) completeSession(ctx context.Context, c *gin.Context, account string, forwardBody []byte) {
	rec, found := h.dir.Lookup(account)
	denyCase := ""
	switch {
	case !found:
		denyCase = "no_record"
	case rec.SiteID == "":
		denyCase = "empty_site_id"
	default:
		if _, ok := h.siteAuth[rec.SiteID]; !ok {
			denyCase = "site_missing_auth_url"
		} else if _, ok := h.siteNATS[rec.SiteID]; !ok {
			denyCase = "site_missing_nats_url"
		}
	}
	if denyCase != "" {
		ctx = errcode.WithLogValues(ctx, "deny_case", denyCase, "site_id", rec.SiteID)
		errhttp.Write(ctx, c, errcode.Forbidden("account is not ready for chat",
			errcode.WithReason(errcode.PortalAccountNotReady)))
		return
	}

	if target, redirect := h.crossSiteRedirect(c.GetHeader("Origin"), rec.SiteID); redirect {
		slog.InfoContext(ctx, "cross-site login redirected home", "account", account, "home_site", rec.SiteID)
		c.JSON(http.StatusOK, redirectResponse{RedirectTo: target})
		return
	}

	status, respBody, err := h.forwarder.Forward(ctx, h.siteAuth[rec.SiteID], forwardBody)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("forward auth request: %w", err))
		return
	}
	if status != http.StatusOK {
		// Relay a well-formed upstream errcode envelope as-is; anything else is internal.
		if e, ok := errcode.Parse(respBody); ok && e.Code.Valid() {
			errhttp.Write(ctx, c, e)
			return
		}
		errhttp.Write(ctx, c, fmt.Errorf("auth-service returned unexpected status %d", status))
		return
	}

	var upstream struct {
		NATSJWT string          `json:"natsJwt"`
		User    json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal(respBody, &upstream); err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("decode auth-service response: %w", err))
		return
	}
	if upstream.NATSJWT == "" {
		errhttp.Write(ctx, c, fmt.Errorf("auth-service response missing natsJwt"))
		return
	}
	c.JSON(http.StatusOK, portalAuthResponse{
		NATSJWT: upstream.NATSJWT,
		NATSURL: h.siteNATS[rec.SiteID],
		User:    upstream.User,
	})
}

// crossSiteRedirect reports whether a known foreign frontend should be sent
// home. Origin is a UX signal only — the gate is the token + directory.
func (h *PortalHandler) crossSiteRedirect(origin, homeSiteID string) (string, bool) {
	if origin == "" {
		return "", false
	}
	homeOrigin, hasHome := h.siteFrontend[homeSiteID]
	if !hasHome {
		return "", false
	}
	norm, err := normalizeOrigin(origin)
	if err != nil {
		return "", false
	}
	if _, known := h.frontendOrigins[norm]; !known {
		return "", false
	}
	if norm == homeOrigin {
		return "", false
	}
	return homeOrigin, true
}

// HandleCacheReload re-runs LoadAll and swaps the map; cron-driven, bearer-guarded.
func (h *PortalHandler) HandleCacheReload(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	want := "Bearer " + h.adminToken
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte(want)) != 1 {
		errhttp.Write(ctx, c, errcode.Unauthenticated("invalid admin token"))
		return
	}
	n, err := h.dir.Reload(ctx, h.store)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("reload directory: %w", err))
		return
	}
	slog.InfoContext(ctx, "directory reloaded", "records", n)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "records": n})
}

func (h *PortalHandler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
```

- [ ] **Step 4: Create `portal-service/routes.go`**

```go
package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *PortalHandler) {
	r.POST("/session/nats-jwt", h.HandleSessionNATSJWT)
	r.POST("/admin/cache/reload", h.HandleCacheReload)
	r.GET("/healthz", h.HandleHealth)
}
```

- [ ] **Step 5: Run the tests**

Run: `make test SERVICE=portal-service`
Expected: PASS (all handler, dirmap, forwarder, middleware tests)

- [ ] **Step 6: Commit**

```bash
git add portal-service/handler.go portal-service/routes.go portal-service/handler_test.go
git commit -m "feat(portal-service): session handler with directory gate, cross-site redirect, and auth relay"
```

---

### Task 7: config + main wiring

**Files:**
- Create: `portal-service/main.go`
- Test: `portal-service/main_test.go`

Mirrors `auth-service/main.go` (env parse → fail-fast wiring → gin → `shutdown.Wait`). Portal extras: Mongo connect + initial `dir.Reload` fail-fast, site maps parsed with `envKeyValSeparator:"="` (URLs contain `:` so the default `:` separator cannot be used). HTTP-only shutdown: `srv.Shutdown` then `mongoutil.Disconnect` — no NATS anywhere in this service.

- [ ] **Step 1: Write the failing config tests**

Create `portal-service/main_test.go`:

```go
package main

import (
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateConfig(t *testing.T) {
	base := config{
		DevMode:       false,
		OIDCIssuerURL: "http://keycloak/realms/chatapp",
		OIDCAudiences: []string{"nats-chat"},
	}

	t.Run("prod with oidc passes", func(t *testing.T) {
		assert.NoError(t, validateConfig(base))
	})

	t.Run("prod without issuer fails", func(t *testing.T) {
		cfg := base
		cfg.OIDCIssuerURL = ""
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("prod without audiences fails", func(t *testing.T) {
		cfg := base
		cfg.OIDCAudiences = nil
		assert.Error(t, validateConfig(cfg))
	})

	t.Run("dev mode skips oidc requirements", func(t *testing.T) {
		assert.NoError(t, validateConfig(config{DevMode: true}))
	})
}

// Site maps use "=" between key and value because the URLs themselves contain ":".
func TestConfig_SiteMapParsing(t *testing.T) {
	t.Setenv("MONGO_URI", "mongodb://localhost:27017")
	t.Setenv("ADMIN_TOKEN", "tok")
	t.Setenv("SITE_AUTH_URLS", "site-a=http://auth-a:8080,site-b=http://auth-b:8080")
	t.Setenv("SITE_NATS_URLS", "site-a=wss://nats-a:9222")
	t.Setenv("SITE_FRONTEND_URLS", "site-a=https://chat-a.example.com")

	cfg, err := env.ParseAs[config]()
	require.NoError(t, err)
	assert.Equal(t, "http://auth-a:8080", cfg.SiteAuthURLs["site-a"])
	assert.Equal(t, "http://auth-b:8080", cfg.SiteAuthURLs["site-b"])
	assert.Equal(t, "wss://nats-a:9222", cfg.SiteNATSURLs["site-a"])
	assert.Equal(t, "https://chat-a.example.com", cfg.SiteFrontendURLs["site-a"])
	assert.Equal(t, "8080", cfg.Port)
}
```

- [ ] **Step 2: Verify the tests fail**

Run: `make test SERVICE=portal-service`
Expected: FAIL — `undefined: config`, `validateConfig`

- [ ] **Step 3: Implement `portal-service/main.go` (Green)**

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/mongoutil"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/shutdown"
)

type config struct {
	Port    string `env:"PORT"     envDefault:"8080"`
	DevMode bool   `env:"DEV_MODE" envDefault:"false"`

	// OIDC settings — required when DEV_MODE is false.
	OIDCIssuerURL string   `env:"OIDC_ISSUER_URL"`
	OIDCAudiences []string `env:"OIDC_AUDIENCES" envSeparator:","`
	TLSSkipVerify bool     `env:"TLS_SKIP_VERIFY" envDefault:"false"`

	MongoURI            string `env:"MONGO_URI,required"`
	MongoUsername       string `env:"MONGO_USERNAME" envDefault:""`
	MongoPassword       string `env:"MONGO_PASSWORD" envDefault:""`
	MongoDB             string `env:"MONGO_DB"       envDefault:"chat"`
	DirectoryCollection string `env:"DIRECTORY_COLLECTION" envDefault:"directory"`

	// "=" key/value separator: the URL values contain ":".
	SiteAuthURLs     map[string]string `env:"SITE_AUTH_URLS,required"     envSeparator:"," envKeyValSeparator:"="`
	SiteNATSURLs     map[string]string `env:"SITE_NATS_URLS,required"     envSeparator:"," envKeyValSeparator:"="`
	SiteFrontendURLs map[string]string `env:"SITE_FRONTEND_URLS,required" envSeparator:"," envKeyValSeparator:"="`

	AuthForwardTimeout time.Duration `env:"AUTH_FORWARD_TIMEOUT" envDefault:"10s"`
	AdminToken         string        `env:"ADMIN_TOKEN,required"`
}

func validateConfig(cfg config) error {
	if !cfg.DevMode && (cfg.OIDCIssuerURL == "" || len(cfg.OIDCAudiences) == 0) {
		return fmt.Errorf("OIDC_ISSUER_URL and OIDC_AUDIENCES are required when DEV_MODE is false")
	}
	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	ctx := context.Background()

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		return fmt.Errorf("connect mongo: %w", err)
	}
	store := newMongoDirectoryStore(mongoClient.Database(cfg.MongoDB), cfg.DirectoryCollection)

	// Bulk-load the directory before serving; an unloadable directory is fatal.
	dir := newDirMap()
	n, err := dir.Reload(ctx, store)
	if err != nil {
		return fmt.Errorf("initial directory load: %w", err)
	}
	slog.Info("directory loaded", "records", n)

	var verifier TokenVerifier
	if cfg.DevMode {
		slog.Info("dev mode enabled — OIDC validation disabled")
	} else {
		oidcValidator, err := pkgoidc.NewValidator(ctx, pkgoidc.Config{
			IssuerURL:     cfg.OIDCIssuerURL,
			Audiences:     cfg.OIDCAudiences,
			TLSSkipVerify: cfg.TLSSkipVerify,
		})
		if err != nil {
			return fmt.Errorf("create oidc validator: %w", err)
		}
		slog.Info("oidc validator initialized", "issuer", cfg.OIDCIssuerURL)
		verifier = oidcValidator
	}

	handler, err := NewPortalHandler(PortalHandlerParams{
		Verifier:         verifier,
		Store:            store,
		Dir:              dir,
		Forwarder:        newRestyForwarder(cfg.AuthForwardTimeout, cfg.TLSSkipVerify),
		SiteAuthURLs:     cfg.SiteAuthURLs,
		SiteNATSURLs:     cfg.SiteNATSURLs,
		SiteFrontendURLs: cfg.SiteFrontendURLs,
		AdminToken:       cfg.AdminToken,
		DevMode:          cfg.DevMode,
	})
	if err != nil {
		return fmt.Errorf("create portal handler: %w", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestIDMiddleware())
	r.Use(accessLogMiddleware())
	r.Use(corsMiddleware(handler.frontendOrigins))
	registerRoutes(r, handler)

	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	srvErr := make(chan error, 1)
	go func() {
		slog.Info("portal service starting", "addr", addr)
		srvErr <- srv.ListenAndServe()
	}()

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		// HTTP service cleanup order: stop the server, then disconnect Mongo.
		shutdown.Wait(ctx, 25*time.Second, func(ctx context.Context) error {
			slog.Info("shutting down portal service")
			if err := srv.Shutdown(ctx); err != nil {
				return fmt.Errorf("shutdown http server: %w", err)
			}
			mongoutil.Disconnect(ctx, mongoClient)
			return nil
		})
	}()

	err = <-srvErr
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen portal server: %w", err)
	}
	<-shutdownDone

	return nil
}
```

- [ ] **Step 4: Run tests and build**

Run: `make test SERVICE=portal-service && make build SERVICE=portal-service`
Expected: PASS, and `bin/portal-service` builds cleanly.

- [ ] **Step 5: Commit**

```bash
git add portal-service/main.go portal-service/main_test.go
git commit -m "feat(portal-service): config parsing, wiring, and graceful shutdown"
```

---

### Task 8: end-to-end integration test (real Mongo + stub auth-service)

**Files:**
- Modify: `portal-service/integration_test.go` (append; TestMain already exists from Task 3)

Proves the full chain with real pieces: Mongo-backed `LoadAll` → dirMap → handler → **real `restyForwarder`** → `httptest` auth-service (asserting `X-Request-ID` propagation) → relay; then the reload flow against live Mongo.

- [ ] **Step 1: Append the failing test**

Append to `portal-service/integration_test.go` (add the new imports to the existing block: `bytes`, `encoding/json`, `net/http`, `net/http/httptest`, `strings`, `time`, `github.com/gin-gonic/gin`, `github.com/hmchangw/chat/pkg/idgen`, `github.com/hmchangw/chat/pkg/natsutil`):

```go
func TestPortal_EndToEnd_IssueAndReload(t *testing.T) {
	db := testutil.MongoDB(t, "portal")
	ctx := context.Background()

	_, err := db.Collection("directory").InsertOne(ctx,
		bson.D{{Key: "_id", Value: "alice"}, {Key: "employeeId", Value: "E001"}, {Key: "siteId", Value: "site-a"}})
	require.NoError(t, err)

	var upstreamRequestID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequestID = r.Header.Get(natsutil.RequestIDHeader)
		assert.Equal(t, "/auth", r.URL.Path)
		var req struct {
			Account       string `json:"account"`
			NATSPublicKey string `json:"natsPublicKey"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"natsJwt":"jwt-e2e","user":{"account":"` + req.Account + `"}}`))
	}))
	t.Cleanup(upstream.Close)

	store := newMongoDirectoryStore(db, "directory")
	dir := newDirMap()
	_, err = dir.Reload(ctx, store)
	require.NoError(t, err)

	h, err := NewPortalHandler(PortalHandlerParams{
		Store:            store,
		Dir:              dir,
		Forwarder:        newRestyForwarder(5*time.Second, false),
		SiteAuthURLs:     map[string]string{"site-a": upstream.URL},
		SiteNATSURLs:     map[string]string{"site-a": "wss://nats-a:9222"},
		SiteFrontendURLs: map[string]string{"site-a": "https://chat-a.example.com"},
		AdminToken:       "it-admin-token",
		DevMode:          true, // account-shape body; no Keycloak in integration env
	})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestIDMiddleware())
	registerRoutes(r, h)

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/session/nats-jwt", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	userPub := mustUserNKey(t)

	// 1) alice is ready → credentials relayed, natsUrl resolved, request id propagated.
	w := post(`{"account":"alice","natsPublicKey":"` + userPub + `"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"natsJwt":"jwt-e2e"`)
	assert.Contains(t, w.Body.String(), `"natsUrl":"wss://nats-a:9222"`)
	assert.True(t, idgen.IsValidUUID(upstreamRequestID), "X-Request-ID must reach auth-service")

	// 2) bob is unknown until the cron syncs + reloads.
	w = post(`{"account":"bob","natsPublicKey":"` + userPub + `"}`)
	require.Equal(t, http.StatusForbidden, w.Code)

	_, err = db.Collection("directory").InsertOne(ctx,
		bson.D{{Key: "_id", Value: "bob"}, {Key: "employeeId", Value: "E002"}, {Key: "siteId", Value: "site-a"}})
	require.NoError(t, err)

	reloadReq := httptest.NewRequest(http.MethodPost, "/admin/cache/reload", bytes.NewReader(nil))
	reloadReq.Header.Set("Authorization", "Bearer it-admin-token")
	wr := httptest.NewRecorder()
	r.ServeHTTP(wr, reloadReq)
	require.Equal(t, http.StatusOK, wr.Code, wr.Body.String())

	w = post(`{"account":"bob","natsPublicKey":"` + userPub + `"}`)
	assert.Equal(t, http.StatusOK, w.Code, "bob must resolve after reload")
}
```

- [ ] **Step 2: Run it — it should pass immediately** (all pieces exist; this is a wiring proof, not a Red phase)

Run: `make test-integration SERVICE=portal-service`
Expected: PASS (3 tests). If it fails, the wiring contract is broken — fix the code, not the test.

- [ ] **Step 3: Commit**

```bash
git add portal-service/integration_test.go
git commit -m "test(portal-service): end-to-end integration — issue, gate, reload against real Mongo"
```

---

### Task 9: quality gates (coverage, lint, sast)

- [ ] **Step 1: Coverage check** (≥80% required; handlers should be 90%+)

Run: `go test -race -coverprofile=/tmp/portal-cover.out ./portal-service/ && go tool cover -func=/tmp/portal-cover.out | tail -5`
Expected: total ≥80%. `main()`/`run()` are the only untested lines; every handler/dirmap/forwarder/middleware function should show 85%+. If below the floor, add unit cases for the uncovered branches (the test lists in Tasks 2–7 cover every branch — a gap means a missed case, not a need for new production code).

- [ ] **Step 2: Lint + format**

Run: `make fmt && make lint`
Expected: no diffs, no findings. Fix anything reported before continuing.

- [ ] **Step 3: SAST**

Run: `make sast` (run `make tools` first if the binaries are missing; `sast-vuln`/`sast-semgrep` need network — if unavailable, run `make sast-gosec` minimum and note that CI runs the full set)
Expected: PASS. The only `#nosec` in the new code is the G402 in `forwarder.go` (dev-only TLS skip), which carries both the gosec-native comment and `//nolint:gosec`, matching `pkg/oidc/oidc.go`.

- [ ] **Step 4: Commit (only if fixes were needed)**

```bash
git add -A portal-service/
git commit -m "chore(portal-service): lint/sast cleanups"
```

---

### Task 10: deploy files + local-stack lockdown

**Files:**
- Create: `portal-service/deploy/Dockerfile`
- Create: `portal-service/deploy/docker-compose.yml`
- Create: `portal-service/deploy/azure-pipelines.yml`
- Modify: `docker-local/compose.services.yaml`
- Modify: `auth-service/deploy/docker-compose.yml`

No tests here (infra files); verification is `docker compose config` + a compose build.

- [ ] **Step 1: Create `portal-service/deploy/Dockerfile`** (auth-service's, renamed)

```dockerfile
FROM golang:1.25.11-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY portal-service/ portal-service/
RUN CGO_ENABLED=0 go build -o /portal-service ./portal-service/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=builder /portal-service /portal-service
USER app
ENTRYPOINT ["/portal-service"]
```

- [ ] **Step 2: Create `portal-service/deploy/docker-compose.yml`**

The portal takes over host port 8080 (freed from auth-service in Step 5). The one-shot `portal-directory-seed` seeds dev accounts — production directory data is owned by the daily ops cron.

```yaml
name: portal-service

services:
  portal-service:
    build:
      context: ../..
      dockerfile: portal-service/deploy/Dockerfile
    ports:
      - "8080:8080" # the only public credential entry; auth-service is internal-only
    env_file:
      - path: ../../docker-local/.env
        required: false
    environment:
      - PORT=8080
      # Bypass OIDC; accept any account name. Flip to false to test OIDC.
      - DEV_MODE=${DEV_MODE:-true}
      - OIDC_ISSUER_URL=http://keycloak:8080/realms/chatapp
      - OIDC_AUDIENCES=nats-chat
      - TLS_SKIP_VERIFY=false
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
      - DIRECTORY_COLLECTION=directory
      - SITE_AUTH_URLS=${SITE_AUTH_URLS:-site-local=http://auth-service:8080}
      - SITE_NATS_URLS=${SITE_NATS_URLS:-site-local=ws://localhost:9222}
      - SITE_FRONTEND_URLS=${SITE_FRONTEND_URLS:-site-local=http://localhost:3000} # vite dev server port (vite.config.js)
      - ADMIN_TOKEN=${PORTAL_ADMIN_TOKEN:-local-dev-admin-token}
    depends_on:
      portal-directory-seed:
        condition: service_completed_successfully
    networks:
      - chat-local

  # Dev-only one-shot: seed the directory so dev accounts resolve to site-local.
  portal-directory-seed:
    image: mongo:8.2.9
    entrypoint: ["mongosh", "mongodb://mongodb:27017/chat", "--quiet", "--eval"]
    command:
      - >-
        ['alice','bob','charlie','diana'].forEach(a =>
          db.directory.replaceOne(
            { _id: a },
            { _id: a, employeeId: 'E-' + a, siteId: 'site-local' },
            { upsert: true }))
    restart: "no"
    networks:
      - chat-local

networks:
  chat-local:
    external: true
```

- [ ] **Step 3: Create `portal-service/deploy/azure-pipelines.yml`**

Copy `auth-service/deploy/azure-pipelines.yml` verbatim, changing only the path filters and service name:
- both `paths.include` lists: `auth-service/` → `portal-service/`
- `SERVICE_NAME: auth-service` → `SERVICE_NAME: portal-service`

Everything else (stages, Go version, docker build) is identical.

- [ ] **Step 4: Register the portal in `docker-local/compose.services.yaml`**

In the `include:` list, after `  - ../notification-worker/deploy/docker-compose.yml`, add:

```yaml
  - ../portal-service/deploy/docker-compose.yml
```

- [ ] **Step 5: Lockdown mirror — remove auth-service's host port**

In `auth-service/deploy/docker-compose.yml`, delete:

```yaml
    ports:
      - "8080:8080"
```

and add this comment directly above `env_file:` (replacing the deleted block's position):

```yaml
    # No host port: auth-service is internal-only — credentials are issued
    # exclusively via portal-service (see portal-service spec §2.1).
```

- [ ] **Step 6: Validate compose files**

Run: `docker compose -f portal-service/deploy/docker-compose.yml config -q && docker compose -f auth-service/deploy/docker-compose.yml config -q && docker compose -f docker-local/compose.services.yaml config -q`
Expected: exit 0, no output. (The services compose warns only if the `chat-local` network is absent — that's fine outside `make deps-up`.)

- [ ] **Step 7: Commit**

```bash
git add portal-service/deploy/ docker-local/compose.services.yaml auth-service/deploy/docker-compose.yml
git commit -m "feat(portal-service): deploy files; lock auth-service behind the portal in local stack"
```

---

### Task 11: client API documentation

**Files:**
- Modify: `docs/client-api.md` (§2.2, §6)
- Modify: `chat-frontend/CLAUDE.md` (reason catalog)

Required by CLAUDE.md: client-facing handler changes must update `docs/client-api.md` in the same PR.

- [ ] **Step 1: Replace §2.2** (`### 2.2 HTTP — POST /auth`, lines ~135–214) with:

````markdown
### 2.2 HTTP — POST /session/nats-jwt (portal-service)

**Endpoint:** `POST /session/nats-jwt`
**Reply:** synchronous HTTP response

Exchanges an SSO token for a signed NATS user JWT plus the home-site NATS URL. portal-service is the **only public credential entry**: it verifies the SSO token, resolves the account's home site from the user directory (readiness gate), forwards to that site's auth-service to mint the JWT, and relays the result. auth-service `POST /auth` still exists but is internal-only (reachable solely by the portal).

#### Request body

| Field | Type | Required | Notes |
|---|---|---|---|
| `ssoToken` | string | yes | OIDC-issued SSO token. |
| `natsPublicKey` | string | yes | The client's NATS user public NKey (must pass `nkeys.IsValidPublicUserKey`). |

```json
{
  "ssoToken": "<sso-token>",
  "natsPublicKey": "UDXU4RCSJNZOIQHZNWXHXORDPRTGNJAHAHFRGZNEEJCPQTT2M7NLCNF4"
}
```

#### Success response — credentials

`HTTP 200`

| Field | Type | Notes |
|---|---|---|
| `natsJwt` | string | Signed NATS user JWT (minted by the home site's auth-service). Use when connecting to NATS (§2.1). |
| `natsUrl` | string | The home-site NATS WebSocket URL to connect to. Replaces any static client-side NATS URL config. |
| `user.email` | string | OIDC email claim. |
| `user.account` | string | The `{account}` value used in every NATS subject. Derived from `preferred_username` (falls back to `name`). |
| `user.employeeId` | string | Parsed from the SSO `description` claim. |
| `user.engName` | string | Parsed from the SSO `description` claim. |
| `user.chineseName` | string | Parsed from the SSO `description` claim. |
| `user.deptName` | string | OIDC dept-name claim. |
| `user.deptId` | string | OIDC dept-id claim. |

```json
{
  "natsJwt": "eyJ0eXAiOiJKV1QiLCJhbGciOiJlZDI1NTE5LW5rZXkifQ...",
  "natsUrl": "wss://nats-a.example.com:9222",
  "user": {
    "email": "alice@example.com",
    "account": "alice",
    "employeeId": "E12345",
    "engName": "Alice",
    "chineseName": "愛麗絲",
    "deptName": "Engineering",
    "deptId": "ENG"
  }
}
```

#### Success response — cross-site redirect

`HTTP 200` — returned instead of credentials when the request `Origin` is a known chat-frontend that is **not** the account's home site. No JWT is minted.

| Field | Type | Notes |
|---|---|---|
| `redirectTo` | string | The home-site frontend origin. Navigate there (`window.location.assign`); the home frontend re-runs this call (the Keycloak session already exists, so login is silent). |

```json
{ "redirectTo": "https://chat-a.example.com" }
```

#### Error response

See [Error envelope](#6-error-envelope-reference). HTTP statuses:

| Status | `code` | `reason` | Example body |
|---|---|---|---|
| 400 | `bad_request` | `missing_fields` | `{ "code": "bad_request", "reason": "missing_fields", "error": "ssoToken and natsPublicKey are required" }` |
| 400 | `bad_request` | `invalid_nkey` | `{ "code": "bad_request", "reason": "invalid_nkey", "error": "invalid natsPublicKey format" }` |
| 401 | `unauthenticated` | `sso_token_expired` | `{ "code": "unauthenticated", "reason": "sso_token_expired", "error": "SSO token has expired, please re-login" }` |
| 401 | `unauthenticated` | `invalid_sso_token` | `{ "code": "unauthenticated", "reason": "invalid_sso_token", "error": "invalid SSO token" }` |
| 403 | `forbidden` | `account_not_ready` | `{ "code": "forbidden", "reason": "account_not_ready", "error": "account is not ready for chat" }` — terminal until the directory sync provisions the account; show a "contact your administrator" screen, do not retry. |
| 500 | `internal` | — | `{ "code": "internal", "error": "internal error" }` — the real cause is logged server-side and never sent to the client. |

Upstream auth-service errors are relayed in the same envelope with their original status.

The returned `natsJwt` has a server-configured lifetime (default 2h). Clients should re-call `POST /session/nats-jwt` to refresh before it expires.

> **Background renewal.** The web client also calls `POST /session/nats-jwt`
> periodically to renew the NATS user JWT before it expires (at ~80% of the
> token's lifetime, jittered). It obtains a fresh SSO access token in the
> background via the OIDC refresh token (silent renew) and re-mints with the
> **same** `natsPublicKey`, so the request/response schema is identical to the
> initial login call. A `redirectTo` reply during renewal means the account
> moved sites mid-session — navigate to it. When silent renewal fails (the SSO
> session has ended), the client performs a graceful re-login redirect instead.

#### Triggered events — success path

`None — HTTP-only.`

#### Triggered events — error path

`None.`
````

- [ ] **Step 2: Update §2.1's pointer** — in the §2.1 intro sentence, change "a signed JWT obtained from the auth-service (§2.2)" to "a signed JWT obtained via portal-service (§2.2)".

- [ ] **Step 3: Check the Table of contents** (lines ~36–60) — if it lists the §2.2 heading text, update it to `2.2 HTTP — POST /session/nats-jwt (portal-service)` matching the new heading anchor style used by neighboring entries.

- [ ] **Step 4: §6 reason catalog** — in the table ending with `missing_fields`, append after that row:

```markdown
| `account_not_ready` | forbidden | portal-service `POST /session/nats-jwt` (no ready directory record for the account) |
```

- [ ] **Step 5: §6 "Where envelopes are sent"** — change the HTTP bullet from "auth-service `POST /auth` writes the envelope…" to "portal-service `POST /session/nats-jwt` writes the envelope as the response body with the matching HTTP status from the table above (upstream auth-service envelopes are relayed unchanged)."

- [ ] **Step 6: §6 client branching guidance** — extend the reason examples sentence with: "show a terminal 'account not provisioned' screen on `account_not_ready` (do not retry)".

- [ ] **Step 7: chat-frontend/CLAUDE.md reason catalog** — in the "Reasons emitted today" list, after the auth-service line, add:

```markdown
- `account_not_ready` — portal-service (account not provisioned/ready in the directory; terminal screen, not a retry)
```

- [ ] **Step 8: Commit**

```bash
git add docs/client-api.md chat-frontend/CLAUDE.md
git commit -m "docs(client-api): portal-service POST /session/nats-jwt replaces public POST /auth"
```

---

### Task 12: frontend runtime config + deploy templates

**Files:**
- Modify: `chat-frontend/src/lib/runtimeConfig.js`
- Modify: `chat-frontend/deploy/config.js.template`
- Modify: `chat-frontend/deploy/30-render-config.sh`
- Modify: `chat-frontend/deploy/docker-compose.yml`

Frontend rules: `chat-frontend/CLAUDE.md` applies. Run all npm commands from `chat-frontend/`.

- [ ] **Step 1: `runtimeConfig.js`** — replace the `AUTH_URL` and `NATS_URL` exports (lines 6–10) with:

```js
export const PORTAL_URL =
  runtime.PORTAL_URL || import.meta.env.VITE_PORTAL_URL || 'http://localhost:8080'
```

Keep `DEFAULT_SITE_ID`, `DEV_MODE`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID` unchanged (login is unchanged; `natsUrl` now arrives in the portal response).

- [ ] **Step 2: `deploy/config.js.template`** — replace:

```js
  AUTH_URL: "${AUTH_URL}",
  NATS_URL: "${NATS_URL}",
```

with:

```js
  PORTAL_URL: "${PORTAL_URL}",
```

- [ ] **Step 3: `deploy/30-render-config.sh`** — replace the two default lines:

```bash
: "${AUTH_URL:=http://localhost:8080}"
: "${NATS_URL:=ws://localhost:9222}"
```

with:

```bash
: "${PORTAL_URL:=http://localhost:8080}"
```

and update the `export` line, the `envsubst` variable list, and the final `echo` line: replace `AUTH_URL NATS_URL` / `${AUTH_URL} ${NATS_URL}` / `AUTH_URL=$AUTH_URL  NATS_URL=$NATS_URL` with `PORTAL_URL` / `${PORTAL_URL}` / `PORTAL_URL=$PORTAL_URL` respectively.

- [ ] **Step 4: `deploy/docker-compose.yml`** (frontend) — replace:

```yaml
      AUTH_URL: ${AUTH_URL:-http://localhost:8080}
      NATS_URL: ${NATS_URL:-ws://localhost:9222}
```

with:

```yaml
      PORTAL_URL: ${PORTAL_URL:-http://localhost:8080}
```

- [ ] **Step 5: Remove the dead `/auth` dev proxy**

In `chat-frontend/vite.config.js`, delete the proxy block (all fetches use the absolute `PORTAL_URL`; nothing requests a relative `/auth` after the cutover):

```js
    proxy: {
      '/auth': 'http://localhost:8080',
    },
```

(keep `server.port: 3000`).

- [ ] **Step 6: Verify nothing else references the removed exports**

Run: `grep -rn "AUTH_URL\|NATS_URL" chat-frontend/src/ chat-frontend/vite.config.js`
Expected: only `chat-frontend/src/context/NatsContext/NatsContext.jsx` (fixed in Task 13). If anything else appears, repoint it to `PORTAL_URL`/portal response data in this task.

- [ ] **Step 7: Commit**

```bash
git add chat-frontend/src/lib/runtimeConfig.js chat-frontend/deploy/ chat-frontend/vite.config.js
git commit -m "feat(chat-frontend): PORTAL_URL replaces AUTH_URL/NATS_URL runtime config"
```

(The frontend tests still pass at this point only because nothing imports the removed names yet besides NatsContext — Task 13 lands immediately after; run the full `npm test` gate there.)

---

### Task 13: frontend NatsContext → portal (+ redirect branch)

**Files:**
- Create: `chat-frontend/src/lib/portalRedirect.js` (+ test)
- Modify: `chat-frontend/src/context/NatsContext/NatsContext.jsx`
- Modify: `chat-frontend/src/context/NatsContext/NatsContext.test.jsx`
- Modify: `chat-frontend/src/api/_transport/asyncJob.ts` (REASON_COPY)

The `redirectTo` branch is needed in two call sites (connect + refresh) — it lives once in `lib/` (pure utility per chat-frontend/CLAUDE.md), so the `window.location` test scaffolding exists in exactly one test file.

- [ ] **Step 0: Create the shared helper (TDD)**

Create `chat-frontend/src/lib/portalRedirect.test.js`:

```js
import { describe, it, expect, vi } from 'vitest'
import { followPortalRedirect } from './portalRedirect'

describe('followPortalRedirect', () => {
  it('navigates and reports true for a redirect payload', () => {
    const assign = vi.fn()
    const original = window.location
    Object.defineProperty(window, 'location', { writable: true, value: { ...original, assign } })
    try {
      expect(followPortalRedirect({ redirectTo: 'https://chat-a.example.com' })).toBe(true)
      expect(assign).toHaveBeenCalledWith('https://chat-a.example.com')
    } finally {
      Object.defineProperty(window, 'location', { writable: true, value: original })
    }
  })

  it('reports false and stays put without redirectTo', () => {
    expect(followPortalRedirect({ natsJwt: 'x' })).toBe(false)
    expect(followPortalRedirect(undefined)).toBe(false)
  })
})
```

Run `cd chat-frontend && npm test -- --run src/lib/portalRedirect.test.js` → FAIL (module missing). Then create `chat-frontend/src/lib/portalRedirect.js`:

```js
// Navigates to a portal-issued redirectTo target (wrong-site login or
// mid-session site move). Returns true when navigation was triggered.
export function followPortalRedirect(data) {
  if (!data?.redirectTo) return false
  window.location.assign(data.redirectTo)
  return true
}
```

Re-run → PASS.

- [ ] **Step 1: Update the NatsContext tests (Red)**

In `chat-frontend/src/context/NatsContext/NatsContext.test.jsx`:

(a) Update the fetch mock in `beforeEach` to the portal response shape:

```js
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        natsJwt: 'JWT123',
        natsUrl: 'ws://nats.from-portal:9222',
        user: { account: 'alice' },
      }),
    })
```

(b) In the first test (`sets credentials before connecting…`), add after the existing `natsConnect` assertion:

```js
    expect(global.fetch).toHaveBeenCalledWith(
      'http://localhost:8080/session/nats-jwt',
      expect.objectContaining({ method: 'POST' }),
    )
    expect(natsConnect).toHaveBeenCalledWith(
      expect.objectContaining({ servers: 'ws://nats.from-portal:9222' }))
```

(c) Mock the shared helper at the top of the file (with the other `vi.mock` calls), defaulting to "no redirect":

```js
vi.mock('@/lib/portalRedirect', () => ({ followPortalRedirect: vi.fn(() => false) }))
```

and import it with the other imports: `import { followPortalRedirect } from '@/lib/portalRedirect'`. Add `followPortalRedirect.mockClear().mockReturnValue(false)` to `beforeEach`.

(d) Add a new test at the end of the describe block (navigation itself is covered by `portalRedirect.test.js` — here we assert the call site honors the helper):

```js
  it('stops without connecting when the portal returns redirectTo', async () => {
    global.fetch.mockResolvedValue({
      ok: true,
      json: async () => ({ redirectTo: 'https://chat-home.example.com' }),
    })
    followPortalRedirect.mockReturnValueOnce(true)
    const { result } = renderHook(() => useNats(), { wrapper })
    await act(async () => {
      await result.current.connect({ mode: 'sso', ssoToken: 'tok', siteId: 'site-1' })
    })
    expect(followPortalRedirect).toHaveBeenCalledWith(
      expect.objectContaining({ redirectTo: 'https://chat-home.example.com' }))
    expect(natsConnect).not.toHaveBeenCalled()
    expect(result.current.connected).toBe(false)
  })
```

- [ ] **Step 2: Verify the tests fail**

Run: `cd chat-frontend && npm test -- --run src/context/NatsContext/NatsContext.test.jsx`
Expected: FAIL — the import of `AUTH_URL`/`NATS_URL` from runtimeConfig no longer resolves (Task 12 removed them), and the new assertions don't match.

- [ ] **Step 3: Update `NatsContext.jsx` (Green)**

Apply these exact changes:

(a) Line 4 import (plus the helper):

```js
import { PORTAL_URL } from '@/lib/runtimeConfig'
import { followPortalRedirect } from '@/lib/portalRedirect'
```

(b) Lines 22–23: replace

```js
  const authUrl = AUTH_URL
  const natsUrl = NATS_URL
```

with

```js
  const portalUrl = PORTAL_URL
```

(c) Line 25: `useJwtRefresh({ authUrl, ncRef })` → `useJwtRefresh({ portalUrl, ncRef })`

(d) In `connectToNats`, replace the fetch URL:

```js
    const authResp = await fetch(`${portalUrl}/session/nats-jwt`, {
```

(e) Replace the success-path destructure (line 70):

```js
    const data = await authResp.json()
    if (followPortalRedirect(data)) return

    const { natsJwt, natsUrl, user: userInfo } = data
```

(f) The `natsConnect` call keeps `servers: natsUrl` (now from the response, not config).

(g) Update the callback deps (line 96): `[authUrl, natsUrl, authenticator, setCredentials]` → `[portalUrl, authenticator, setCredentials]`

(h) Update the function's JSDoc first line: "Authenticate against auth-service and open the NATS WebSocket connection." → "Authenticate via portal-service and open the NATS WebSocket connection to the returned natsUrl."

(i) Update the error-path comment (lines 59–61) to two lines max:

```js
      // portal-service relays the errcode envelope {code, reason?, error, metadata?};
      // consumers branch on reason ?? code and fall back to message text.
```

- [ ] **Step 4: Add the REASON_COPY entry**

In `chat-frontend/src/api/_transport/asyncJob.ts`, after the `pin_room_too_large` line in `REASON_COPY`, add:

```ts
  account_not_ready: "Your account isn't set up for chat yet — contact your administrator.",
```

- [ ] **Step 5: Run the tests**

Run: `cd chat-frontend && npm test -- --run src/lib/portalRedirect.test.js src/context/NatsContext/NatsContext.test.jsx && npm run typecheck`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/lib/portalRedirect.js chat-frontend/src/lib/portalRedirect.test.js chat-frontend/src/context/NatsContext/NatsContext.jsx chat-frontend/src/context/NatsContext/NatsContext.test.jsx chat-frontend/src/api/_transport/asyncJob.ts
git commit -m "feat(chat-frontend): connect via portal-service; handle cross-site redirectTo"
```

---

### Task 14: frontend refresh loop → portal

**Files:**
- Modify: `chat-frontend/src/context/NatsContext/useJwtRefresh.js`
- Modify: `chat-frontend/src/context/NatsContext/useJwtRefresh.test.js`

- [ ] **Step 1: Update the tests first (Red)**

In `useJwtRefresh.test.js`:

(a) In `setup()`, change the hook args:

```js
  const view = renderHook(() => useJwtRefresh({ portalUrl: 'http://portal', ncRef }))
```

(b) In the test `refreshes at ~80% of life…`, add after the body assertion:

```js
    expect(global.fetch.mock.calls[0][0]).toBe('http://portal/session/nats-jwt')
```

(c) Mock the shared helper at the top (with the other `vi.mock` calls), import it, and reset it in `beforeEach` (`followPortalRedirect.mockReset().mockReturnValue(false)`):

```js
vi.mock('@/lib/portalRedirect', () => ({ followPortalRedirect: vi.fn(() => false) }))
```
```js
import { followPortalRedirect } from '@/lib/portalRedirect'
```

(d) Add a new test after the ~80%-of-life test:

```js
  it('stops the loop when the portal returns redirectTo mid-session (site move)', async () => {
    renewSsoToken.mockResolvedValue('sso')
    global.fetch.mockResolvedValue({
      ok: true,
      json: async () => ({ redirectTo: 'https://chat-home.example.com' }),
    })
    followPortalRedirect.mockReturnValueOnce(true)
    const reconnect = vi.fn()
    const { result } = setup({ ncRef: { current: { reconnect } } })
    act(() => {
      result.current.setCredentials({ jwt: makeJwt(100), seed: new Uint8Array(), natsPublicKey: 'UPUB', refreshable: true })
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(85_000) })
    expect(followPortalRedirect).toHaveBeenCalledWith(
      expect.objectContaining({ redirectTo: 'https://chat-home.example.com' }))
    expect(reconnect).not.toHaveBeenCalled()
    expect(redirectToReloginOnTokenInvalid).not.toHaveBeenCalled()
    // Timer cleared: no further refresh attempts fire.
    await act(async () => { await vi.advanceTimersByTimeAsync(200_000) })
    expect(renewSsoToken).toHaveBeenCalledTimes(1)
  })
```

- [ ] **Step 2: Verify the tests fail**

Run: `cd chat-frontend && npm test -- --run src/context/NatsContext/useJwtRefresh.test.js`
Expected: FAIL — the hook still reads `authUrl` (undefined → fetch URL is `undefined/auth`), and the redirect test fails.

- [ ] **Step 3: Update `useJwtRefresh.js` (Green)**

(a) Signature (line 29): `export function useJwtRefresh({ authUrl, ncRef })` → `export function useJwtRefresh({ portalUrl, ncRef })`

(b) JSDoc line 24–26: change "call after each /auth" to "call after each portal mint".

(c) In `refresh`, the fetch:

```js
      const resp = await fetch(`${portalUrl}/session/nats-jwt`, {
```

(d) Add the helper import at the top: `import { followPortalRedirect } from '@/lib/portalRedirect'`, then replace the success-path block (lines 105–107):

```js
      const data = await resp.json()
      if (stale()) return
      // Mid-session site move: this account now lives on another frontend.
      if (followPortalRedirect(data)) {
        clearTimer()
        return
      }
      const { natsJwt } = data
      jwtRef.current = natsJwt
```

(keep the existing reconnect + `scheduleRefresh(natsJwt)` lines that follow; delete the old `const { natsJwt } = await resp.json()` and `if (stale()) return` pair they replace).

(e) Update the `refresh` deps array (line 125): `[authUrl, ncRef, scheduleRefresh, redirect, armTimer]` → `[portalUrl, ncRef, scheduleRefresh, redirect, armTimer, clearTimer]`

- [ ] **Step 4: Run the full frontend gate**

Run: `cd chat-frontend && npm test -- --run && npm run typecheck`
Expected: PASS (all suites — NatsContext, useJwtRefresh, and everything untouched)

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/context/NatsContext/useJwtRefresh.js chat-frontend/src/context/NatsContext/useJwtRefresh.test.js
git commit -m "feat(chat-frontend): JWT refresh re-mints via portal-service"
```

---

### Task 15: smoke scripts → portal

**Files:**
- Modify: `chat-frontend/smoke-test.mjs`
- Modify: `chat-frontend/scripts/liveStack.smoke.mjs`

Both scripts hit `${AUTH_URL}/auth` on host port 8080 — that slot now belongs to the portal (auth-service lost its host port in Task 10). Same port, new path, `natsUrl` available from the response. No test runner covers these; they're verified against a live stack (optional step below).

- [ ] **Step 1: `smoke-test.mjs`**

(a) Header comment line "(auth-service on :8080, NATS WS on :4223)" → "(portal-service on :8080, NATS WS on :4223)".

(b) Lines 15–16:

```js
const PORTAL_URL = 'http://localhost:8080'
const NATS_URL = 'ws://localhost:4223'
```

(c) In `authenticate()`, the fetch:

```js
  const resp = await fetch(`${PORTAL_URL}/session/nats-jwt`, {
```

(d) Return the portal-resolved URL too:

```js
  const data = await resp.json()
  return { nkey, natsPublicKey, jwt: data.natsJwt, natsUrl: data.natsUrl, user: data.user }
```

(e) In `connectNats()`: `servers: NATS_URL,` → `servers: auth.natsUrl || NATS_URL,`

- [ ] **Step 2: `scripts/liveStack.smoke.mjs`**

(a) Line 3 header comment: "POST /auth (dev mode) returns a NATS JWT" → "POST /session/nats-jwt (dev mode) returns a NATS JWT".

(b) Line 25: `const AUTH_URL = process.env.AUTH_URL || 'http://localhost:8080'` → `const PORTAL_URL = process.env.PORTAL_URL || 'http://localhost:8080'`

(c) In `devLogin()`, the fetch: `` fetch(`${AUTH_URL}/auth`, … `` → `` fetch(`${PORTAL_URL}/session/nats-jwt`, … `` and the error message `auth failed:` → `portal auth failed:`.

(d) Line 51 log: `` console.log(`Auth: ${AUTH_URL}  |  NATS-ws: ${NATS_WS}`) `` → `` console.log(`Portal: ${PORTAL_URL}  |  NATS-ws: ${NATS_WS}`) ``

- [ ] **Step 3 (optional, needs the live local stack):** `make deps-up && make up`, then `cd chat-frontend && npm run smoke:livestack`
Expected: all checks PASS through the portal.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/smoke-test.mjs chat-frontend/scripts/liveStack.smoke.mjs
git commit -m "chore(chat-frontend): smoke scripts authenticate via portal-service"
```

---

### Task 16: full verification + push

- [ ] **Step 1: Full Go gate**

Run: `make fmt && make lint && make test`
Expected: clean. (`make test` runs every package — confirms nothing outside portal-service broke.)

- [ ] **Step 2: Mock freshness**

Run: `make generate && git status --porcelain`
Expected: no diff (generated mocks are current).

- [ ] **Step 3: Integration suite**

Run: `make test-integration SERVICE=portal-service`
Expected: PASS.

- [ ] **Step 4: SAST**

Run: `make sast`
Expected: PASS (see Task 9 note about network-dependent scanners).

- [ ] **Step 5: Full frontend gate**

Run: `cd chat-frontend && npm run typecheck && npm test -- --run && npm run build`
Expected: all clean.

- [ ] **Step 6: Spec-coverage sanity check** — confirm each spec section has landed: §2.1 lockdown (Task 10), §3 topology/dirMap (2, 7), §3.1 redirect (6, 13, 14), §4 data model + reload (2, 3, 6), §5 site maps (7), §6 endpoints (6), §7 errors (1, 6), §8 token validation + shared account derivation (1b, 6, 7), §9 layout (all), §10 config (7), §11 testing (2–9), §12 deploy/docs (10, 11), §14 frontend (12–15).

- [ ] **Step 7: Push**

```bash
git push -u origin claude/dreamy-bell-cqdibt
```

(On network failure retry up to 4 times with 2s/4s/8s/16s backoff.)

---

## Execution notes

- **Order matters only where stated:** Tasks 1→9 (including 1b) are sequential — each builds on the last. Tasks 10–11 are independent of each other. Tasks 12→15 are sequential (12 breaks the frontend build until 13 lands — commit them in close succession).
- **Docker unavailability:** integration steps (3, 8, 16.3) need Docker; if the environment lacks it, verify compilation (`go vet ./portal-service/`), note it in the commit, and rely on CI.
- **Do not** touch auth-service Go code, `pkg/oidc`, or any worker service — the spec's lockdown is deployment-level only.

