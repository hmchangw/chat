# Search Index Env Vars: Required + Versioned Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every search index env var on `search-sync-worker` and `search-service` required (no silent-fallback defaults), enforce a consistent `<base>-v<N>` versioning contract on the two versionable indices (messages, spotlight), and keep user-room unversioned because ES `terms_lookup` cannot accept a wildcard pattern.

**Architecture:**
- New shared helper `pkg/searchindex.StripVersion(name)` returns `(base, version, ok)` by stripping a `-v\d+$` suffix.
- `search-sync-worker` makes all three index envs required, validates `-v<N>` suffix on messages and spotlight, and derives ES template names + `index_patterns` from the stripped base so future v2/v3 indices auto-inherit the template.
- `search-service` keeps its envs required, uses `SEARCH_USER_ROOM_INDEX` as-is for `terms_lookup` (single concrete index, ES constraint), and strips the version off `SEARCH_SPOTLIGHT_INDEX` at startup to build a `<base>-*` read pattern. Messages search pattern stays hardcoded for cross-cluster federation.
- Dead-code cleanup: drop the `UserRoomIndex` constant + `resolveUserRoomIndex` fallback in `search-service` now that the env is no longer optional.
- docker-compose files updated so a fresh `docker compose up` brings everything up with the new contract.

**Tech Stack:** Go 1.25, `caarlos0/env/v11`, `nats.go/jetstream`, Elasticsearch (`pkg/searchengine`), Testify, mockgen. All commands run via the root Makefile.

---

## File Structure

| File | Action | Purpose |
|---|---|---|
| `pkg/searchindex/version.go` | Create | `StripVersion(name) (base string, version int, ok bool)` helper, regex `-v\d+$` |
| `pkg/searchindex/version_test.go` | Create | Table-driven tests for `StripVersion` covering suffix present/absent, bad suffixes, edge cases |
| `search-sync-worker/main.go` | Modify | Remove `envDefault:""` + fallback blocks; mark spotlight/user-room `,required`; add startup validation for `-v<N>` on messages + spotlight |
| `search-sync-worker/messages.go` | Modify | Derive template name + `index_patterns` from `StripVersion(prefix)` base, not raw prefix |
| `search-sync-worker/messages_test.go` | Modify | Update template-name + pattern assertions for new derived values |
| `search-sync-worker/spotlight.go` | Modify | Same: derive template name + pattern from base, write target stays the full versioned env value |
| `search-sync-worker/spotlight_test.go` | Modify | Update template-name + pattern assertions |
| `search-sync-worker/user_room.go` | Modify | Template name = `<envValue>_template`; `index_patterns` = `[envValue]` (single exact name) — encodes "intentionally unversioned" |
| `search-sync-worker/user_room_test.go` | Modify | Update template-name assertion |
| `search-sync-worker/deploy/docker-compose.yml` | Modify | Add `SPOTLIGHT_INDEX` and `USER_ROOM_INDEX` env vars (previously omitted, relied on fallbacks) |
| `search-service/main.go` | Modify | Validate `-v<N>` on `SEARCH_SPOTLIGHT_INDEX`; compute spotlight read pattern at startup; pass it into handler |
| `search-service/handler.go` | Modify | Rename `SpotlightIndex` field to `SpotlightReadPattern` to reflect its new semantics; the value passed in is `<base>-*` |
| `search-service/handler_test.go` | Modify | Update field name + expected search-index assertion |
| `search-service/store_es.go` | Modify | Delete `const UserRoomIndex` and `resolveUserRoomIndex`; constructor drops the fallback |
| `search-service/store_es_test.go` | Modify | Remove the test that asserted the fallback |
| `search-service/query_messages.go` | Modify | Drop the `resolveUserRoomIndex(...)` call in `buildMessageQuery`; caller is now responsible |
| `search-service/query_messages_test.go` | Modify | Drop the resolver dependency; pass concrete index name directly |
| `search-service/integration_test.go` | Modify | Update test fixture index names to versioned form for spotlight; rename `UserRoomIndex` constant references to use the test config value |
| `search-service/deploy/docker-compose.yml` | Modify | Drop trailing `-chat` from `SEARCH_SPOTLIGHT_INDEX` so it conforms to `-v<N>$` |

