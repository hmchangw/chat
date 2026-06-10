# avatar-service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `avatar-service` — a Gin HTTP service that resolves user/bot/room avatars by 307-redirecting (external employee-photo or owning cluster) or proxy-streaming custom images from MinIO, with a deterministic SVG fallback, plus a platform-admin-gated bot-avatar upload endpoint.

**Architecture:** Read path resolves the subject's owning site → cross-cluster 307 (loop-broken by `?fwd=1`) → looks up the `avatars` Mongo doc → streams the MinIO object (304 answered from the doc's ETag, no MinIO hit) or generates a deterministic initials SVG. Custom-image existence is the presence of an `avatars` doc (written by bot uploads here, or by an external one-off migration for legacy room images). No NATS; auth is OIDC token validation; upload authz is the caller's platform-admin role.

**Tech Stack:** Go 1.25, Gin, `go.mongodb.org/mongo-driver/v2` (via `pkg/mongoutil`), `minio-go/v7` (via `pkg/minioutil`), `pkg/oidc`, `pkg/errcode`+`errhttp`, `pkg/shutdown`, `caarlos0/env/v11`, `go.uber.org/mock` (mockgen), `testify`, `pkg/testutil` (testcontainers).

**Source spec:** `docs/specs/avatar-service.md`.

**Out of scope (this plan):** the legacy-data **migration** (a separate external one-off job, spec §4.4); OTel/Prometheus (spec defers post-v1); public-GET rate-limiting (spec §9, not yet considered).

**Two spec gaps resolved in this plan (confirm with author):**
- Bot detection uses the codebase's canonical `botPattern` = `` `\.bot$|^p_` `` (matches `.bot` suffix **and** `p_` prefix), not the spec's `.bot`-only wording (spec §5).
- The account `@domain`→`siteID` mapping is unspecified in the spec; this plan adds a `DOMAIN_SITES` (domain→siteID) config map. `CLUSTER_DOMAINS` remains siteID→baseURL.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `pkg/model/avatar.go` | `Avatar` struct + `AvatarSubjectType` constants (shared model) |
| `pkg/model/model_test.go` | add `Avatar` to the JSON round-trip test (modify) |
| `avatar-service/avatar.go` | pure helpers: `isBot`/`botPattern`, `parseAccount`, `sanitizeInitial`, `renderDefaultSVG`, `defaultETag`, `stableHash`, `palette`, `botObjectKey` |
| `avatar-service/store.go` | `avatarStore` interface + `//go:generate mockgen` |
| `avatar-service/store_mongo.go` | Mongo implementation (`users`, `subscriptions`, `avatars`) |
| `avatar-service/minio.go` | `blobStore` seam + `minioBlobStore` (MinIO impl), `errBlobNotFound` |
| `avatar-service/cache.go` | thread-safe bounded TTL cache for account→employeeID |
| `avatar-service/config.go` | `config` struct (`caarlos0/env`) + helpers (`clusterBaseURL`, `resolveBotSite`) |
| `avatar-service/middleware.go` | `requestIDMiddleware`, `accessLogMiddleware`, `corsMiddleware`, `adminAuthMiddleware` |
| `avatar-service/routes.go` | route registration |
| `avatar-service/handler.go` | `handler` struct, `HandleHealth`, read endpoints, `serveStored`, `serveDefault` |
| `avatar-service/upload.go` | `HandleBotUpload` (parse/locality/existence/validate/store/upsert) |
| `avatar-service/main.go` | config parse, wire Mongo+MinIO+OIDC, Gin server, graceful shutdown |
| `avatar-service/*_test.go` | unit tests (same `package main`) |
| `avatar-service/mock_store_test.go` | generated mock (never hand-edited) |
| `avatar-service/integration_test.go` | testcontainers (Mongo + MinIO), `//go:build integration` |
| `avatar-service/deploy/` | `Dockerfile`, `docker-compose.yml`, `azure-pipelines.yml` |
| `docs/client-api.md` | add an avatar-service section (modify) |

Each task is TDD: write the failing test, run it red, implement minimally, run it green, commit.

---

## Task 1: `Avatar` model

**Files:**
- Create: `pkg/model/avatar.go`
- Test: `pkg/model/model_test.go` (modify)

- [ ] **Step 1: Write the failing test**

Add to `pkg/model/model_test.go`:

```go
func TestAvatarJSON(t *testing.T) {
	src := &model.Avatar{
		ID:          "bot:helper.bot",
		SubjectType: model.AvatarSubjectBot,
		SubjectID:   "helper.bot",
		MinioKey:    "bot/helper.bot",
		ContentType: "image/png",
		Size:        2048,
		ETag:        "abc123",
		CreatedAt:   time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	}
	roundTrip(t, src, &model.Avatar{})
}
```