---

## Task 1: Create `pkg/searchindex` package

**Files:**
- Create: `pkg/searchindex/version.go`
- Test: `pkg/searchindex/version_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/searchindex/version_test.go`:

```go
package searchindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripVersion(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantBase    string
		wantVersion int
		wantOK      bool
	}{
		{
			name:        "single digit version",
			input:       "messages-site-a-v1",
			wantBase:    "messages-site-a",
			wantVersion: 1,
			wantOK:      true,
		},
		{
			name:        "multi digit version",
			input:       "spotlight-site-b-v42",
			wantBase:    "spotlight-site-b",
			wantVersion: 42,
			wantOK:      true,
		},
		{
			name:        "no suffix returns input unchanged",
			input:       "user-room-mv-site-a",
			wantBase:    "user-room-mv-site-a",
			wantVersion: 0,
			wantOK:      false,
		},
		{
			name:        "uppercase V is not stripped",
			input:       "messages-site-a-V1",
			wantBase:    "messages-site-a-V1",
			wantVersion: 0,
			wantOK:      false,
		},
		{
			name:        "non-numeric tail is not stripped",
			input:       "messages-site-a-v1a",
			wantBase:    "messages-site-a-v1a",
			wantVersion: 0,
			wantOK:      false,
		},
		{
			name:        "version in middle is not stripped",
			input:       "messages-v1-site-a",
			wantBase:    "messages-v1-site-a",
			wantVersion: 0,
			wantOK:      false,
		},
		{
			name:        "empty input",
			input:       "",
			wantBase:    "",
			wantVersion: 0,
			wantOK:      false,
		},
		{
			name:        "only version suffix",
			input:       "-v1",
			wantBase:    "",
			wantVersion: 1,
			wantOK:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, version, ok := StripVersion(tc.input)
			assert.Equal(t, tc.wantBase, base)
			assert.Equal(t, tc.wantVersion, version)
			assert.Equal(t, tc.wantOK, ok)
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/searchindex/... -run TestStripVersion -v`
Expected: FAIL — `pkg/searchindex` does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Create `pkg/searchindex/version.go`:

```go
// Package searchindex contains small helpers shared by services that read
// and write Elasticsearch indices managed by search-sync-worker. Today it
// only carries the StripVersion helper used to derive a base index name
// from a versioned write target so callers can construct read patterns
// like "<base>-*" without each service reinventing the parsing rule.
package searchindex

import (
	"regexp"
	"strconv"
)

var versionSuffix = regexp.MustCompile(`-v(\d+)$`)

// StripVersion splits a versioned index name like "messages-site-a-v1"
// into its base ("messages-site-a") and integer version (1). When the
// input does not end in a -v<N> suffix (e.g. user-room indices that are
// intentionally unversioned), the input is returned unchanged with
// ok=false so callers can treat the value as a literal index name.
//
// Matching is anchored to the end of the string and accepts only lowercase
// `v` followed by one or more digits. Anything else (uppercase V, trailing
// alpha characters, version tokens embedded mid-name) is rejected so the
// helper stays predictable.
func StripVersion(name string) (base string, version int, ok bool) {
	m := versionSuffix.FindStringSubmatchIndex(name)
	if m == nil {
		return name, 0, false
	}
	v, err := strconv.Atoi(name[m[2]:m[3]])
	if err != nil {
		return name, 0, false
	}
	return name[:m[0]], v, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/searchindex/... -run TestStripVersion -v`
Expected: PASS, all 8 subtests green.

- [ ] **Step 5: Commit**

```bash
git add pkg/searchindex/version.go pkg/searchindex/version_test.go
git commit -m "feat(searchindex): add StripVersion helper for derived base names"
```

---

## Task 2: Make search-sync-worker envs required and validate version suffix

**Files:**
- Modify: `search-sync-worker/main.go:42-53` (config struct), `:91-96` (delete fallback block)

- [ ] **Step 1: Update the config struct**

Replace lines 51-53 of `search-sync-worker/main.go`:

```go
	MsgIndexPrefix string `env:"MSG_INDEX_PREFIX,required"`
	SpotlightIndex string `env:"SPOTLIGHT_INDEX,required"`
	UserRoomIndex  string `env:"USER_ROOM_INDEX,required"`
```

with the same three lines but make all three `,required` (MsgIndexPrefix already is; spotlight/user-room change). After this step the three lines should read exactly as shown above.

- [ ] **Step 2: Delete the fallback block**

Delete lines 91-96 of `search-sync-worker/main.go` entirely:

```go
	if cfg.SpotlightIndex == "" {
		cfg.SpotlightIndex = fmt.Sprintf("spotlight-%s-v1-chat", cfg.SiteID)
	}
	if cfg.UserRoomIndex == "" {
		cfg.UserRoomIndex = fmt.Sprintf("user-room-%s", cfg.SiteID)
	}
```

If after deletion `fmt` is no longer used in `main.go`, also remove it from the import block. Quick check: `fmt.Sprintf` is used elsewhere in main.go for error messages? Search the file before deleting the import. If `fmt` is unused, `make lint` will catch it.

- [ ] **Step 3: Add version-suffix validation**

Immediately after the existing `cfg.BulkFlushInterval <= 0` check (around line 115 of `main.go`), append:

```go
	if _, _, ok := searchindex.StripVersion(cfg.MsgIndexPrefix); !ok {
		slog.Error("invalid config", "name", "MSG_INDEX_PREFIX", "value", cfg.MsgIndexPrefix, "reason", "must end with -v<N>, e.g. messages-site-a-v1")
		os.Exit(1)
	}
	if _, _, ok := searchindex.StripVersion(cfg.SpotlightIndex); !ok {
		slog.Error("invalid config", "name", "SPOTLIGHT_INDEX", "value", cfg.SpotlightIndex, "reason", "must end with -v<N>, e.g. spotlight-site-a-v1")
		os.Exit(1)
	}
```

Note: `USER_ROOM_INDEX` is intentionally NOT validated for a `-v<N>` suffix — this index is rebuilt from MongoDB rather than ES `_reindex` and the value is passed verbatim into ES `terms_lookup`, which rejects wildcards. Encoding "unversioned" in the env shape is intentional.

Add the import for `github.com/hmchangw/chat/pkg/searchindex` to the import block at the top of `main.go`.

- [ ] **Step 4: Build to confirm it compiles**

Run: `make build SERVICE=search-sync-worker`
Expected: build succeeds.

- [ ] **Step 5: Commit**

```bash
git add search-sync-worker/main.go
git commit -m "feat(search-sync-worker): require index envs and validate -v<N> suffix"
```

---

## Task 3: Derive messages template name + index_patterns from stripped base

**Files:**
- Modify: `search-sync-worker/messages.go:41-46`, `:147-150`
- Test: `search-sync-worker/messages_test.go`

- [ ] **Step 1: Update the failing test first**

In `search-sync-worker/messages_test.go`, find the test asserting `TemplateName()` (likely named `TestMessageCollection_TemplateName` or similar). Replace whatever it asserts today with:

```go
func TestMessageCollection_TemplateName_StripsVersion(t *testing.T) {
	c := newMessageCollection("messages-site-a-v1")
	assert.Equal(t, "messages-site-a_template", c.TemplateName())
}

func TestMessageCollection_TemplateName_BareBaseFallback(t *testing.T) {
	// Defensive: if a non-versioned value reaches the collection (validation
	// is enforced upstream in main.go), TemplateName must still produce a
	// stable, valid ES template name rather than panicking.
	c := newMessageCollection("messages-site-a")
	assert.Equal(t, "messages-site-a_template", c.TemplateName())
}

func TestMessageCollection_TemplateBody_PatternStripsVersion(t *testing.T) {
	c := newMessageCollection("messages-site-a-v1")
	body := c.TemplateBody()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	patterns, ok := decoded["index_patterns"].([]any)
	require.True(t, ok)
	require.Len(t, patterns, 1)
	assert.Equal(t, "messages-site-a-*", patterns[0])
}
```