(If `time` is not already imported in the test file, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/model/ -run TestAvatarJSON -v`
Expected: FAIL — `undefined: model.Avatar` / `model.AvatarSubjectBot`.

- [ ] **Step 3: Write minimal implementation**

Create `pkg/model/avatar.go`:

```go
package model

import "time"

// AvatarSubjectType discriminates what an Avatar document portrays.
type AvatarSubjectType string

const (
	AvatarSubjectRoom AvatarSubjectType = "room"
	AvatarSubjectBot  AvatarSubjectType = "bot"
)

// Avatar is a custom (uploaded or migrated) avatar for a room or bot, stored in
// the avatars collection. Presence of a document means the subject has a custom
// image in MinIO; absence means the service serves a generated default.
// The collection is cluster-local, so no siteId is stored.
type Avatar struct {
	ID          string            `json:"id"          bson:"_id"`
	SubjectType AvatarSubjectType `json:"subjectType" bson:"subjectType"`
	// SubjectID is the id the service looks the subject up by:
	//   room → roomID;  bot → bot account (".bot").
	SubjectID   string    `json:"subjectId"   bson:"subjectId"`
	MinioKey    string    `json:"minioKey"    bson:"minioKey"`
	ContentType string    `json:"contentType" bson:"contentType"`
	Size        int64     `json:"size"        bson:"size"`
	ETag        string    `json:"etag"        bson:"etag"`
	CreatedAt   time.Time `json:"createdAt"   bson:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"   bson:"updatedAt"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/model/ -run TestAvatarJSON -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/avatar.go pkg/model/model_test.go
git commit -m "feat(model): add Avatar type for avatar-service"
```

---

## Task 2: Pure SVG/account helpers (`avatar.go`)

**Files:**
- Create: `avatar-service/avatar.go`
- Test: `avatar-service/avatar_test.go`

- [ ] **Step 1: Write the failing test**

Create `avatar-service/avatar_test.go`:

```go
package main

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsBot(t *testing.T) {
	assert.True(t, isBot("helper.bot"))
	assert.True(t, isBot("p_payroll"))
	assert.False(t, isBot("alice"))
}

func TestParseAccount(t *testing.T) {
	l, d := parseAccount("helper.bot@site2.example.com")
	assert.Equal(t, "helper.bot", l)
	assert.Equal(t, "site2.example.com", d)
	l, d = parseAccount("alice")
	assert.Equal(t, "alice", l)
	assert.Equal(t, "", d)
}

func TestSanitizeInitial(t *testing.T) {
	cases := map[string]string{
		"alice":        "A",
		"張三":           "張",
		"7eleven":      "7",
		"</text>":      "?", // not a letter/digit → placeholder
		"":             "?",
		" leading":     "?", // leading space → placeholder (first rune is space)
	}
	for in, want := range cases {
		assert.Equalf(t, want, sanitizeInitial(in), "sanitizeInitial(%q)", in)
	}
}

func TestRenderDefaultSVG_Deterministic(t *testing.T) {
	a := renderDefaultSVG("room-1", "General")
	b := renderDefaultSVG("room-1", "General")
	assert.Equal(t, a, b, "same input must yield byte-identical SVG")
}

func TestRenderDefaultSVG_StableColourPerSeed(t *testing.T) {
	// Same seed → same fill, regardless of the name.
	a := renderDefaultSVG("room-1", "Alpha")
	b := renderDefaultSVG("room-1", "Beta")
	fa := strings.Split(strings.SplitN(a, `fill="`, 2)[1], `"`)[0]
	_ = b
	assert.Contains(t, string(a), `fill="`+fa+`"`)
}

func TestRenderDefaultSVG_ValidXMLAndInjectionSafe(t *testing.T) {
	out := renderDefaultSVG("seed", `</text><script>alert(1)</script>`)
	require.NoError(t, xml.Unmarshal(out, new(struct{ XMLName xml.Name })), "must be well-formed XML")
	assert.NotContains(t, string(out), "<script>", "hostile name must not produce raw markup")
}

func TestDefaultETag_StableAndQuoted(t *testing.T) {
	e1 := defaultETag("room-1", "General")
	e2 := defaultETag("room-1", "General")
	assert.Equal(t, e1, e2)
	assert.True(t, strings.HasPrefix(e1, `"`) && strings.HasSuffix(e1, `"`), "ETag must be quoted")
}

func TestBotObjectKey(t *testing.T) {
	assert.Equal(t, "bot/helper.bot", botObjectKey("helper.bot"))
}
```

Note `renderDefaultSVG` returns `[]byte`; the tests above index it as a string in a couple of places — wrap with `string(...)`. Adjust:
- `strings.Split(strings.SplitN(string(a), ...))` and `string(b)`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./avatar-service/ -run 'TestIsBot|TestParseAccount|TestSanitizeInitial|TestRenderDefaultSVG|TestDefaultETag|TestBotObjectKey' -v`
Expected: FAIL — undefined symbols (package `main` has no other files yet; this is the first file).

- [ ] **Step 3: Write minimal implementation**

Create `avatar-service/avatar.go`:

```go
package main

import (
	"fmt"
	"hash/fnv"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const svgTemplateVersion = "v1"

// botPattern mirrors room-service / message-gatekeeper: an account is a bot if
// it ends in ".bot" or begins with "p_".
var botPattern = regexp.MustCompile(`\.bot$|^p_`)

func isBot(account string) bool { return botPattern.MatchString(account) }

// parseAccount splits "<local>@<domain>" into its parts; domain is "" if absent.
func parseAccount(account string) (local, domain string) {
	if i := strings.IndexByte(account, '@'); i >= 0 {
		return account[:i], account[i+1:]
	}
	return account, ""
}

var palette = []string{
	"#1abc9c", "#2ecc71", "#3498db", "#9b59b6",
	"#e67e22", "#e74c3c", "#f39c12", "#16a085",
}

func stableHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// sanitizeInitial returns the first rune of name, uppercased, when it is a
// letter or digit; otherwise a neutral placeholder "?". The result never
// contains characters that need XML escaping.
func sanitizeInitial(name string) string {
	r, sz := utf8.DecodeRuneInString(name)
	if sz == 0 || r == utf8.RuneError {
		return "?"
	}
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return string(unicode.ToUpper(r))
	}
	return "?"
}

// defaultETag is a strong, deterministic validator over (seed, sanitized glyph).
func defaultETag(seed, name string) string {
	return fmt.Sprintf(`"%s-%x"`, svgTemplateVersion, stableHash(seed+sanitizeInitial(name)))
}

// renderDefaultSVG returns the same bytes for the same (seed, name) on every
// replica. seed picks the background colour; the first sanitized rune of name is
// the glyph. The glyph is html.EscapeString-escaped as defense-in-depth.
func renderDefaultSVG(seed, name string) []byte {
	bg := palette[stableHash(seed)%uint32(len(palette))]
	initial := html.EscapeString(sanitizeInitial(name))
	svg := fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="120" height="120" viewBox="0 0 120 120">`+
			`<rect width="120" height="120" fill="%s"/>`+
			`<text x="60" y="60" font-family="sans-serif" font-size="60" fill="#ffffff" `+
			`text-anchor="middle" dominant-baseline="central">%s</text></svg>`,
		bg, initial)
	return []byte(svg)
}

// botObjectKey is the MinIO key chosen for a new bot upload; it is then stored
// verbatim in the avatars doc and used as-is on reads.
func botObjectKey(localPart string) string { return "bot/" + localPart }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./avatar-service/ -run 'TestIsBot|TestParseAccount|TestSanitizeInitial|TestRenderDefaultSVG|TestDefaultETag|TestBotObjectKey' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/avatar.go avatar-service/avatar_test.go
git commit -m "feat(avatar-service): deterministic default-SVG generator + account helpers"
```

---

## Task 3: Store interface + Mongo implementation

**Files:**
- Create: `avatar-service/store.go`, `avatar-service/store_mongo.go`
- Test: `avatar-service/integration_test.go`

- [ ] **Step 1: Write the store interface (no test yet — it's the contract)**

Create `avatar-service/store.go`:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

// avatarStore is the data access this service needs. Each method reads/writes
// exactly one collection: users, subscriptions, or avatars.
type avatarStore interface {
	// EmployeeID returns a user's employeeId (users collection). found=false when
	// the account has no user record or no employeeId.
	EmployeeID(ctx context.Context, account string) (eid string, found bool, err error)
	// IsPlatformAdmin reports whether the account's user record holds the admin role.
	IsPlatformAdmin(ctx context.Context, account string) (bool, error)
	// BotExists reports whether a bot's user record exists on this cluster.
	BotExists(ctx context.Context, account string) (bool, error)
	// RoomSite returns the room's owning site, type, and name from any one of its
	// local subscriptions. found=false when no local subscription exists.
	RoomSite(ctx context.Context, roomID string) (siteID string, roomType model.RoomType, name string, found bool, err error)
	// Avatar looks up a custom-image doc by subject. found=false → serve default.
	Avatar(ctx context.Context, subjectType model.AvatarSubjectType, subjectID string) (*model.Avatar, bool, error)
	// SetBotAvatar upserts a bot's avatars doc (by _id).
	SetBotAvatar(ctx context.Context, av *model.Avatar) error
}
```

- [ ] **Step 2: Write the failing integration test**

Create `avatar-service/integration_test.go`:

```go
//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTestsWithPrewarm(m, testutil.EnsureMongo, testutil.EnsureMinIO) }

func TestMongoStore_EmployeeID(t *testing.T) {
	db := testutil.MongoDB(t, "avatar")
	ctx := context.Background()
	_, err := db.Collection("users").InsertOne(ctx, model.User{ID: "u1", Account: "alice", EmployeeID: "E123"})
	require.NoError(t, err)
	st := newMongoStore(db)

	eid, found, err := st.EmployeeID(ctx, "alice")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "E123", eid)

	_, found, err = st.EmployeeID(ctx, "ghost")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestMongoStore_IsPlatformAdmin(t *testing.T) {
	db := testutil.MongoDB(t, "avatar")
	ctx := context.Background()
	_, err := db.Collection("users").InsertOne(ctx, model.User{ID: "u2", Account: "admin", Roles: []model.UserRole{model.UserRoleAdmin}})
	require.NoError(t, err)
	_, err = db.Collection("users").InsertOne(ctx, model.User{ID: "u3", Account: "joe", Roles: []model.UserRole{model.UserRoleUser}})
	require.NoError(t, err)
	st := newMongoStore(db)

	ok, err := st.IsPlatformAdmin(ctx, "admin")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = st.IsPlatformAdmin(ctx, "joe")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestMongoStore_BotExists(t *testing.T) {
	db := testutil.MongoDB(t, "avatar")
	ctx := context.Background()
	_, err := db.Collection("users").InsertOne(ctx, model.User{ID: "b1", Account: "helper.bot", SiteID: "s1"})
	require.NoError(t, err)
	st := newMongoStore(db)

	ok, err := st.BotExists(ctx, "helper.bot")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = st.BotExists(ctx, "ghost.bot")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestMongoStore_RoomSite(t *testing.T) {
	db := testutil.MongoDB(t, "avatar")
	ctx := context.Background()
	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "sub1", RoomID: "room-1", SiteID: "site-b", RoomType: model.RoomTypeChannel, Name: "General",
	})
	require.NoError(t, err)
	st := newMongoStore(db)

	site, rt, name, found, err := st.RoomSite(ctx, "room-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "site-b", site)
	assert.Equal(t, model.RoomTypeChannel, rt)
	assert.Equal(t, "General", name)

	_, _, _, found, err = st.RoomSite(ctx, "nope")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestMongoStore_AvatarAndSetBotAvatar(t *testing.T) {
	db := testutil.MongoDB(t, "avatar")
	ctx := context.Background()
	st := newMongoStore(db)

	_, found, err := st.Avatar(ctx, model.AvatarSubjectBot, "helper.bot")
	require.NoError(t, err)
	assert.False(t, found)

	av := &model.Avatar{
		ID: "bot:helper.bot", SubjectType: model.AvatarSubjectBot, SubjectID: "helper.bot",
		MinioKey: "bot/helper.bot", ContentType: "image/png", Size: 10, ETag: "e1",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.SetBotAvatar(ctx, av))

	got, found, err := st.Avatar(ctx, model.AvatarSubjectBot, "helper.bot")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "bot/helper.bot", got.MinioKey)

	// upsert overwrites in place
	av.ETag = "e2"
	require.NoError(t, st.SetBotAvatar(ctx, av))
	got, _, err = st.Avatar(ctx, model.AvatarSubjectBot, "helper.bot")
	require.NoError(t, err)
	assert.Equal(t, "e2", got.ETag)

	var count int64
	count, err = db.Collection("avatars").CountDocuments(ctx, bson.M{"_id": "bot:helper.bot"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `make test-integration SERVICE=avatar-service`
Expected: FAIL — `undefined: newMongoStore`.

- [ ] **Step 4: Write minimal implementation**

Create `avatar-service/store_mongo.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

type mongoStore struct {
	users         *mongo.Collection
	subscriptions *mongo.Collection
	avatars       *mongo.Collection
}

func newMongoStore(db *mongo.Database) *mongoStore {
	return &mongoStore{
		users:         db.Collection("users"),
		subscriptions: db.Collection("subscriptions"),
		avatars:       db.Collection("avatars"),
	}
}

func (s *mongoStore) EmployeeID(ctx context.Context, account string) (string, bool, error) {
	var u model.User
	err := s.users.FindOne(ctx, bson.M{"account": account},
		options.FindOne().SetProjection(bson.M{"employeeId": 1})).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find employeeId: %w", err)
	}
	if u.EmployeeID == "" {
		return "", false, nil
	}
	return u.EmployeeID, true, nil
}

func (s *mongoStore) IsPlatformAdmin(ctx context.Context, account string) (bool, error) {
	var u model.User
	err := s.users.FindOne(ctx, bson.M{"account": account},
		options.FindOne().SetProjection(bson.M{"roles": 1})).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find user roles: %w", err)
	}
	return model.IsPlatformAdmin(&u), nil
}

func (s *mongoStore) BotExists(ctx context.Context, account string) (bool, error) {
	err := s.users.FindOne(ctx, bson.M{"account": account},
		options.FindOne().SetProjection(bson.M{"_id": 1})).Decode(&struct{}{})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find bot: %w", err)
	}
	return true, nil
}

func (s *mongoStore) RoomSite(ctx context.Context, roomID string) (string, model.RoomType, string, bool, error) {
	var sub model.Subscription
	err := s.subscriptions.FindOne(ctx, bson.M{"roomId": roomID},
		options.FindOne().SetProjection(bson.M{"siteId": 1, "roomType": 1, "name": 1})).Decode(&sub)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, fmt.Errorf("find room subscription: %w", err)
	}
	return sub.SiteID, sub.RoomType, sub.Name, true, nil
}

func (s *mongoStore) Avatar(ctx context.Context, st model.AvatarSubjectType, subjectID string) (*model.Avatar, bool, error) {
	id := string(st) + ":" + subjectID
	var av model.Avatar
	err := s.avatars.FindOne(ctx, bson.M{"_id": id}).Decode(&av)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find avatar: %w", err)
	}
	return &av, true, nil
}

func (s *mongoStore) SetBotAvatar(ctx context.Context, av *model.Avatar) error {
	_, err := s.avatars.ReplaceOne(ctx, bson.M{"_id": av.ID}, av, options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert bot avatar: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Generate the mock**

Run: `make generate SERVICE=avatar-service`
Expected: creates `avatar-service/mock_store_test.go` (mock of `avatarStore`). It compiles against `package main`.

- [ ] **Step 6: Run integration tests to verify they pass**

Run: `make test-integration SERVICE=avatar-service`
Expected: PASS (all `TestMongoStore_*`).

- [ ] **Step 7: Commit**

```bash
git add avatar-service/store.go avatar-service/store_mongo.go avatar-service/mock_store_test.go avatar-service/integration_test.go
git commit -m "feat(avatar-service): avatarStore interface + Mongo implementation"
```

---

## Task 4: MinIO blob seam (`minio.go`)

**Files:**
- Create: `avatar-service/minio.go`
- Test: `avatar-service/integration_test.go` (append)

- [ ] **Step 1: Write the failing integration test**

Append to `avatar-service/integration_test.go`:

```go
func TestMinioBlobStore_PutGet(t *testing.T) {
	client, bucket := testutil.MinIO(t, "avatar")
	ctx := context.Background()
	bs := newMinioBlobStore(client, bucket)

	etag, err := bs.Put(ctx, "bot/x", strings.NewReader("PNGDATA"), int64(len("PNGDATA")), "image/png")
	require.NoError(t, err)
	assert.NotEmpty(t, etag)

	rc, info, err := bs.Get(ctx, "bot/x")
	require.NoError(t, err)
	defer rc.Close()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "PNGDATA", string(body))
	assert.Equal(t, "image/png", info.ContentType)
	assert.Equal(t, int64(7), info.Size)
	assert.NotEmpty(t, info.ETag)
}

func TestMinioBlobStore_GetMissing(t *testing.T) {
	client, bucket := testutil.MinIO(t, "avatar")
	bs := newMinioBlobStore(client, bucket)
	_, _, err := bs.Get(context.Background(), "bot/missing")
	assert.ErrorIs(t, err, errBlobNotFound)
}
```

Add imports `io` and `strings` to the integration test file's import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-integration SERVICE=avatar-service`
Expected: FAIL — `undefined: newMinioBlobStore` / `errBlobNotFound`.

- [ ] **Step 3: Write minimal implementation**

Create `avatar-service/minio.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
)

// errBlobNotFound is returned by blobStore.Get when the object does not exist —
// the read path treats it as "doc/object inconsistency" and serves the default.
var errBlobNotFound = errors.New("blob not found")

type blobInfo struct {
	Size        int64
	ContentType string
	ETag        string
}

// blobStore is the object-storage seam the handler depends on (so handler tests
// need no real MinIO).
type blobStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, blobInfo, error)
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (etag string, err error)
}

type minioBlobStore struct {
	client *minio.Client
	bucket string
}

func newMinioBlobStore(client *minio.Client, bucket string) *minioBlobStore {
	return &minioBlobStore{client: client, bucket: bucket}
}

func (m *minioBlobStore) Get(ctx context.Context, key string) (io.ReadCloser, blobInfo, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, blobInfo{}, fmt.Errorf("get object: %w", err)
	}
	st, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, blobInfo{}, errBlobNotFound
		}
		return nil, blobInfo{}, fmt.Errorf("stat object: %w", err)
	}
	return obj, blobInfo{Size: st.Size, ContentType: st.ContentType, ETag: st.ETag}, nil
}

func (m *minioBlobStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	info, err := m.client.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("put object: %w", err)
	}
	return info.ETag, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test-integration SERVICE=avatar-service`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/minio.go avatar-service/integration_test.go
git commit -m "feat(avatar-service): MinIO blob seam (Get with NotFound sentinel, Put)"
```

---

## Task 5: Config + helpers + employeeID cache

**Files:**
- Create: `avatar-service/config.go`, `avatar-service/cache.go`
- Test: `avatar-service/config_test.go`, `avatar-service/cache_test.go`

- [ ] **Step 1: Write the failing tests**

Create `avatar-service/config_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClusterBaseURL(t *testing.T) {
	c := config{ClusterDomains: map[string]string{"xxx-2": "https://avatar-2"}}
	assert.Equal(t, "https://avatar-2", c.clusterBaseURL("xxx-2"))
	assert.Equal(t, "", c.clusterBaseURL("unknown"))
}

func TestResolveBotSite(t *testing.T) {
	c := config{DomainSites: map[string]string{"site2.example.com": "xxx-2"}}
	assert.Equal(t, "xxx-2", c.resolveBotSite("site2.example.com"))
	assert.Equal(t, "", c.resolveBotSite(""))            // no domain → local
	assert.Equal(t, "", c.resolveBotSite("unknown.com")) // unknown → local
}
```

Create `avatar-service/cache_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTTLCache_GetPutExpire(t *testing.T) {
	c := newTTLCache(2, 50*time.Millisecond)
	c.Put("a", "1")
	v, ok := c.Get("a")
	assert.True(t, ok)
	assert.Equal(t, "1", v)

	time.Sleep(60 * time.Millisecond)
	_, ok = c.Get("a")
	assert.False(t, ok, "entry must expire after TTL")
}

func TestTTLCache_CapacityBound(t *testing.T) {
	c := newTTLCache(2, time.Minute)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Put("c", "3") // exceeds capacity → cache must not grow unbounded
	assert.LessOrEqual(t, c.len(), 2)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./avatar-service/ -run 'TestClusterBaseURL|TestResolveBotSite|TestTTLCache' -v`
Expected: FAIL — undefined `config`, `newTTLCache`.

- [ ] **Step 3: Write minimal implementations**

Create `avatar-service/config.go`:

```go
package main

type config struct {
	Port     string `env:"PORT" envDefault:"8080"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	SiteID   string `env:"SITE_ID,required"`

	// CLUSTER_DOMAINS maps siteID → that cluster's avatar-service base URL
	// (incl. scheme), used verbatim as a redirect target.
	ClusterDomains map[string]string `env:"CLUSTER_DOMAINS,required" envKeyValSeparator:"=" envSeparator:","`
	// DOMAIN_SITES maps a bot account's @domain → siteID (resolves the spec's
	// unspecified domain→site mapping; see plan header).
	DomainSites map[string]string `env:"DOMAIN_SITES" envKeyValSeparator:"=" envSeparator:","`

	EmployeePhotoBaseURL string `env:"EMPLOYEE_PHOTO_BASE_URL,required"`

	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB" envDefault:"chat"`
	MongoUsername string `env:"MONGO_USERNAME"`
	MongoPassword string `env:"MONGO_PASSWORD"`

	OIDCIssuerURL string   `env:"OIDC_ISSUER_URL"`
	OIDCAudiences []string `env:"OIDC_AUDIENCES" envSeparator:","`
	DevMode       bool     `env:"DEV_MODE" envDefault:"false"`
	TLSSkipVerify bool     `env:"TLS_SKIP_VERIFY" envDefault:"false"`

	MinioEndpoint  string `env:"MINIO_ENDPOINT,required"`
	MinioAccessKey string `env:"MINIO_ACCESS_KEY,required"`
	MinioSecretKey string `env:"MINIO_SECRET_KEY,required"`
	MinioUseSSL    bool   `env:"MINIO_USE_SSL" envDefault:"false"`
	AvatarBucket   string `env:"AVATAR_BUCKET" envDefault:"avatars"`

	MaxUploadBytes     int64 `env:"MAX_UPLOAD_BYTES" envDefault:"1048576"`
	CacheMaxAgeSeconds int   `env:"CACHE_MAX_AGE_SECONDS" envDefault:"21600"`
}

// clusterBaseURL returns the configured base URL for a site, or "" if unknown.
func (c config) clusterBaseURL(siteID string) string { return c.ClusterDomains[siteID] }

// resolveBotSite maps a bot account's @domain to its owning siteID. "" means
// "treat as local" (no domain, or unknown domain).
func (c config) resolveBotSite(domain string) string {
	if domain == "" {
		return ""
	}
	return c.DomainSites[domain]
}
```

Create `avatar-service/cache.go`:

```go
package main

import (
	"sync"
	"time"
)

type cacheEntry struct {
	val string
	exp time.Time
}

// ttlCache is a tiny thread-safe cache with a TTL and a hard capacity. When the
// capacity is exceeded it drops all entries (simple bounded behaviour — the
// cache is only an accelerator). Stores positive lookups only.
type ttlCache struct {
	mu   sync.Mutex
	m    map[string]cacheEntry
	cap  int
	ttl  time.Duration
}

func newTTLCache(capacity int, ttl time.Duration) *ttlCache {
	return &ttlCache{m: make(map[string]cacheEntry), cap: capacity, ttl: ttl}
}

func (c *ttlCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.exp) {
		return "", false
	}
	return e.val, true
}

func (c *ttlCache) Put(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.cap {
		c.m = make(map[string]cacheEntry, c.cap)
	}
	c.m[key] = cacheEntry{val: val, exp: time.Now().Add(c.ttl)}
}

func (c *ttlCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./avatar-service/ -run 'TestClusterBaseURL|TestResolveBotSite|TestTTLCache' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/config.go avatar-service/cache.go avatar-service/config_test.go avatar-service/cache_test.go
git commit -m "feat(avatar-service): config + cluster/bot-site helpers + ttl cache"
```

---

## Task 6: Middleware + routes + healthz + handler skeleton + main

**Files:**
- Create: `avatar-service/middleware.go`, `avatar-service/routes.go`, `avatar-service/handler.go`, `avatar-service/main.go`
- Test: `avatar-service/handler_test.go`

- [ ] **Step 1: Write the failing test (healthz through the gin stack)**

Create `avatar-service/handler_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func newTestRouter(t *testing.T) (*gin.Engine, *MockavatarStore, *fakeBlobStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	store := NewMockavatarStore(ctrl)
	blobs := &fakeBlobStore{}
	h := newHandler(store, blobs, config{
		SiteID:               "s1",
		EmployeePhotoBaseURL: "https://photos.example.com",
		CacheMaxAgeSeconds:   3600,
		AvatarBucket:         "avatars",
		ClusterDomains:       map[string]string{"s2": "https://avatar-s2"},
		DomainSites:          map[string]string{"site2.example.com": "s2"},
	})
	r := gin.New()
	registerRoutes(r, h)
	return r, store, blobs
}

func TestHandleHealth(t *testing.T) {
	r, _, _ := newTestRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}
```

Also create the shared test fake — add to `handler_test.go`:

```go
import (
	"bytes"
	"context"
	"io"
)

// fakeBlobStore is an in-memory blobStore for handler tests.
type fakeBlobStore struct {
	objects map[string][]byte // key → bytes
	info    map[string]blobInfo
	putErr  error
	getErr  error
}

func (f *fakeBlobStore) Get(_ context.Context, key string) (io.ReadCloser, blobInfo, error) {
	if f.getErr != nil {
		return nil, blobInfo{}, f.getErr
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, blobInfo{}, errBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), f.info[key], nil
}

func (f *fakeBlobStore) Put(_ context.Context, key string, r io.Reader, _ int64, ct string) (string, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	if f.objects == nil {
		f.objects = map[string][]byte{}
		f.info = map[string]blobInfo{}
	}
	b, _ := io.ReadAll(r)
	f.objects[key] = b
	f.info[key] = blobInfo{Size: int64(len(b)), ContentType: ct, ETag: "etag-" + key}
	return "etag-" + key, nil
}
```

(Combine all imports into one block per Go rules.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./avatar-service/ -run TestHandleHealth -v`
Expected: FAIL — `undefined: newHandler`, `registerRoutes`, `NewMockavatarStore` (mock already generated in Task 3; `newHandler`/`registerRoutes` are not).

- [ ] **Step 3: Write minimal implementations**

Create `avatar-service/handler.go`:

```go
package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type handler struct {
	store    avatarStore
	blobs    blobStore
	cfg      config
	eidCache *ttlCache
}

func newHandler(store avatarStore, blobs blobStore, cfg config) *handler {
	return &handler{
		store:    store,
		blobs:    blobs,
		cfg:      cfg,
		eidCache: newTTLCache(50000, 10*time.Minute),
	}
}

func (h *handler) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
```

(Add `"time"` to the import block.)

Create `avatar-service/routes.go`:

```go
package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *handler) {
	r.GET("/healthz", h.HandleHealth)
	// read endpoints (Task 7/8) and upload (Task 10) are registered below as
	// they are implemented.
}
```

Create `avatar-service/middleware.go` (mirrors auth-service):

```go
package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
)

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

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
		c.Header("Access-Control-Max-Age", "300")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// accessLogMiddleware logs one structured line per request, including the typed
// avatar outcome set by the read handlers (kind + outcome).
func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.InfoContext(c.Request.Context(), "request",
			"request_id", c.GetString("request_id"),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
			"avatar_kind", c.GetString("avatar_kind"),
			"avatar_outcome", c.GetString("avatar_outcome"),
		)
	}
}
```

Create `avatar-service/main.go`:

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

	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
	"github.com/hmchangw/chat/pkg/minioutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("avatar-service exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()

	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		return fmt.Errorf("connect mongo: %w", err)
	}
	store := newMongoStore(mongoClient.Database(cfg.MongoDB))

	minioClient, err := minioutil.Connect(ctx, cfg.MinioEndpoint, cfg.MinioUseSSL, cfg.MinioAccessKey, cfg.MinioSecretKey)
	if err != nil {
		return fmt.Errorf("connect minio: %w", err)
	}
	blobs := newMinioBlobStore(minioClient, cfg.AvatarBucket)

	var validator *pkgoidc.Validator
	if !cfg.DevMode {
		if cfg.OIDCIssuerURL == "" || len(cfg.OIDCAudiences) == 0 {
			return fmt.Errorf("OIDC_ISSUER_URL and OIDC_AUDIENCES are required when DEV_MODE is false")
		}
		validator, err = pkgoidc.NewValidator(ctx, pkgoidc.Config{
			IssuerURL:     cfg.OIDCIssuerURL,
			Audiences:     cfg.OIDCAudiences,
			TLSSkipVerify: cfg.TLSSkipVerify,
		})
		if err != nil {
			return fmt.Errorf("create oidc validator: %w", err)
		}
	}

	h := newHandler(store, blobs, cfg)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestIDMiddleware())
	r.Use(accessLogMiddleware())
	r.Use(corsMiddleware())
	registerRoutes(r, h)
	registerUploadRoutes(r, h, validator, cfg) // defined in upload.go (Task 10)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		slog.Info("avatar-service listening", "port", cfg.Port, "site", cfg.SiteID)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	shutdown.Wait(ctx, 25*time.Second, func(ctx context.Context) error {
		slog.Info("shutting down avatar-service")
		return srv.Shutdown(ctx)
	})
	return nil
}
```

> NOTE: `registerUploadRoutes` is created in Task 10. Until then, to keep the build green, temporarily add a stub at the bottom of `routes.go`:
> ```go
> func registerUploadRoutes(_ *gin.Engine, _ *handler, _ interface{}, _ config) {}
> ```
> Task 10 replaces this stub with the real signature.

- [ ] **Step 4: Run test + build to verify they pass**

Run: `go test ./avatar-service/ -run TestHandleHealth -v` → PASS
Run: `make build SERVICE=avatar-service` → builds `bin/avatar-service`.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/handler.go avatar-service/routes.go avatar-service/middleware.go avatar-service/main.go avatar-service/handler_test.go
git commit -m "feat(avatar-service): service skeleton — gin, middleware, healthz, wiring"
```

---

## Task 7: Endpoint 1 — `GET /avatar/v1/:accountName` (user + bot)

**Files:**
- Modify: `avatar-service/handler.go`, `avatar-service/routes.go`
- Test: `avatar-service/handler_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `avatar-service/handler_test.go`:

```go
func TestEndpoint1_UserRedirectToEmployeePhoto(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().EmployeeID(gomock.Any(), "alice").Return("E123", true, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/alice", nil))

	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Equal(t, "https://photos.example.com/xxxPhoto/po/E123_120.JPG", w.Header().Get("Location"))
}

func TestEndpoint1_UserNoEmployeeID_ServesDefault(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().EmployeeID(gomock.Any(), "alice").Return("", false, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/alice", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, w.Body.String(), "<svg")
}

func TestEndpoint1_BotLocalCustomImage_Streams(t *testing.T) {
	r, store, blobs := newTestRouter(t)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectBot, "helper.bot").
		Return(&model.Avatar{MinioKey: "bot/helper.bot", ContentType: "image/png", Size: 3, ETag: `"e1"`}, true, nil)
	blobs.objects = map[string][]byte{"bot/helper.bot": []byte("PNG")}
	blobs.info = map[string]blobInfo{"bot/helper.bot": {Size: 3, ContentType: "image/png", ETag: `"e1"`}}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
	assert.Equal(t, "PNG", w.Body.String())
}

func TestEndpoint1_BotCustomImage_NotModified(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectBot, "helper.bot").
		Return(&model.Avatar{MinioKey: "bot/helper.bot", ETag: `"e1"`}, true, nil)

	req := httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot", nil)
	req.Header.Set("If-None-Match", `"e1"`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotModified, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestEndpoint1_BotNoCustomImage_ServesDefault(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectBot, "helper.bot").Return(nil, false, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
}

func TestEndpoint1_BotRemoteCluster_Redirects(t *testing.T) {
	r, _, _ := newTestRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot@site2.example.com", nil))

	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Equal(t, "https://avatar-s2/avatar/v1/helper.bot@site2.example.com?fwd=1", w.Header().Get("Location"))
}

func TestEndpoint1_BotRemoteWithFwd_NoReRedirect(t *testing.T) {
	r, store, _ := newTestRouter(t)
	// fwd=1 means "resolve locally": even though domain≠local, do not redirect again.
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectBot, "helper.bot").Return(nil, false, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot@site2.example.com?fwd=1", nil))
	assert.Equal(t, http.StatusOK, w.Code) // served default locally
}
```

Add `"github.com/hmchangw/chat/pkg/model"` to the test import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./avatar-service/ -run TestEndpoint1 -v`
Expected: FAIL — route not registered (404).

- [ ] **Step 3: Write minimal implementation**

Add to `avatar-service/handler.go`:

```go
import (
	"fmt"
	"net/url"
	// existing: net/http, time, gin
	"github.com/hmchangw/chat/pkg/model"
)

const requestForwardedParam = "fwd"

// setImageCacheHeaders applies the common cache + safety headers.
func (h *handler) setImageCacheHeaders(c *gin.Context, etag string) {
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", h.cfg.CacheMaxAgeSeconds))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'")
	if etag != "" {
		c.Header("ETag", etag)
	}
}

// serveDefault generates and writes a deterministic initials SVG (with a 304
// fast path against its deterministic ETag).
func (h *handler) serveDefault(c *gin.Context, kind, seed, name string) {
	c.Set("avatar_kind", kind)
	c.Set("avatar_outcome", "default")
	etag := defaultETag(seed, name)
	h.setImageCacheHeaders(c, etag)
	c.Header("Content-Type", "image/svg+xml")
	if match := c.GetHeader("If-None-Match"); match == etag {
		c.Set("avatar_outcome", "304")
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "image/svg+xml", renderDefaultSVG(seed, name))
}

// serveStored serves a stored MinIO image referenced by the avatars doc. The 304
// is answered from av.ETag with no MinIO call.
func (h *handler) serveStored(c *gin.Context, kind string, av *model.Avatar, fallbackSeed, fallbackName string) {
	c.Set("avatar_kind", kind)
	h.setImageCacheHeaders(c, av.ETag)
	if match := c.GetHeader("If-None-Match"); match != "" && match == av.ETag {
		c.Set("avatar_outcome", "304")
		c.Status(http.StatusNotModified)
		return
	}
	rc, info, err := h.blobs.Get(c.Request.Context(), av.MinioKey)
	if err == errBlobNotFound {
		h.serveDefault(c, kind, fallbackSeed, fallbackName)
		return
	}
	if err != nil {
		c.Set("avatar_outcome", "error")
		_ = c.Error(fmt.Errorf("get avatar object: %w", err))
		c.Status(http.StatusInternalServerError)
		return
	}
	defer rc.Close()
	c.Set("avatar_outcome", "stream")
	c.DataFromReader(http.StatusOK, info.Size, info.ContentType, rc, nil)
}

func (h *handler) HandleAccountAvatar(c *gin.Context) {
	account := c.Param("accountName")
	localPart, domain := parseAccount(account)
	fwd := c.Query(requestForwardedParam) != ""
	ctx := c.Request.Context()

	if isBot(localPart) {
		if owning := h.cfg.resolveBotSite(domain); owning != "" && owning != h.cfg.SiteID && !fwd {
			if base := h.cfg.clusterBaseURL(owning); base != "" {
				c.Set("avatar_kind", "bot")
				c.Set("avatar_outcome", "redirect")
				c.Redirect(http.StatusTemporaryRedirect,
					fmt.Sprintf("%s/avatar/v1/%s?fwd=1", base, url.PathEscape(account)))
				return
			}
			// unknown site → fall through to default (never dead-end)
		}
		av, found, err := h.store.Avatar(ctx, model.AvatarSubjectBot, localPart)
		if err != nil {
			c.Set("avatar_outcome", "error")
			_ = c.Error(err)
			c.Status(http.StatusInternalServerError)
			return
		}
		if found {
			h.serveStored(c, "bot", av, localPart, localPart)
			return
		}
		h.serveDefault(c, "bot", localPart, localPart)
		return
	}

	// user (synced everywhere; domain informational)
	eid, ok := h.eidCache.Get(localPart)
	if !ok {
		var found bool
		var err error
		eid, found, err = h.store.EmployeeID(ctx, localPart)
		if err != nil {
			c.Set("avatar_outcome", "error")
			_ = c.Error(err)
			c.Status(http.StatusInternalServerError)
			return
		}
		if !found {
			h.serveDefault(c, "user", localPart, localPart)
			return
		}
		h.eidCache.Put(localPart, eid)
	}
	c.Set("avatar_kind", "user")
	c.Set("avatar_outcome", "redirect")
	c.Redirect(http.StatusTemporaryRedirect,
		fmt.Sprintf("%s/xxxPhoto/po/%s_120.JPG", h.cfg.EmployeePhotoBaseURL, url.PathEscape(eid)))
}
```

Register the route — modify `avatar-service/routes.go`:

```go
func registerRoutes(r *gin.Engine, h *handler) {
	r.GET("/healthz", h.HandleHealth)
	r.GET("/avatar/v1/:accountName", h.HandleAccountAvatar)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./avatar-service/ -run 'TestEndpoint1|TestHandleHealth' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/handler.go avatar-service/routes.go avatar-service/handler_test.go
git commit -m "feat(avatar-service): Endpoint 1 — user/bot avatar resolve, redirect, stream, default"
```

---

## Task 8: Endpoint 2 — `GET /avatar/v1/room/:roomID`

**Files:**
- Modify: `avatar-service/handler.go`, `avatar-service/routes.go`
- Test: `avatar-service/handler_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `avatar-service/handler_test.go`:

```go
func TestEndpoint2_ChannelCustomImage_Streams(t *testing.T) {
	r, store, blobs := newTestRouter(t)
	store.EXPECT().RoomSite(gomock.Any(), "room-1").Return("s1", model.RoomTypeChannel, "General", true, nil)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectRoom, "room-1").
		Return(&model.Avatar{MinioKey: "room/room-1", ContentType: "image/png", Size: 3, ETag: `"r1"`}, true, nil)
	blobs.objects = map[string][]byte{"room/room-1": []byte("PNG")}
	blobs.info = map[string]blobInfo{"room/room-1": {Size: 3, ContentType: "image/png", ETag: `"r1"`}}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/room/room-1", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "PNG", w.Body.String())
}

func TestEndpoint2_ChannelNoCustomImage_Default(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().RoomSite(gomock.Any(), "room-1").Return("s1", model.RoomTypeChannel, "General", true, nil)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectRoom, "room-1").Return(nil, false, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/room/room-1", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
}

func TestEndpoint2_NotFound_Default(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().RoomSite(gomock.Any(), "room-x").Return("", model.RoomType(""), "", false, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/room/room-x", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
}

func TestEndpoint2_DMType_Default(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().RoomSite(gomock.Any(), "dm-1").Return("s1", model.RoomTypeDM, "", true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/room/dm-1", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
}

func TestEndpoint2_RemoteCluster_Redirects(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().RoomSite(gomock.Any(), "room-1").Return("s2", model.RoomTypeChannel, "General", true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/room/room-1", nil))
	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Equal(t, "https://avatar-s2/avatar/v1/room/room-1?fwd=1", w.Header().Get("Location"))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./avatar-service/ -run TestEndpoint2 -v`
Expected: FAIL — route not registered.

- [ ] **Step 3: Write minimal implementation**

Add to `avatar-service/handler.go`:

```go
func (h *handler) HandleRoomAvatar(c *gin.Context) {
	roomID := c.Param("roomID")
	fwd := c.Query(requestForwardedParam) != ""
	ctx := c.Request.Context()

	siteID, roomType, name, found, err := h.store.RoomSite(ctx, roomID)
	if err != nil {
		c.Set("avatar_outcome", "error")
		_ = c.Error(err)
		c.Status(http.StatusInternalServerError)
		return
	}
	if !found {
		h.serveDefault(c, "room", roomID, roomID)
		return
	}
	if roomType == model.RoomTypeDM || roomType == model.RoomTypeBotDM {
		h.serveDefault(c, "room", roomID, name)
		return
	}
	if siteID != h.cfg.SiteID && !fwd {
		if base := h.cfg.clusterBaseURL(siteID); base != "" {
			c.Set("avatar_kind", "room")
			c.Set("avatar_outcome", "redirect")
			c.Redirect(http.StatusTemporaryRedirect,
				fmt.Sprintf("%s/avatar/v1/room/%s?fwd=1", base, url.PathEscape(roomID)))
			return
		}
		// unknown site → default
	}
	av, found, err := h.store.Avatar(ctx, model.AvatarSubjectRoom, roomID)
	if err != nil {
		c.Set("avatar_outcome", "error")
		_ = c.Error(err)
		c.Status(http.StatusInternalServerError)
		return
	}
	if found {
		h.serveStored(c, "room", av, roomID, name)
		return
	}
	h.serveDefault(c, "room", roomID, name)
}
```

Register the route — modify `avatar-service/routes.go`:

```go
	r.GET("/avatar/v1/room/:roomID", h.HandleRoomAvatar)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./avatar-service/ -run TestEndpoint2 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/handler.go avatar-service/routes.go avatar-service/handler_test.go
git commit -m "feat(avatar-service): Endpoint 2 — room avatar via subscription, redirect, stream, default"
```

---

## Task 9: Upload auth middleware (OIDC + platform-admin)

**Files:**
- Modify: `avatar-service/middleware.go`
- Test: `avatar-service/middleware_test.go`

- [ ] **Step 1: Write the failing tests**

Create `avatar-service/middleware_test.go`:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

// stubVerifier lets tests inject account/err without a real OIDC issuer.
type stubVerifier struct {
	account string
	err     error
}

func (s stubVerifier) accountFromBearer(_ context.Context, _ string) (string, error) {
	return s.account, s.err
}

func TestAdminAuth_NoToken_401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	store := NewMockavatarStore(ctrl)
	r := gin.New()
	r.PUT("/x", adminAuthMiddleware(stubVerifier{}, store, false), func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/x", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminAuth_NonAdmin_403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	store := NewMockavatarStore(ctrl)
	store.EXPECT().IsPlatformAdmin(gomock.Any(), "joe").Return(false, nil)
	r := gin.New()
	r.PUT("/x", adminAuthMiddleware(stubVerifier{account: "joe"}, store, false), func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/x", nil)
	req.Header.Set("Authorization", "Bearer t")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAdminAuth_Admin_PassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	store := NewMockavatarStore(ctrl)
	store.EXPECT().IsPlatformAdmin(gomock.Any(), "boss").Return(true, nil)
	r := gin.New()
	r.PUT("/x", adminAuthMiddleware(stubVerifier{account: "boss"}, store, false), func(c *gin.Context) { c.Status(204) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/x", nil)
	req.Header.Set("Authorization", "Bearer t")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "boss", w.Header().Get("X-Test-Account")) // see middleware: sets context only; this asserts pass-through via handler
}
```

Note: the last assertion on `X-Test-Account` requires the test handler to echo it. Simplify the final assert to just `assert.Equal(t, http.StatusNoContent, w.Code)` and drop the header line (the pass-through is proven by reaching the 204 handler).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./avatar-service/ -run TestAdminAuth -v`
Expected: FAIL — `undefined: adminAuthMiddleware`, `accountVerifier`.

- [ ] **Step 3: Write minimal implementation**

Add to `avatar-service/middleware.go`:

```go
import (
	"context"
	"strings"
	// existing imports …
	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
)

// accountVerifier extracts an authenticated account from a bearer token. The
// production impl wraps pkg/oidc; tests inject a stub.
type accountVerifier interface {
	accountFromBearer(ctx context.Context, raw string) (account string, err error)
}

const ctxAccountKey = "account"

// adminAuthMiddleware authenticates the OIDC bearer token and requires the
// caller to be a platform admin. devMode bypasses authentication.
func adminAuthMiddleware(v accountVerifier, store avatarStore, devMode bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var account string
		if devMode {
			account = "dev-admin"
		} else {
			raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			if raw == "" || raw == c.GetHeader("Authorization") { // missing or no "Bearer " prefix
				errhttp.Write(ctx, c, errcode.Unauthenticated("missing bearer token"))
				c.Abort()
				return
			}
			acc, err := v.accountFromBearer(ctx, raw)
			if err != nil {
				errhttp.Write(ctx, c, errcode.Unauthenticated("invalid token"))
				c.Abort()
				return
			}
			account = acc
		}
		if !devMode {
			admin, err := store.IsPlatformAdmin(ctx, account)
			if err != nil {
				errhttp.Write(ctx, c, err)
				c.Abort()
				return
			}
			if !admin {
				errhttp.Write(ctx, c, errcode.Forbidden("platform-admin role required"))
				c.Abort()
				return
			}
		}
		c.Set(ctxAccountKey, account)
		c.Next()
	}
}
```

Also create the production verifier — add to `avatar-service/upload.go` is fine, but since it wraps oidc, put it in `middleware.go`:

```go
import pkgoidc "github.com/hmchangw/chat/pkg/oidc"

type oidcVerifier struct{ v *pkgoidc.Validator }

func (o oidcVerifier) accountFromBearer(ctx context.Context, raw string) (string, error) {
	claims, err := o.v.Validate(ctx, raw)
	if err != nil {
		return "", err
	}
	account := claims.PreferredUsername
	if account == "" {
		account = claims.Name
	}
	if account == "" {
		return "", errcode.Unauthenticated("token has no account claim")
	}
	return account, nil
}
```

- [ ] **Step 4: Run tests to verify they pass** (after simplifying the last assert per the note)

Run: `go test ./avatar-service/ -run TestAdminAuth -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/middleware.go avatar-service/middleware_test.go
git commit -m "feat(avatar-service): OIDC + platform-admin upload auth middleware"
```

---

## Task 10: Bot upload handler (`upload.go`)

**Files:**
- Create: `avatar-service/upload.go`
- Modify: `avatar-service/routes.go` (replace the `registerUploadRoutes` stub), `avatar-service/main.go` (wrap the validator)
- Test: `avatar-service/upload_test.go`

- [ ] **Step 1: Write the failing tests**

Create `avatar-service/upload_test.go`:

```go
package main

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return buf.Bytes()
}

// newUploadRouter wires only the upload route with devMode auth bypass.
func newUploadRouter(t *testing.T) (*gin.Engine, *MockavatarStore, *fakeBlobStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	store := NewMockavatarStore(ctrl)
	blobs := &fakeBlobStore{}
	h := newHandler(store, blobs, config{SiteID: "s1", AvatarBucket: "avatars",
		DomainSites: map[string]string{"site2.example.com": "s2"}, ClusterDomains: map[string]string{"s2": "https://avatar-s2"},
		MaxUploadBytes: 1 << 20})
	r := gin.New()
	registerUploadRoutes(r, h, stubVerifier{account: "dev"}, config{DevMode: true})
	return r, store, blobs
}

func putReq(method, path string, body []byte, ct string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	return req
}

func TestUpload_MalformedBotName_400(t *testing.T) {
	r, _, _ := newUploadRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq(http.MethodPut, "/avatar/v1/bot/alice", pngBytes(t), "image/png")) // not a bot
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpload_WrongCluster_RejectsWithDomain(t *testing.T) {
	r, _, _ := newUploadRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq(http.MethodPut, "/avatar/v1/bot/helper.bot@site2.example.com", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "https://avatar-s2")
}

func TestUpload_UnknownBot_404(t *testing.T) {
	r, store, _ := newUploadRouter(t)
	store.EXPECT().BotExists(gomock.Any(), "helper.bot").Return(false, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq(http.MethodPut, "/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpload_RejectSVG(t *testing.T) {
	r, store, _ := newUploadRouter(t)
	store.EXPECT().BotExists(gomock.Any(), "helper.bot").Return(true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq(http.MethodPut, "/avatar/v1/bot/helper.bot", []byte("<svg/>"), "image/svg+xml"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpload_RejectNonImage(t *testing.T) {
	r, store, _ := newUploadRouter(t)
	store.EXPECT().BotExists(gomock.Any(), "helper.bot").Return(true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq(http.MethodPut, "/avatar/v1/bot/helper.bot", []byte("not an image"), "image/png"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpload_Success_StoresAndUpserts(t *testing.T) {
	r, store, blobs := newUploadRouter(t)
	store.EXPECT().BotExists(gomock.Any(), "helper.bot").Return(true, nil)
	store.EXPECT().SetBotAvatar(gomock.Any(), gomock.Any()).DoAndReturn(func(_ any, av *model.Avatar) error {
		assert.Equal(t, "bot:helper.bot", av.ID)
		assert.Equal(t, "bot/helper.bot", av.MinioKey)
		assert.Equal(t, "image/png", av.ContentType)
		assert.NotEmpty(t, av.ETag)
		return nil
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq(http.MethodPut, "/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	_, ok := blobs.objects["bot/helper.bot"]
	assert.True(t, ok, "object must be stored in MinIO before the doc")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./avatar-service/ -run TestUpload -v`
Expected: FAIL — `undefined: registerUploadRoutes` (real one) / handler.

- [ ] **Step 3: Write minimal implementation**

Create `avatar-service/upload.go`:

```go
package main

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	_ "image/jpeg" // register decoders
	_ "image/png"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errhttp"
	"github.com/hmchangw/chat/pkg/model"
)

// registerUploadRoutes wires PUT /avatar/v1/bot/:botName behind admin auth.
func registerUploadRoutes(r *gin.Engine, h *handler, v accountVerifier, cfg config) {
	r.PUT("/avatar/v1/bot/:botName",
		adminAuthMiddleware(v, h.store, cfg.DevMode),
		h.HandleBotUpload)
}

func (h *handler) HandleBotUpload(c *gin.Context) {
	ctx := c.Request.Context()
	localPart, domain := parseAccount(c.Param("botName"))

	if !isBot(localPart) {
		errhttp.Write(ctx, c, errcode.BadRequest("not a bot account"))
		return
	}

	// Wrong cluster, with a hint → reject with the correct domain.
	if owning := h.cfg.resolveBotSite(domain); owning != "" && owning != h.cfg.SiteID {
		base := h.cfg.clusterBaseURL(owning)
		errhttp.Write(ctx, c, errcode.Conflict(fmt.Sprintf("bot is owned by another cluster; upload to %s", base)))
		return
	}

	exists, err := h.store.BotExists(ctx, localPart)
	if err != nil {
		errhttp.Write(ctx, c, err)
		return
	}
	if !exists {
		errhttp.Write(ctx, c, errcode.NotFound("bot not found on this cluster"))
		return
	}

	// Size cap before reading the body.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.cfg.MaxUploadBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("upload too large or unreadable"))
		return
	}

	// Decode to confirm it is a real PNG/JPEG; capture the detected format.
	_, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") {
		errhttp.Write(ctx, c, errcode.BadRequest("body is not a valid PNG or JPEG image"))
		return
	}
	contentType := "image/" + format

	// Store the object FIRST, then upsert the doc (doc exists ⟺ object exists).
	key := botObjectKey(localPart)
	etag, err := h.blobs.Put(ctx, key, bytes.NewReader(raw), int64(len(raw)), contentType)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("store avatar object: %w", err))
		return
	}
	now := time.Now().UTC()
	av := &model.Avatar{
		ID:          "bot:" + localPart,
		SubjectType: model.AvatarSubjectBot,
		SubjectID:   localPart,
		MinioKey:    key,
		ContentType: contentType,
		Size:        int64(len(raw)),
		ETag:        etag,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.store.SetBotAvatar(ctx, av); err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("upsert avatar doc: %w", err))
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusNoContent)
}
```

Remove the temporary stub `registerUploadRoutes` from `routes.go` (added in Task 6) so there is exactly one definition. The real signature takes an `accountVerifier`, so **update the call site in `main.go`** from `registerUploadRoutes(r, h, validator, cfg)` to:

```go
registerUploadRoutes(r, h, oidcVerifier{v: validator}, cfg)
```

(In `DEV_MODE`, `validator` is nil but `cfg.DevMode` makes the middleware skip `Validate`, so the nil is never dereferenced.)

> NOTE on `CreatedAt` upsert: a re-upload overwrites `CreatedAt` with `now`. If preserving the original `CreatedAt` matters, fetch-then-set; v1 accepts overwriting (uploads are admin-only and rare). This matches spec §7a.2 "overwrites in place".

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./avatar-service/ -run TestUpload -v` → PASS
Run: `make test SERVICE=avatar-service` → all unit tests PASS
Run: `make build SERVICE=avatar-service` → builds.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/upload.go avatar-service/routes.go avatar-service/upload_test.go
git commit -m "feat(avatar-service): bot-avatar upload — locality, existence, validate, store, upsert"
```

---

## Task 11: Lint, coverage, and full verification

**Files:** none (verification only)

- [ ] **Step 1: Run goimports/format**

Run: `make fmt`
Expected: no diff, or formatting applied (then re-commit if changed).

- [ ] **Step 2: Run the linter**

Run: `make lint`
Expected: PASS (fix any findings: unused imports, error-wrap messages, etc.).

- [ ] **Step 3: Run unit tests with race + coverage**

Run: `go test -race -coverprofile=cover.out ./avatar-service/... && go tool cover -func=cover.out | tail -1`
Expected: PASS; total coverage ≥ 80%. If below, add table cases for uncovered branches (error paths in handlers, `serveStored` blob error, cache expiry).

- [ ] **Step 4: Run integration tests**

Run: `make test-integration SERVICE=avatar-service`
Expected: PASS.

- [ ] **Step 5: Run SAST**

Run: `make sast`
Expected: PASS (no medium+). The `image.Decode` of untrusted bytes is bounded by `http.MaxBytesReader`; if gosec flags a decompression-bomb concern, document the size cap with a justified `// #nosec` only if truly a false positive.

- [ ] **Step 6: Commit any fixes**

```bash
git add -A
git commit -m "chore(avatar-service): lint, format, coverage fixes"
```

---

## Task 12: Deploy artifacts

**Files:**
- Create: `avatar-service/deploy/Dockerfile`, `avatar-service/deploy/docker-compose.yml`, `avatar-service/deploy/azure-pipelines.yml`

- [ ] **Step 1: Create the Dockerfile**

Create `avatar-service/deploy/Dockerfile` (copy an existing service's, e.g. `auth-service/deploy/Dockerfile`, changing the binary name). Multi-stage: `golang:1.25.11-alpine` builder, `alpine:3.21` runtime; build context = repo root; build `./avatar-service/`.

```dockerfile
FROM golang:1.25.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/avatar-service ./avatar-service/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /out/avatar-service /usr/local/bin/avatar-service
EXPOSE 8080
ENTRYPOINT ["avatar-service"]
```

- [ ] **Step 2: Create docker-compose.yml**

Create `avatar-service/deploy/docker-compose.yml` for local dev: the service + Mongo + MinIO, with `DEV_MODE=true`, `SITE_ID`, `CLUSTER_DOMAINS`, `DOMAIN_SITES`, `EMPLOYEE_PHOTO_BASE_URL`, `MONGO_URI`, `MINIO_*`, `AVATAR_BUCKET`. (Mirror an existing service's compose for Mongo/MinIO service blocks.)

- [ ] **Step 3: Create azure-pipelines.yml**

Create `avatar-service/deploy/azure-pipelines.yml` mirroring another service's pipeline, with the service name `avatar-service`.

- [ ] **Step 4: Verify the image builds**

Run: `docker build -f avatar-service/deploy/Dockerfile -t avatar-service:dev .`
Expected: image builds.

- [ ] **Step 5: Commit**

```bash
git add avatar-service/deploy/
git commit -m "chore(avatar-service): deploy artifacts (Dockerfile, compose, pipeline)"
```

---

## Task 13: Client API docs

**Files:**
- Modify: `docs/client-api.md`

- [ ] **Step 1: Add an avatar-service section**

Append a section to `docs/client-api.md` documenting the public HTTP surface:
- `GET /avatar/v1/:accountName` — user/bot avatar; 307 to employee-photo (users) or owning cluster (remote bots); streams custom bot image (200, `ETag`, `Cache-Control`); deterministic SVG default; `?fwd=1` loop-breaker; `If-None-Match`→304.
- `GET /avatar/v1/room/:roomID` — channel/discussion only; 307 to owning cluster; stream/default; dm/botDM→default.
- `PUT /avatar/v1/bot/:botName` — auth: Bearer OIDC + platform-admin; body = PNG/JPEG bytes; `400` malformed/invalid image, `401` no/invalid token, `403` non-admin, `404` unknown bot, `409` wrong cluster (body names the correct domain), `204` success.
- Note the frontend-default contract for employee-photo 404 (spec §6/§9).

- [ ] **Step 2: Commit**

```bash
git add docs/client-api.md
git commit -m "docs(client-api): add avatar-service HTTP endpoints"
```

---

## Self-Review Checklist (run before execution)

- **Spec coverage:** §4.1 serve-stored (Task 7/8 `serveStored`), §4.2 fwd loop-breaker (Task 7/8), §4.3 caching headers (Task 7), §4.4 avatars collection (Task 1, 3), §5 parsing + resolver seam (Task 2, 5), §6 Endpoint 1 (Task 7), §7 Endpoint 2 (Task 8), §7a.1–7a.4 upload (Task 9, 10), §8 default SVG (Task 2, 7), §2 cross-cutting middleware/healthz (Task 6), config §3 (Task 5), testing §10 (each task + Task 11), deploy (Task 12), docs §11 (Task 13). ✅
- **Out of scope, intentionally:** migration job, OTel/Prometheus, rate-limiting.
- **Type consistency:** `avatarStore` methods match between `store.go`, the mock usage in tests, and `store_mongo.go`; `blobStore.Get/Put`, `blobInfo`, `errBlobNotFound` consistent across `minio.go`, fake, handler; `config` field names consistent across `config.go`, `main.go`, tests; `serveDefault(c, kind, seed, name)` / `serveStored(c, kind, av, seed, name)` signatures consistent.