Make sure the file imports `encoding/json` and `github.com/stretchr/testify/require` if they aren't already in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=search-sync-worker`
Expected: FAIL on the three new tests — current code returns `messages-site-a-v1_template` and pattern `messages-site-a-v1-*`.

- [ ] **Step 3: Update messages.go to strip version**

Edit `search-sync-worker/messages.go`. Find:

```go
func (c *messageCollection) TemplateName() string {
	return fmt.Sprintf("%s_template", c.indexPrefix)
}
```

Replace with:

```go
func (c *messageCollection) TemplateName() string {
	base, _, _ := searchindex.StripVersion(c.indexPrefix)
	return fmt.Sprintf("%s_template", base)
}
```

Find:

```go
func messageTemplateBody(prefix string) json.RawMessage {
	...
		"index_patterns": []string{fmt.Sprintf("%s-*", prefix)},
```

Replace the `index_patterns` line with:

```go
		"index_patterns": []string{fmt.Sprintf("%s-*", stripMsgVersion(prefix))},
```

And add a small helper at the bottom of the file:

```go
// stripMsgVersion drops a trailing -v<N> from a messages index prefix so the
// template's index_patterns match both the current version's monthly indices
// (e.g. messages-site-a-v1-2026-05) AND any future versions (v2-*, v3-*).
func stripMsgVersion(prefix string) string {
	base, _, _ := searchindex.StripVersion(prefix)
	return base
}
```

Add the import for `github.com/hmchangw/chat/pkg/searchindex`.

Note: the **write target** for messages still uses `c.indexPrefix` unchanged (so monthly indices become `messages-site-a-v1-2026-05`). Only the template name and `index_patterns` are derived from the stripped base. Verify by grepping `messages.go` for `c.indexPrefix` — every use that constructs a write index name should keep using the full versioned prefix.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=search-sync-worker`
Expected: all messages tests green.

- [ ] **Step 5: Commit**

```bash
git add search-sync-worker/messages.go search-sync-worker/messages_test.go
git commit -m "feat(search-sync-worker): derive messages template name/pattern from stripped base"
```

---

## Task 4: Derive spotlight template name + index_patterns from stripped base

**Files:**
- Modify: `search-sync-worker/spotlight.go:30-36`, `:126-135`
- Test: `search-sync-worker/spotlight_test.go`

- [ ] **Step 1: Update the failing test first**

In `search-sync-worker/spotlight_test.go`, find existing template-name and template-body assertions and replace with:

```go
func TestSpotlightCollection_TemplateName_StripsVersion(t *testing.T) {
	c := newSpotlightCollection("spotlight-site-a-v1")
	assert.Equal(t, "spotlight-site-a_template", c.TemplateName())
}

func TestSpotlightCollection_TemplateBody_PatternStripsVersion(t *testing.T) {
	c := newSpotlightCollection("spotlight-site-a-v1")
	body := c.TemplateBody()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	patterns, ok := decoded["index_patterns"].([]any)
	require.True(t, ok)
	require.Len(t, patterns, 1)
	assert.Equal(t, "spotlight-site-a-*", patterns[0])
}
```

If the existing tests assert different values, delete them — they're encoding the old contract.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=search-sync-worker`
Expected: FAIL — current code returns `"spotlight_template"` and pattern `[spotlight-site-a-v1]`.

- [ ] **Step 3: Update spotlight.go**

Replace lines 30-36 of `search-sync-worker/spotlight.go`:

```go
func (c *spotlightCollection) TemplateName() string {
	base, _, _ := searchindex.StripVersion(c.indexName)
	return fmt.Sprintf("%s_template", base)
}

func (c *spotlightCollection) TemplateBody() json.RawMessage {
	return spotlightTemplateBody(c.indexName)
}
```

Find `spotlightTemplateBody` (around line 130) and change the `index_patterns` line from:

```go
		"index_patterns": []string{indexName},
```

to:

```go
		"index_patterns": []string{fmt.Sprintf("%s-*", stripSpotlightVersion(indexName))},
```

Add helper at the bottom of `spotlight.go`:

```go
// stripSpotlightVersion drops a trailing -v<N> from a spotlight index name
// so the template's index_patterns match the current and any future
// versioned indices (spotlight-{site}-v1, v2, v3, ...).
func stripSpotlightVersion(name string) string {
	base, _, _ := searchindex.StripVersion(name)
	return base
}
```

Add import `github.com/hmchangw/chat/pkg/searchindex`.

The write target (where docs are indexed) continues to use `c.indexName` unchanged — that's the concrete versioned index like `spotlight-site-a-v1`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=search-sync-worker`
Expected: spotlight tests green; all other worker tests still green.

- [ ] **Step 5: Commit**

```bash
git add search-sync-worker/spotlight.go search-sync-worker/spotlight_test.go
git commit -m "feat(search-sync-worker): derive spotlight template name/pattern from stripped base"
```

---

## Task 5: Update user-room collection template name (no versioning)

**Files:**
- Modify: `search-sync-worker/user_room.go:39-45`
- Test: `search-sync-worker/user_room_test.go`

- [ ] **Step 1: Update the failing test**

In `search-sync-worker/user_room_test.go`, replace the existing template-name assertion with:

```go
func TestUserRoomCollection_TemplateName_DerivesFromEnv(t *testing.T) {
	c := newUserRoomCollection("user-room-mv-site-a")
	assert.Equal(t, "user-room-mv-site-a_template", c.TemplateName())
}

func TestUserRoomCollection_TemplateBody_PatternIsExactName(t *testing.T) {
	// User-room is intentionally unversioned: ES terms_lookup forbids
	// wildcards, so the template matches exactly one concrete index.
	c := newUserRoomCollection("user-room-mv-site-a")
	body := c.TemplateBody()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	patterns, ok := decoded["index_patterns"].([]any)
	require.True(t, ok)
	require.Len(t, patterns, 1)
	assert.Equal(t, "user-room-mv-site-a", patterns[0])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=search-sync-worker`
Expected: FAIL — current code returns hardcoded `"user_room_template"`.

- [ ] **Step 3: Update user_room.go**

Replace lines 39-45 of `search-sync-worker/user_room.go`:

```go
func (c *userRoomCollection) TemplateName() string {
	return fmt.Sprintf("%s_template", c.indexName)
}

func (c *userRoomCollection) TemplateBody() json.RawMessage {
	return userRoomTemplateBody(c.indexName)
}
```

`userRoomTemplateBody` already sets `index_patterns: []string{indexName}` (the exact-name behavior we want), so no changes needed there.

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=search-sync-worker`
Expected: all worker tests green.

- [ ] **Step 5: Commit**

```bash
git add search-sync-worker/user_room.go search-sync-worker/user_room_test.go
git commit -m "feat(search-sync-worker): derive user-room template name from env value"
```

---

## Task 6: Update search-sync-worker docker-compose

**Files:**
- Modify: `search-sync-worker/deploy/docker-compose.yml`

- [ ] **Step 1: Add the two new env vars**

Edit `search-sync-worker/deploy/docker-compose.yml`. After the existing `MSG_INDEX_PREFIX` line, add:

```yaml
      - SPOTLIGHT_INDEX=spotlight-site-local-v1
      - USER_ROOM_INDEX=user-room-mv-site-local
```

The final environment block should read:

```yaml
    environment:
      - NATS_URL=nats://nats:4222
      - NATS_CREDS_FILE=/etc/nats/backend.creds
      - SITE_ID=site-local
      - SEARCH_URL=http://elasticsearch:9200
      - SEARCH_BACKEND=elasticsearch
      - MSG_INDEX_PREFIX=messages-site-local-v1
      - SPOTLIGHT_INDEX=spotlight-site-local-v1
      - USER_ROOM_INDEX=user-room-mv-site-local
      - BOOTSTRAP_STREAMS=true
```

Notes on chosen values:
- `spotlight-site-local-v1`: matches the `-v<N>$` regex. The previous value `spotlight-site-local-v1-chat` would NOT pass validation since `-chat` follows the `-v1`; dropping the `-chat` suffix is intentional and aligns local dev with the new contract.
- `user-room-mv-site-local`: unversioned, matches the new design. The `-mv` keeps consistency with how `roomMembers` is referred to elsewhere in the codebase.

- [ ] **Step 2: Verify it boots locally (optional smoke check)**

If Docker is available locally:

```bash
docker compose -f search-sync-worker/deploy/docker-compose.yml up -d
docker compose -f search-sync-worker/deploy/docker-compose.yml logs search-sync-worker | head -50
docker compose -f search-sync-worker/deploy/docker-compose.yml down
```

Expected: worker starts without "parse config" or "invalid config" errors and logs the three index names.

If Docker isn't available, skip this step — the integration tests in Task 11 will exercise the same code paths.

- [ ] **Step 3: Commit**

```bash
git add search-sync-worker/deploy/docker-compose.yml
git commit -m "chore(search-sync-worker): set SPOTLIGHT_INDEX and USER_ROOM_INDEX in compose"
```

---

## Task 7: Validate spotlight env + derive read pattern in search-service

**Files:**
- Modify: `search-service/main.go:45-54`, `:111-125` (handler wiring)
- Modify: `search-service/handler.go:18-30`

- [ ] **Step 1: Update SearchConfig field name + docs**

Rename the field in `search-service/main.go:52` so the new semantic (read pattern, not concrete name) is clear at call sites:

```go
	SpotlightIndex string `env:"SPOTLIGHT_INDEX,required"`
```

stays the same at the env layer (operators still set `SEARCH_SPOTLIGHT_INDEX=spotlight-site-a-v1`), but downstream we'll compute and pass a derived pattern, not the raw env value.

- [ ] **Step 2: Add startup validation + derivation**

In `search-service/main.go`, after `cfg, err := env.ParseAs[Config]()` (around line 73) and after any `os.Exit(1)` on parse failure, add:

```go
	spotBase, _, ok := searchindex.StripVersion(cfg.Search.SpotlightIndex)
	if !ok {
		slog.Error("invalid config", "name", "SEARCH_SPOTLIGHT_INDEX", "value", cfg.Search.SpotlightIndex, "reason", "must end with -v<N>, e.g. spotlight-site-a-v1")
		os.Exit(1)
	}
	spotlightReadPattern := fmt.Sprintf("%s-*", spotBase)
```

User-room is NOT validated against `-v<N>` here — the env value is used as-is for `terms_lookup`. It's still `,required`, so empty values fail at parse time.

Add imports for `fmt` and `github.com/hmchangw/chat/pkg/searchindex`.

- [ ] **Step 3: Update handler wiring**

At the existing handler construction (around line 119-125 of `main.go`), the field that today reads:

```go
		SpotlightIndex: cfg.Search.SpotlightIndex,
```

becomes:

```go
		SpotlightReadPattern: spotlightReadPattern,
```

- [ ] **Step 4: Rename field on the handler struct**

In `search-service/handler.go` around line 22-23, change:

```go
	UserRoomIndex  string
	SpotlightIndex string
```

to:

```go
	UserRoomIndex        string
	SpotlightReadPattern string
```

Find every reference to `h.cfg.SpotlightIndex` in `handler.go` (line 140 has one) and replace with `h.cfg.SpotlightReadPattern`. There should be exactly one production usage.

- [ ] **Step 5: Update handler tests**

In `search-service/handler_test.go`, find references to `SpotlightIndex: testSpotlightIndex` (line 89) and rename to `SpotlightReadPattern: testSpotlightIndex`. Update `testSpotlightIndex` value (line 17) to `"spotlight-*"` so it reflects a pattern, not a concrete name. Update the assertion at line 240 to expect `[]string{"spotlight-*"}` to match the new pattern contract.

- [ ] **Step 6: Build + run unit tests**

Run: `make build SERVICE=search-service && make test SERVICE=search-service`
Expected: builds and all unit tests pass.

- [ ] **Step 7: Commit**

```bash
git add search-service/main.go search-service/handler.go search-service/handler_test.go
git commit -m "feat(search-service): derive spotlight read pattern from versioned env"
```

---

## Task 8: Remove dead UserRoomIndex fallback in search-service

**Files:**
- Modify: `search-service/store_es.go:9-39`
- Modify: `search-service/store_es_test.go`
- Modify: `search-service/query_messages.go:31-32`, `:136-145`
- Modify: `search-service/query_messages_test.go`

- [ ] **Step 1: Delete the fallback test**

In `search-service/store_es_test.go`, find the test (around line 104) that asserts `resolveUserRoomIndex("")` returns the `UserRoomIndex` constant and delete that test entirely. If the file has another test that exercises `resolveUserRoomIndex` with a non-empty argument and asserts it's returned unchanged, also delete that — once the function is gone, the test goes with it.

- [ ] **Step 2: Run remaining tests to confirm no other references**

Run: `grep -rn "resolveUserRoomIndex\|const UserRoomIndex" search-service/`
Expected: only references remaining are the ones we're about to delete in `store_es.go` and `query_messages.go`. If any other file references either symbol, list them and add them to this task before proceeding.

- [ ] **Step 3: Update store_es.go**

In `search-service/store_es.go`, delete:

- The `const UserRoomIndex = "user-room"` declaration around line 13.
- The `resolveUserRoomIndex(...)` function around lines 32-39.
- The call to `resolveUserRoomIndex(userRoomIndex)` inside `newESStore` around line 29 — replace `resolveUserRoomIndex(userRoomIndex)` with `userRoomIndex` directly so the empty-value fallback is gone.

Final `newESStore` should read:

```go
func newESStore(engine *searchengine.Engine, userRoomIndex string) *esStore {
	return &esStore{engine: engine, userRoomIndex: userRoomIndex}
}
```

- [ ] **Step 4: Update query_messages.go**

In `search-service/query_messages.go`, delete the `userRoomIndex = resolveUserRoomIndex(userRoomIndex)` line at line 32 — the caller (`handler.go` line 83) now passes a guaranteed-non-empty value (env is `,required`).

Update the doc comment at lines 136-139 (currently documents the resolver dependency). Replace with:

```go
// termsLookupClause resolves the user's allowed rooms via ES terms-lookup
// instead of shipping the rooms[] array on every query. The caller must
// pass a concrete, non-empty index name (validated upstream via the
// USER_ROOM_INDEX env var being marked ,required in main.go). ES
// terms_lookup rejects wildcard patterns, which is why this index is
// intentionally unversioned across the codebase.
```

- [ ] **Step 5: Update query_messages_test.go**

In `search-service/query_messages_test.go`, find any test that depends on the fallback (passes `""` and expects `"user-room"`). Update to pass an explicit test index name like `"test-user-room"` and assert that exact value flows into the produced query body.

- [ ] **Step 6: Update integration_test.go fixture names if needed**

In `search-service/integration_test.go`, find references to the `UserRoomIndex` constant (lines 119, 276, 409, 518). Since the constant is gone, replace those references with a locally-scoped test constant at the top of the file:

```go
const testUserRoomIndex = "user-room-mv-site-test"
```

Replace each `UserRoomIndex` reference inside the file with `testUserRoomIndex`. Same goes for the spotlight index references — define `const testSpotlightIndex = "spotlight-site-test-v1"` for consistency and use it in lieu of any inline `"spotlight-test"` strings.

- [ ] **Step 7: Build + run unit tests**

Run: `make build SERVICE=search-service && make test SERVICE=search-service`
Expected: green.

- [ ] **Step 8: Commit**

```bash
git add search-service/store_es.go search-service/store_es_test.go search-service/query_messages.go search-service/query_messages_test.go search-service/integration_test.go
git commit -m "refactor(search-service): drop dead UserRoomIndex fallback now that env is required"
```

---

## Task 9: Update search-service docker-compose

**Files:**
- Modify: `search-service/deploy/docker-compose.yml:23-24`

- [ ] **Step 1: Update the two index env values**

Replace line 23 of `search-service/deploy/docker-compose.yml`:

```yaml
      - SEARCH_USER_ROOM_INDEX=user-room-site-local
```

with:

```yaml
      - SEARCH_USER_ROOM_INDEX=user-room-mv-site-local
```

Replace line 24:

```yaml
      - SEARCH_SPOTLIGHT_INDEX=spotlight-site-local-v1-chat
```

with:

```yaml
      - SEARCH_SPOTLIGHT_INDEX=spotlight-site-local-v1
```

The `-chat` suffix is dropped so the value matches the `-v<N>$` regex enforced by Task 7. Both values now match exactly what `search-sync-worker/deploy/docker-compose.yml` writes to.

- [ ] **Step 2: Commit**

```bash
git add search-service/deploy/docker-compose.yml
git commit -m "chore(search-service): align index env values with versioned contract"
```

---

## Task 10: End-to-end verification

**Files:**
- None modified — verification only.

- [ ] **Step 1: Run full unit test suite**

Run: `make test`
Expected: every package green, no race failures, no skipped packages.

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: zero warnings. If `golangci-lint` flags an unused import (likely `fmt` in `search-sync-worker/main.go` if no other usage remained), remove it and re-run.

- [ ] **Step 3: Run integration tests for both services**

Run: `make test-integration SERVICE=search-sync-worker`
Expected: green. The worker integration test stands up a real Elasticsearch via testcontainers and exercises template upsert + indexing — this is where any mismatch between the template name/pattern and actual write index names would surface.

Run: `make test-integration SERVICE=search-service`
Expected: green.

- [ ] **Step 4: Local docker-compose smoke (optional but recommended)**

If Docker is available:

```bash
docker compose -f docker-local/docker-compose.yml up -d elasticsearch nats valkey
docker compose -f search-sync-worker/deploy/docker-compose.yml up -d
docker compose -f search-service/deploy/docker-compose.yml up -d
```

Wait ~10 seconds, then:

```bash
docker compose -f search-sync-worker/deploy/docker-compose.yml logs search-sync-worker | grep -E "index template upserted|search-sync-worker running"
docker compose -f search-service/deploy/docker-compose.yml logs search-service | grep -E "ready|listening"
```

Expected log lines:
- `index template upserted` appears three times — once each for `messages-site-local_template`, `spotlight-site-local_template`, `user-room-mv-site-local_template`.
- `search-sync-worker running` with `msgPrefix=messages-site-local-v1 spotlightIndex=spotlight-site-local-v1 userRoomIndex=user-room-mv-site-local`.
- search-service starts without "invalid config" errors.

Then check the templates landed in ES:

```bash
curl -s http://localhost:9200/_index_template | jq '.index_templates[].name'
```

Expected: includes `messages-site-local_template`, `spotlight-site-local_template`, `user-room-mv-site-local_template`.

Tear down:

```bash
docker compose -f search-service/deploy/docker-compose.yml down
docker compose -f search-sync-worker/deploy/docker-compose.yml down
```

- [ ] **Step 5: Push branch**

```bash
git push -u origin claude/search-index-env-vars-5cts2
```

---

## Out of scope (intentionally deferred)

These are NOT in this plan — flagging them so the executor doesn't drift:

- **Production env coordination.** Ops/IaC sets the actual env values per site. Any prod site running the old `spotlight-{site}-v1-chat` value needs a coordinated rename or reindex. This plan only touches local-dev docker-compose. Production migration is a separate ops concern.
- **`docs/client-api.md` updates.** No client-facing handler changes here — both services' request/response schemas are untouched. CLAUDE.md's client-api requirement does not apply.
- **Federation for spotlight/user-room.** Today only messages search uses cross-cluster `*:` patterns. If/when spotlight gets federated, the read pattern would change to `[<base>-*, *:<base>-*]`; out of scope here.
- **Reindex tooling.** The v1→v2 reindex workflow (create v2, `_reindex` API call, bump env, redeploy, drop v1) is documented in this plan's design discussion but not automated. A future plan could add a `make reindex` target if it's a frequent operation.
- **`MSG_INDEX_PREFIX` semantic shift in prod data.** Today's prod monthly indices are named `messages-{site}-YYYY-MM` (no `v1`). After this change, new monthly indices created against an env of `messages-{site}-v1` will be `messages-{site}-v1-YYYY-MM`. The template pattern `messages-{site}-*` still matches both. No data migration required, but ops should be aware that newly-written months will have the version segment.
