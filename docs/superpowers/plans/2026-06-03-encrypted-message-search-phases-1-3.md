# Encrypted Message Search — Phases 1–3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a non-destructive, flag-gated encrypted message-search path end-to-end (index → query), then the harnesses to measure its quality and latency (arms C/A/B), then cut over.

**Architecture:** `search-sync-worker` writes a *second, parallel* ES index whose `content` is replaced by `contentBlind` (HMAC-blinded analyzer tokens) + `contentEnc`/`encNonce` (per-room atrest ciphertext) + `blindKeyVersion`. `search-service` gains a flag-gated query path that blinds the query the same way, matches `contentBlind`, and returns content by decrypting in-process (Approach A) or fetching from a new history-service batch RPC (Approach B). The plaintext index/path stays live as control arm C until Phase 3 cutover. A shared `pkg/blindsearch` guarantees index-time and query-time tokenization are byte-identical.

**Tech Stack:** Go 1.25, NATS/JetStream, Elasticsearch (`pkg/searchengine`), MongoDB (DEK store), HashiCorp Vault transit (`pkg/atrest`), Gin (n/a here — search is NATS), Prometheus (loadgen), testify + testcontainers.

**Source spec:** `docs/superpowers/specs/2026-06-03-encrypted-message-search-design.md` — read §3–§9, §13, §14 (the §14 corrections are authoritative where they supersede earlier text). Phase 0 (`pkg/msganalyzer`, `pkg/blindidx`) is already merged.

---

## Conventions for every task

- TDD: write the failing test, run it red, implement, run it green, commit. Commit messages end with a blank line then:
  `https://claude.ai/code/session_01Gh3esEZGGNHXkZd1RtwdZM`
- Use `make` targets, never raw `go`. Per-package: `make test SERVICE=pkg/<name>`; per-service: `make test SERVICE=<service>`; mocks: `make generate SERVICE=<service>`; lint: `make lint`; SAST: `make sast`.
- Integration tests are `//go:build integration`, use `pkg/testutil` containers, and each integration package has `func TestMain(m *testing.M){ testutil.RunTests(m) }`.
- After changing any store interface, run `make generate SERVICE=<service>` before testing.
- Any client-facing handler change (search-service request/response, new history subject) requires updating `docs/client-api.md` in the same phase (Phase 3 consolidates this; note new fields as you go).

## File Structure (what gets created / modified)

**New shared package**
- `pkg/blindsearch/blindsearch.go` + `_test.go` — composes `msganalyzer.Analyze` + `blindidx`, plus `LoadHasher` (hex-decode guard). The single source of index==query parity.

**Phase 1 — search-sync-worker (encrypted dual-write)**
- `search-sync-worker/encindex.go` (new) — `EncMessageDoc` struct, `encMessageTemplateBody/Properties`, `buildEncDocument`.
- `search-sync-worker/messages.go` (modify) — thread enc options into `messageCollection`; emit the 2nd `BulkAction`; add `AuxTemplates()`.
- `search-sync-worker/collection.go` (modify) — add `AuxTemplates() []NamedTemplate` to the interface; `NamedTemplate` type.
- `search-sync-worker/spotlight.go`, `user_room.go` (modify) — add `AuxTemplates() []NamedTemplate { return nil }`.
- `search-sync-worker/handler.go` (modify) — NAK on transient `BuildAction` error, ack-drop only on `errcode.Permanent`.
- `search-sync-worker/main.go` (modify) — enc config; conditional Vault+Mongo+cipher+hasher wiring; upsert aux templates.
- Tests: `encindex_test.go`, additions to `messages_test.go`, `handler_test.go`, `integration_test.go`.

**Phase 1 — search-service (encrypted query path, arms A & B)**
- `search-service/query_messages.go` (modify) — swap only `bool.must` content clause to `contentBlind` when enc path; phrase support.
- `search-service/response.go` (modify) — A: decrypt `contentEnc`; B: collect IDs and fetch.
- `search-service/encsearch.go` (new) — enc-path orchestration (variant resolution, content retrieval A/B), `historyBatchClient` interface.
- `search-service/handler.go` (modify) — resolve `Variant`, route to enc path, choose enc index pattern.
- `search-service/main.go` (modify) — enc config; conditional Vault+Mongo+cipher+hasher+history-requester wiring.
- `pkg/model/search.go` (modify) — add `Variant` field (+ `contentEnc`/`encNonce` decode struct lives in search-service).
- Tests: `encsearch_test.go`, additions to `query_messages_test.go`, `handler_test.go`, `integration_messages_test.go`.

**Phase 1 — history-service (Approach B batch RPC)**
- `pkg/subject/subject.go` (modify) — `MsgBatchGet`/`MsgBatchGetPattern`.
- `pkg/model` (modify) — `GetMessagesByIDsRequest`/`Response` (in history-service `internal/models` per its convention).
- `history-service/internal/service/messages_batch.go` (new) + registration in `service.go` — handler wrapping `Repository.GetMessagesByIDs` with per-message access enforcement.
- Tests alongside.

**Phase 2 — harnesses**
- `tools/searchquality/` (new program) — pure metric funcs (`recall_at_k`, `jaccard`, `rbo`) + ES quality/parity runner + report writer.
- `tools/loadgen/{metrics.go,main.go}` (modify) + `preset_search.go`, `search_generator.go`, `search_collector.go`, `search_main.go` (new) — `search-sustained` subcommand, `search-small|medium|large` presets, `loadgen_search_latency_seconds{arm}`.
- `tools/loadgen/deploy/grafana/dashboards/loadtest.json` (modify) — search-latency panel.
- `docs/superpowers/runbooks/encrypted-search-harnesses.md` (new) — how to run both harnesses + the tiny VFS demo.

**Phase 3 — cutover**
- Config default flips; plaintext `content` + `custom_analyzer` removed from the encrypted template path; backfill tool/runbook (`SYNC_MESSAGES_FROM` model); `docs/client-api.md` update; final verification.

---

# PHASE 1

## Task 1.1: `pkg/blindsearch` — index==query parity composition

**Files:**
- Create: `pkg/blindsearch/blindsearch.go`, `pkg/blindsearch/blindsearch_test.go`

- [ ] **Step 1: Write the failing test**

```go
package blindsearch_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindsearch"
)

var key32Hex = strings.Repeat("ab", 32) // 64 hex chars -> 32 raw bytes

func TestLoadHasher_DecodesHexAndEnforcesFloor(t *testing.T) {
	h, err := blindsearch.LoadHasher(key32Hex, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", h.Version())

	// 31 raw bytes (62 hex chars) must be rejected AFTER decoding, not pass as a
	// 62-char string (carry-forward flag #3).
	_, err = blindsearch.LoadHasher(strings.Repeat("ab", 31), "v1")
	require.Error(t, err)

	_, err = blindsearch.LoadHasher("nothex!!", "v1")
	require.Error(t, err)

	_, err = blindsearch.LoadHasher(key32Hex, "")
	require.Error(t, err)
}

func TestTermsAndField_MatchAndAreParallel(t *testing.T) {
	h, err := blindsearch.LoadHasher(key32Hex, "v1")
	require.NoError(t, err)

	terms := blindsearch.Terms(h, "Hello, World!")
	// "hello" and "world" -> 2 blinded hex terms.
	require.Len(t, terms, 2)
	for _, term := range terms {
		_, decErr := hex.DecodeString(term)
		assert.NoError(t, decErr, "term must be hex (whitespace-free) for the space-join")
		assert.NotContains(t, term, " ")
	}

	// Field is exactly the space-join of Terms — the contract both index and
	// query sides rely on.
	assert.Equal(t, strings.Join(terms, " "), blindsearch.Field(h, "Hello, World!"))

	// Empty content -> empty field (Phase-0 carry-forward flag #2).
	assert.Empty(t, blindsearch.Terms(h, ""))
	assert.Equal(t, "", blindsearch.Field(h, ""))
}
```

- [ ] **Step 2: Run red**

Run: `make test SERVICE=pkg/blindsearch`
Expected: FAIL — package does not compile.

- [ ] **Step 3: Implement**

```go
// Package blindsearch composes the message analyzer (pkg/msganalyzer) with the
// blind-index hasher (pkg/blindidx) into the single function pair used at BOTH
// index time (search-sync-worker) and query time (search-service). Routing all
// blind-token production through here guarantees the two sides are byte-identical
// — the core correctness invariant of blind-index search.
package blindsearch

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hmchangw/chat/pkg/blindidx"
	"github.com/hmchangw/chat/pkg/msganalyzer"
)

// LoadHasher decodes a hex-encoded blind key into raw bytes and builds a Hasher.
// The hex decode happens BEFORE the length check so a 32-byte key supplied as 64
// hex chars is not mistaken for a 64-byte key (carry-forward flag #3).
func LoadHasher(hexKey, version string) (*blindidx.Hasher, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode blind key hex: %w", err)
	}
	h, err := blindidx.New(raw, version)
	if err != nil {
		return nil, fmt.Errorf("build blind hasher: %w", err)
	}
	return h, nil
}

// Terms returns the ordered blinded terms for text: Analyze -> HMAC each token.
func Terms(h *blindidx.Hasher, text string) []string {
	return h.Tokens(msganalyzer.Analyze(text))
}

// Field returns the space-joined blinded terms, ready to store in / query against
// the `contentBlind` ES field (whitespace analyzer). Empty text -> "".
func Field(h *blindidx.Hasher, text string) string {
	return strings.Join(Terms(h, text), " ")
}
```

- [ ] **Step 4: Green**

Run: `make test SERVICE=pkg/blindsearch` → PASS. Then `make lint`.

- [ ] **Step 5: Commit**

```bash
git add pkg/blindsearch/
git commit -m "Add pkg/blindsearch composing analyzer + blind hasher"
```

---

## Task 1.2: Encrypted index template + document (search-sync-worker)

**Context:** Mirror `search-sync-worker/messages.go:90-206` (`MessageSearchIndex`, `messageTemplateBody`, `messageTemplateProperties`, `indexName`). The encrypted template drops `content`/`custom_analyzer`; `contentBlind` uses the built-in `whitespace` analyzer (keeps positions for phrase + term freq for BM25); `contentEnc`/`encNonce` are `binary, index:false` (authored explicitly — the `es:` reflection helper at `template.go:19-43` cannot emit `index:false`).

**Files:**
- Create: `search-sync-worker/encindex.go`, `search-sync-worker/encindex_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestEncMessageTemplateProperties_FieldShapes(t *testing.T) {
	props := encMessageTemplateProperties()

	// contentBlind: text + whitespace analyzer, NOT custom_analyzer.
	cb := props["contentBlind"].(map[string]any)
	assert.Equal(t, "text", cb["type"])
	assert.Equal(t, "whitespace", cb["analyzer"])

	// contentEnc / encNonce: binary. ES binary fields are inherently
	// non-indexed/non-searchable — do NOT set "index":false, real ES rejects
	// "unknown parameter [index] on mapper of type [binary]".
	for _, f := range []string{"contentEnc", "encNonce"} {
		m := props[f].(map[string]any)
		assert.Equal(t, "binary", m["type"], f)
		_, hasIndex := m["index"]
		assert.False(t, hasIndex, "binary mapper must not carry an index param")
	}

	assert.Equal(t, "keyword", props["blindKeyVersion"].(map[string]any)["type"])

	// Metadata preserved (sample); plaintext `content` must be ABSENT.
	assert.Equal(t, "keyword", props["roomId"].(map[string]any)["type"])
	assert.Equal(t, "date", props["createdAt"].(map[string]any)["type"])
	_, hasContent := props["content"]
	assert.False(t, hasContent, "encrypted index must not map plaintext content")
}

func TestEncMessageTemplateBody_PatternAndNoCustomAnalyzer(t *testing.T) {
	body := encMessageTemplateBody("enc-messages-v1")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	patterns := parsed["index_patterns"].([]any)
	// StripVersionBase drops the -vN suffix; the version lives in the rolling
	// index name, never in the template index_patterns (mirrors messages.go).
	assert.Equal(t, "enc-messages-*", patterns[0])

	// No custom_analyzer block on the encrypted template.
	tmpl := parsed["template"].(map[string]any)
	settings, _ := json.Marshal(tmpl["settings"])
	assert.NotContains(t, string(settings), "custom_analyzer")
}

func TestBuildEncDocument_Roundtrips(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	evt := &model.MessageEvent{
		SiteID:    "site-a",
		Timestamp: now.UnixMilli(),
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "acc1",
			Content: "hello", CreatedAt: now,
		},
	}
	doc := buildEncDocument(evt, "blindfield hashes", []byte("CIPHER"), []byte("NONCE12bytes"), "v1")

	var got EncMessageDoc
	require.NoError(t, json.Unmarshal(doc, &got))
	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "r1", got.RoomID)
	assert.Equal(t, "blindfield hashes", got.ContentBlind)
	assert.Equal(t, []byte("CIPHER"), got.ContentEnc)
	assert.Equal(t, "v1", got.BlindKeyVersion)
	assert.Equal(t, now.UTC(), got.CreatedAt.UTC())
}
```

- [ ] **Step 2: Run red** — `make test SERVICE=search-sync-worker` → FAIL (undefined).

- [ ] **Step 3: Implement** `search-sync-worker/encindex.go`

```go
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchindex"
)

// EncMessageDoc is the encrypted-index document. It mirrors MessageSearchIndex's
// metadata fields (es-tagged, reflected into the template) but replaces the
// plaintext `content` with blind tokens + an atrest ciphertext blob. The
// contentEnc/encNonce mappings are authored in encMessageTemplateProperties
// because the es-tag reflection helper cannot express binary/index:false.
type EncMessageDoc struct {
	MessageID             string     `json:"messageId"                              es:"keyword"`
	RoomID                string     `json:"roomId"                                 es:"keyword"`
	SiteID                string     `json:"siteId"                                 es:"keyword"`
	UserID                string     `json:"userId"                                 es:"keyword"`
	UserAccount           string     `json:"userAccount"                            es:"keyword"`
	ContentBlind          string     `json:"contentBlind"                           es:"text,whitespace"`
	ContentEnc            []byte     `json:"contentEnc"`            // mapping authored explicitly
	EncNonce              []byte     `json:"encNonce"`             // mapping authored explicitly
	BlindKeyVersion       string     `json:"blindKeyVersion"                        es:"keyword"`
	CreatedAt             time.Time  `json:"createdAt"                              es:"date"`
	EditedAt              *time.Time `json:"editedAt,omitempty"                     es:"date"`
	UpdatedAt             *time.Time `json:"updatedAt,omitempty"                    es:"date"`
	ThreadParentID        string     `json:"threadParentMessageId,omitempty"        es:"keyword"`
	ThreadParentCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty" es:"date"`
	TShow                 bool       `json:"tshow,omitempty"                        es:"boolean"`
}

// encMessageTemplateProperties reflects the es-tagged metadata fields, then
// injects the binary/index:false mappings the reflector cannot express.
func encMessageTemplateProperties() map[string]any {
	props := esPropertiesFromStruct[EncMessageDoc]()
	// ES binary fields are non-indexed/non-searchable by definition; the "index"
	// parameter is NOT valid on a binary mapper (real ES rejects it). Just "binary".
	props["contentEnc"] = map[string]any{"type": "binary"}
	props["encNonce"] = map[string]any{"type": "binary"}
	return props
}

func encMessageTemplateBody(prefix string) json.RawMessage {
	tmpl := map[string]any{
		"index_patterns": []string{fmt.Sprintf("%s-*", searchindex.StripVersionBase(prefix))},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   4,
					"number_of_replicas": 2,
					"refresh_interval":   "30s",
				},
			},
			"mappings": map[string]any{
				"dynamic":    false,
				"properties": encMessageTemplateProperties(),
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}

func buildEncDocument(evt *model.MessageEvent, contentBlind string, contentEnc, encNonce []byte, keyVersion string) json.RawMessage {
	doc := EncMessageDoc{
		MessageID:             evt.Message.ID,
		RoomID:                evt.Message.RoomID,
		SiteID:                evt.SiteID,
		UserID:                evt.Message.UserID,
		UserAccount:           evt.Message.UserAccount,
		ContentBlind:          contentBlind,
		ContentEnc:            contentEnc,
		EncNonce:              encNonce,
		BlindKeyVersion:       keyVersion,
		CreatedAt:             evt.Message.CreatedAt,
		EditedAt:              evt.Message.EditedAt,
		UpdatedAt:             evt.Message.UpdatedAt,
		ThreadParentID:        evt.Message.ThreadParentMessageID,
		ThreadParentCreatedAt: evt.Message.ThreadParentMessageCreatedAt,
		TShow:                 evt.Message.TShow,
	}
	data, _ := json.Marshal(doc)
	return data
}
```

(Note: `encIndexName` is defined in Task 1.4 — the first task that calls it — to avoid an `unused` lint failure here.)

- [ ] **Step 4: Green** — `make test SERVICE=search-sync-worker` → PASS; `make lint`.
- [ ] **Step 5: Commit** — `git add search-sync-worker/encindex*.go && git commit -m "Add encrypted message index template and document"`

---

## Task 1.3: Collection interface — `AuxTemplates`

**Files:** modify `search-sync-worker/collection.go`, `spotlight.go`, `user_room.go`; test in `messages_test.go` (or a small `collection_test.go`).

- [ ] **Step 1: Failing test** (assert the message collection exposes the enc template only when enc is enabled; spotlight/user_room return nil). Add to `search-sync-worker/encindex_test.go`:

```go
func TestMessageCollection_AuxTemplates(t *testing.T) {
	off := newMessageCollection("msgs-v1", time.Time{})
	assert.Empty(t, off.AuxTemplates())

	on := newMessageCollectionEnc("msgs-v1", time.Time{}, encOptions{
		enabled: true, indexPrefix: "enc-msgs-v1", keyVersion: "v1",
	})
	aux := on.AuxTemplates()
	require.Len(t, aux, 1)
	assert.Equal(t, "enc-msgs-v1_template", aux[0].Name)
}
```

(`newMessageCollectionEnc` and `encOptions` are introduced in Task 1.4; if implementing strictly in order, write this assertion as part of 1.4's test instead and keep 1.3 limited to the interface + nil impls below.)

- [ ] **Step 2–3: Implement** — in `collection.go` add:

```go
// NamedTemplate is an additional ES index template a collection owns beyond its
// primary one (e.g. the encrypted parallel index).
type NamedTemplate struct {
	Name string
	Body json.RawMessage
}
```

Add to the `Collection` interface: `AuxTemplates() []NamedTemplate`. In `spotlight.go` and `user_room.go` add:

```go
func (c *spotlightCollection) AuxTemplates() []NamedTemplate { return nil }
func (c *userRoomCollection) AuxTemplates() []NamedTemplate  { return nil }
```

(Match the actual receiver names/types in those files.)

- [ ] **Step 4: Green** — `make test SERVICE=search-sync-worker`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Add AuxTemplates to sync-worker Collection interface"`

---

## Task 1.4: Encrypted dual-write in the message collection

**Context:** `messageCollection.BuildAction` (`messages.go:127-149`) returns `[]searchengine.BulkAction`. When enc is enabled, append a second action targeting `encIndexName(prefix, createdAt)` with `DocID=evt.Message.ID`, `Version=evt.Timestamp`. The enc doc needs: `contentBlind = blindsearch.Field(hasher, content)`; `contentEnc,encNonce = cipher.Encrypt(ctx, roomID, atrest.EncryptedFields{Msg: content})`; `blindKeyVersion = hasher.Version()`. Deletes also delete from the enc index.

**Failure semantics (critical):** a `cipher.Encrypt` failure is transient (Vault/DEK) → must NAK for redelivery. Validation failures (existing) are poison → ack-drop. `BuildAction` returns `errcode.Permanent(...)` for poison and a plain wrapped error for transient (Task 1.5 makes the handler honor this).

**Files:** modify `search-sync-worker/messages.go`; tests in `messages_test.go`.

- [ ] **Step 1: Failing tests** — table cases:
  - enc disabled → 1 action (unchanged).
  - enc enabled, `EventCreated` → 2 actions; 2nd targets `enc-...-2026-01`, `DocID == msg.ID`, and its doc unmarshals to `EncMessageDoc` with non-empty `ContentBlind`, `ContentEnc`, `BlindKeyVersion=="v1"`.
  - enc enabled, `EventDeleted` → 2 delete actions (plaintext + enc index).
  - enc enabled, cipher returns error → `BuildAction` returns error and `errcode.IsPermanent(err) == false`.
  - validation failure (empty `Message.ID`) → error with `errcode.IsPermanent(err) == true`.

  Use a fake cipher injected via `encOptions` (define interface `encCipher interface{ Encrypt(ctx, roomID string, f atrest.EncryptedFields) ([]byte, atrest.EncMeta, error) }`) and a real `*blindidx.Hasher` (cheap). Inject a fake that returns `([]byte("ct"), atrest.EncMeta{Nonce: []byte("nonce")}, nil)` or an error.

- [ ] **Step 2: Red** — `make test SERVICE=search-sync-worker`.

- [ ] **Step 3: Implement** — add to `messages.go`:

```go
type encCipher interface {
	Encrypt(ctx context.Context, roomID string, f atrest.EncryptedFields) ([]byte, atrest.EncMeta, error)
}

type encOptions struct {
	enabled     bool
	indexPrefix string
	keyVersion  string
	hasher      *blindidx.Hasher
	cipher      encCipher
}

// newMessageCollectionEnc is newMessageCollection plus encryption options.
func newMessageCollectionEnc(prefix string, syncFrom time.Time, enc encOptions) *messageCollection {
	c := newMessageCollection(prefix, syncFrom)
	c.enc = enc
	return c
}
```

Add `enc encOptions` to the `messageCollection` struct. Convert existing validation errors in `BuildAction` to `errcode.Permanent(...)` (wrap the existing `errcode` they already build — if they currently return plain errors, wrap with `errcode.Permanent`). Then, after building the plaintext action:

```go
actions := []searchengine.BulkAction{plaintextAction}
if c.enc.enabled {
	encAction, err := c.buildEncAction(ctx, evt)
	if err != nil {
		return nil, err // transient (cipher) stays non-permanent -> NAK
	}
	actions = append(actions, encAction)
}
return actions, nil
```

First (re)add `encIndexName` to `encindex.go` — it was deferred from Task 1.2 because it had no caller there:

```go
// encIndexName mirrors indexName(): monthly rolling index keyed by message createdAt.
func encIndexName(prefix string, createdAt time.Time) string {
	return fmt.Sprintf("%s-%s", prefix, createdAt.UTC().Format("2006-01"))
}
```

Then the action builder:

```go
func (c *messageCollection) buildEncAction(ctx context.Context, evt *model.MessageEvent) (searchengine.BulkAction, error) {
	idx := encIndexName(c.enc.indexPrefix, evt.Message.CreatedAt)
	if evt.Event == model.EventDeleted {
		return searchengine.BulkAction{Action: searchengine.ActionDelete, Index: idx, DocID: evt.Message.ID, Version: evt.Timestamp}, nil
	}
	contentBlind := blindsearch.Field(c.enc.hasher, evt.Message.Content)
	payload, meta, err := c.enc.cipher.Encrypt(ctx, evt.Message.RoomID, atrest.EncryptedFields{Msg: evt.Message.Content})
	if err != nil {
		return searchengine.BulkAction{}, fmt.Errorf("encrypt message %s for enc index: %w", evt.Message.ID, err)
	}
	doc := buildEncDocument(evt, contentBlind, payload, meta.Nonce, c.enc.keyVersion)
	return searchengine.BulkAction{Action: searchengine.ActionIndex, Index: idx, DocID: evt.Message.ID, Version: evt.Timestamp, Doc: doc}, nil
}
```

Add `AuxTemplates()`:

```go
func (c *messageCollection) AuxTemplates() []NamedTemplate {
	if !c.enc.enabled {
		return nil
	}
	return []NamedTemplate{{
		Name: searchindex.StripVersionBase(c.enc.indexPrefix) + "_template",
		Body: encMessageTemplateBody(c.enc.indexPrefix),
	}}
}
```

Confirm `BuildAction`'s signature has a `context.Context` (it must, to pass to Encrypt) — if the current signature is `BuildAction(data []byte)`, widen it to `BuildAction(ctx context.Context, data []byte)` and update the handler call site (`handler.go`) and all collections accordingly.

- [ ] **Step 4: Green** — `make test SERVICE=search-sync-worker`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Dual-write encrypted message doc from sync-worker"`

---

## Task 1.5: Handler NAK-vs-ack-drop on BuildAction error

**Context:** `handler.go:67-72` currently logs and **acks** any `BuildAction` error (poison-drop). With encryption, transient errors must NAK for redelivery. Change: on `BuildAction` error, if `errcode.IsPermanent(err)` → log + ack-drop (unchanged behavior for poison); else → NAK (`msg.Nak()`), so JetStream redelivers with the existing backoff.

**Files:** modify `search-sync-worker/handler.go`; tests in `handler_test.go`.

- [ ] **Step 1: Failing tests** — extend the handler test (use the `fanOutCollection`/stub pattern, `handler_test.go:284-306`). Add a stub collection whose `BuildAction` returns (a) an `errcode.Permanent` error → assert the message is **acked** and not redelivered; (b) a plain error → assert `Nak` is called (capture via the `stubMsg` fake — add a `naked bool`/counter to it).

- [ ] **Step 2: Red.**

- [ ] **Step 3: Implement** — at the `BuildAction` error branch:

```go
actions, err := coll.BuildAction(ctx, msg.Data())
if err != nil {
	if errcode.IsPermanent(err) {
		slog.WarnContext(ctx, "dropping poison message", "error", err, "subject", msg.Subject())
		_ = msg.Ack()
		return
	}
	slog.WarnContext(ctx, "transient build error; will redeliver", "error", err)
	_ = msg.Nak()
	return
}
```

(Match the actual logger/var names in `handler.go`. Keep the existing ack path for the success case.)

- [ ] **Step 4: Green** — `make test SERVICE=search-sync-worker`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "NAK transient sync-worker build errors; ack-drop only poison"`

---

## Task 1.6: search-sync-worker config + wiring (Vault, Mongo DEK, hasher, aux templates)

**Context:** Mirror the atrest wiring at `message-worker/main.go:104-120`. search-sync-worker currently connects only ES+NATS; enc adds Mongo (DEK store coll `room_data_keys` via `atrest.NewMongoDEKStore`) + Vault (`atrest.NewVaultKeyWrapper`). All gated by `ENC_ENABLED` — when false, nothing new connects.

**Files:** modify `search-sync-worker/main.go`; config tested in a small unit test where practical; full path covered by Task 1.7 integration.

- [ ] **Step 1: Failing test** — unit-test a `buildEncOptions(cfg) (encOptions, error)` helper: with `ENC_ENABLED=false` returns `{enabled:false}` and connects nothing; with enabled but missing `BLINDIDX_KEY` returns an error. (Keep Vault/Mongo construction behind interfaces so the helper is unit-testable; or test only the disabled + validation branches and cover the enabled wiring in integration.)

- [ ] **Step 2: Red.**

- [ ] **Step 3: Implement** — add config fields (follow the `bootstrapConfig`/`SyncMessagesFrom` precedents and `env.ParseAs`):

```go
EncEnabled        bool   `env:"ENC_ENABLED" envDefault:"false"`
EncMsgIndexPrefix string `env:"ENC_MSG_INDEX_PREFIX" envDefault:""` // require + StripVersion-validate when EncEnabled
BlindKey          string `env:"BLINDIDX_KEY" envDefault:""`         // hex, required when EncEnabled
BlindKeyVersion   string `env:"BLINDIDX_KEY_VERSION" envDefault:""` // required when EncEnabled
Atrest            atrest.Config       // ATREST_*
Vault             atrest.VaultConfig  // VAULT_* / ATREST_VAULT_*
Mongo             struct{ URI string `env:"MONGO_URI"`; DB string `env:"MONGO_DB" envDefault:"chat"` }
```

When `EncEnabled`: validate the prefix ends `-vN` (`searchindex.StripVersion`), `LoadHasher(cfg.BlindKey, cfg.BlindKeyVersion)`, connect Mongo (`mongoutil.Connect`), build `atrest.NewVaultKeyWrapper` + `atrest.NewCipher(wrapper, atrest.NewMongoDEKStore(db.Collection(atrest.CollectionName)), cfg.Atrest)`, and pass an `encOptions` into `newMessageCollectionEnc(...)`. After the existing template-upsert loop, also upsert `coll.AuxTemplates()`:

```go
for _, c := range collections {
	if err := engine.UpsertTemplate(ctx, c.TemplateName(), c.TemplateBody()); err != nil { /* fatal */ }
	for _, aux := range c.AuxTemplates() {
		if err := engine.UpsertTemplate(ctx, aux.Name, aux.Body); err != nil { /* fatal */ }
	}
}
```

Graceful shutdown: add Mongo disconnect to the cleanup order if connected.

- [ ] **Step 4: Green** — `make test SERVICE=search-sync-worker`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Wire enc config, Vault/Mongo DEK, and aux template upsert in sync-worker"`

---

## Task 1.7: search-sync-worker integration test (encrypted index end-to-end)

**Files:** modify `search-sync-worker/integration_test.go` (or add `integration_enc_test.go`, same `package main`, `//go:build integration`).

- [ ] **Step 1: Failing test** — with `testutil.NATS(t)` + `testutil.Elasticsearch(t)` and a **fake cipher** (avoid standing up Vault: inject a deterministic local AES cipher implementing `encCipher`, or a stub returning fixed bytes), publish a `MessageEvent` to MESSAGES_CANONICAL, run the consumer, refresh the enc index, and assert: a doc exists in `enc-...-YYYY-MM` with `_id == msg.ID`, `contentBlind` equal to `blindsearch.Field(hasher, content)`, `contentEnc` present, plaintext `content` absent. Also assert the enc template was created (`GetIndexMapping`).

  Reuse `loadTestEvents`/`refreshIndex`/`esURLFor` helpers already in the integration file.

- [ ] **Step 2: Red** → **Step 3: make it pass** (most logic already built; this wires the real consumer + ES) → **Step 4:** `make test-integration SERVICE=search-sync-worker`.
- [ ] **Step 5: Commit** — `git commit -am "Integration test: sync-worker writes encrypted parallel index"`

---

## Task 1.8: `SearchMessagesRequest.Variant` + model decode struct

**Files:** modify `pkg/model/search.go`; round-trip covered by `pkg/model/model_test.go` if it enumerates types.

- [ ] **Step 1: Failing test** — assert `SearchMessagesRequest` JSON round-trips with `variant` omitempty and that an absent field decodes to `""`.

- [ ] **Step 2–3: Implement** — add to `SearchMessagesRequest`:

```go
// Variant selects the benchmark arm ("", "C", "A", "B"). Honored ONLY when the
// server runs with SEARCH_BENCH_MODE_ENABLED=true; otherwise the server's
// configured default arm is used. Empty = server default.
Variant string `json:"variant,omitempty"`
```

- [ ] **Step 4: Green** — `make test SERVICE=pkg/model`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Add benchmark Variant to SearchMessagesRequest"`

---

## Task 1.9: search-service enc query clause (blind match on `contentBlind`)

**Context:** `query_messages.go:41-49` builds the content `multi_match` (bool_prefix, AND, field `content`). For the enc path, swap **only** `bool.must` to a `match` on `contentBlind` with `operator: AND` (or `match_phrase` when the query is quoted), built from `blindsearch.Field(hasher, query)`. Leave `bool.filter` (range + access should-clauses, `query_messages.go:50-133`) byte-for-byte. Empty blind field → a clause that matches nothing (avoid match-all).

**Files:** modify `search-service/query_messages.go`; tests in `query_messages_test.go`.

- [ ] **Step 1: Failing tests** — add `buildEncMessageQuery(req, restricted, hasher)` and assert by walking the JSON:
  - `query.bool.must[0]` is `match` on `contentBlind`, `operator==AND`, and the `query` string equals `blindsearch.Field(hasher, req.Query)`.
  - quoted query (e.g. `"\"foo bar\""`) → `match_phrase` on `contentBlind`.
  - `bool.filter` block is identical to what `buildMessageQuery` produces (assert the access/range clauses unchanged — reuse the existing `filterClauses`/`shouldClauses` helpers).
  - a query that analyzes to zero tokens → `must` clause is `{"match_none":{}}` (or `terms contentBlind []` semantics) so nothing matches.

- [ ] **Step 2: Red.**

- [ ] **Step 3: Implement** — factor the existing filter-building into a shared helper if not already, then:

```go
func buildEncMessageQuery(req model.SearchMessagesRequest, restricted restrictedRooms, h *blindidx.Hasher) json.RawMessage {
	must := encContentClause(req.Query, h)
	// ... reuse the EXACT filter block from buildMessageQuery (range + bool.should access) ...
	// assemble {query:{bool:{must:[must], filter:[...]}}, sort, from, size, track_total_hits}
}

func encContentClause(query string, h *blindidx.Hasher) map[string]any {
	field := blindsearch.Field(h, strings.Trim(query, `"`))
	if field == "" {
		return map[string]any{"match_none": map[string]any{}}
	}
	if isQuoted(query) {
		return map[string]any{"match_phrase": map[string]any{"contentBlind": field}}
	}
	return map[string]any{"match": map[string]any{"contentBlind": map[string]any{"query": field, "operator": "AND"}}}
}
```

- [ ] **Step 4: Green** — `make test SERVICE=search-service`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Build blind contentBlind query clause in search-service"`

---

## Task 1.10: Approach A content retrieval (in-process decrypt)

**Context:** `response.go` decodes hits into `messageSearchHit` (reads `_source.content`). For arm A, decode `contentEnc`/`encNonce` from `_source` and `cipher.Decrypt(ctx, roomID, contentEnc, atrest.EncMeta{Nonce: encNonce})` → `EncryptedFields.Msg`.

**Files:** new `search-service/encsearch.go`; modify `response.go`; tests in `encsearch_test.go`.

- [ ] **Step 1: Failing test** — given a fake `decrypter` (interface `Decrypt(ctx, roomID string, payload []byte, meta atrest.EncMeta) (atrest.EncryptedFields, error)`) and a parsed ES response containing `contentEnc`/`encNonce`/metadata, assert `retrieveContentA` fills each `SearchMessage.Content` from the fake's output, and that a decrypt error for one hit is handled per policy (drop that hit's content vs fail — choose: log + empty content for that hit, keep others; assert this).

- [ ] **Step 2: Red.**

- [ ] **Step 3: Implement** — add an enc hit struct (`encMessageSearchHit` with `ContentEnc []byte json:"contentEnc"`, `EncNonce []byte json:"encNonce"`, plus metadata), parse it, and:

```go
func retrieveContentA(ctx context.Context, d decrypter, hits []encMessageSearchHit) []model.SearchMessage {
	out := make([]model.SearchMessage, 0, len(hits))
	for _, hsHit := range hits {
		fields, err := d.Decrypt(ctx, hsHit.RoomID, hsHit.ContentEnc, atrest.EncMeta{Nonce: hsHit.EncNonce})
		content := fields.Msg
		if err != nil {
			slog.WarnContext(ctx, "decrypt search hit failed", "messageId", hsHit.MessageID, "error", err)
			content = ""
		}
		out = append(out, toEncSearchMessage(hsHit, content))
	}
	return out
}
```

- [ ] **Step 4: Green** — `make test SERVICE=search-service`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Approach A: decrypt contentEnc in search-service"`

---

## Task 1.11: history-service batch-by-IDs RPC (Approach B server side)

**Context:** Expose the existing decrypting `Repository.GetMessagesByIDs` (`cassrepo/messages_by_id.go:36`) as a flat NATS RPC. New subject; handler enforces the same per-message access the single `GetMessageByID` path does (HSS/subscription window). history-service uses `internal/` packages — follow that layout.

**Files:** `pkg/subject/subject.go` (+ `subject_test.go`); `history-service/internal/models/message.go` (request/response); `history-service/internal/service/messages_batch.go` (+ test); register in `service.go`.

- [ ] **Step 1: Failing tests**
  - subject: `subject.MsgBatchGet("acc","site-a")` → `chat.user.acc.request.site-a.msg.batchget` and `MsgBatchGetPattern("site-a")` parses `{account}`.
  - handler: with a fake repo returning two decrypted messages and a fake access-checker, `GetMessagesByIDs` returns both; an ID the caller can't access is filtered out; missing IDs are omitted; empty request → `errcode.BadRequest`.

- [ ] **Step 2: Red.**

- [ ] **Step 3: Implement**
  - `subject.go`: add builders mirroring `MsgGet`/`MsgGetPattern` (`subject.go:44`) but flat (not room-scoped).
  - models: `GetMessagesByIDsRequest{ MessageIDs []string json:"messageIds" }`, `GetMessagesByIDsResponse{ Messages []Message json:"messages" }`.
  - handler `messages_batch.go`: validate non-empty (cap size, e.g. ≤200, else `errcode.BadRequest`), call `repo.GetMessagesByIDs(ctx, ids)`, run each through the existing access/visibility check used by `GetMessageByID`, return survivors. Register with `natsrouter.Register(r, subject.MsgBatchGetPattern(siteID), s.GetMessagesByIDs)` in `service.go` (mirror line ~116).

- [ ] **Step 4: Green** — `make test SERVICE=history-service` and `make test SERVICE=pkg/subject`; `make generate SERVICE=history-service` if a store interface changed; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Add history-service batch GetMessagesByIDs RPC"`

---

## Task 1.12: Approach B content retrieval (search-service → history batch RPC)

**Files:** modify `search-service/encsearch.go`; tests in `encsearch_test.go`.

- [ ] **Step 1: Failing test** — fake `historyBatchClient` (interface `GetByIDs(ctx, account string, ids []string) ([]model.Message, error)`); given enc hits (no content in `_source`), assert `retrieveContentB` collects `_id`s, calls the client once, and maps returned `Message.Content` back onto `SearchMessage` by ID (order preserved from ES; missing → empty content).

- [ ] **Step 2: Red.**

- [ ] **Step 3: Implement** — `retrieveContentB(ctx, client, account, hits)`: build `ids := [...]`, `msgs, err := client.GetByIDs(...)`, index `byID := map[string]string`, then project hits in ES order filling content from `byID`. Implement the production `historyBatchClient` as a thin NATS requester (mirror `natsHistoryRequester` style) that publishes to `subject.MsgBatchGet(account, siteID)`.

- [ ] **Step 4: Green** — `make test SERVICE=search-service`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Approach B: fetch content via history batch RPC in search-service"`

---

## Task 1.13: search-service enc-path routing, variant resolution, config wiring

**Context:** `handler.go:73-126` `searchMessages`. Add: resolve the effective arm — if `SEARCH_BENCH_MODE_ENABLED` and `req.Variant != ""` use it, else the configured default (`SEARCH_ENC_DEFAULT_ARM`, one of `C|A|B`; `C` when `SEARCH_ENC_ENABLED=false`). Arm `C` → existing plaintext path + `messages-*` pattern. Arms `A`/`B` → `buildEncMessageQuery` + enc index pattern (`enc-messages-*` + `*:enc-messages-*`) + retrieval A or B.

**Files:** modify `search-service/handler.go`, `main.go`; tests in `handler_test.go`.

- [ ] **Step 1: Failing tests** (hand-fake style, `fakeStore` capturing `indices`+`body`):
  - default `C` → `Search` called with `["messages-*","*:messages-*"]` and a `multi_match content` body.
  - `Variant:"A"` with bench mode on → `Search` called with the enc index pattern and a `match contentBlind` body; content comes from the fake decrypter.
  - `Variant:"A"` with bench mode **off** → variant ignored, behaves as configured default.
  - `Variant:"B"` → enc pattern + fake `historyBatchClient` supplies content.

- [ ] **Step 2: Red.**

- [ ] **Step 3: Implement** — add config (nested, `SEARCH_` prefix mind the shared-prefix foot-gun at `main.go:76-81`):

```go
EncEnabled       bool   `env:"SEARCH_ENC_ENABLED" envDefault:"false"`
EncDefaultArm    string `env:"SEARCH_ENC_DEFAULT_ARM" envDefault:"C"`
EncMsgIndex      string `env:"SEARCH_ENC_MSG_INDEX_PREFIX" envDefault:""`
BenchModeEnabled bool   `env:"SEARCH_BENCH_MODE_ENABLED" envDefault:"false"`
BlindKey         string `env:"BLINDIDX_KEY" envDefault:""`
BlindKeyVersion  string `env:"BLINDIDX_KEY_VERSION" envDefault:""`
// + atrest.Config, atrest.VaultConfig for arm A; history requester (NATS) for arm B
```

Build `*blindidx.Hasher` via `blindsearch.LoadHasher`; build cipher (arm A) like `message-worker/main.go:104-120` (Mongo handle already exists, `main.go:145`); build the history batch client (arm B). Define `var EncMessageIndexPattern = []string{"enc-messages-*","*:enc-messages-*"}` (derive from `EncMsgIndex`). Route in `searchMessages` by resolved arm.

- [ ] **Step 4: Green** — `make test SERVICE=search-service`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -am "Route flag-gated enc search path (arms C/A/B) in search-service"`

---

## Task 1.14: search-service enc integration test (arms A & B)

**Files:** modify/add `search-service/integration_messages_test.go` (httptest ES stub pattern, must send `X-Elastic-Product: Elasticsearch`).

- [ ] **Step 1: Failing test** — stub ES returning enc hits (`contentBlind`/`contentEnc`/`encNonce`/metadata). Arm A: inject fake decrypter → response content matches. Arm B: inject fake history client → response content matches. Assert the captured request body matched `contentBlind` and used the enc index pattern.

- [ ] **Step 2–4:** Red → green → `make test-integration SERVICE=search-service`.
- [ ] **Step 5: Commit** — `git commit -am "Integration test: search-service arms A and B"`

---

## Task 1.15: Phase 1 verification gate

- [ ] `make test` (full, race) → green.
- [ ] `make test-integration SERVICE=search-sync-worker` and `SERVICE=search-service` → green (Docker required).
- [ ] Coverage ≥80% on changed packages: `go test -cover ./pkg/blindsearch/... ./search-sync-worker/... ./search-service/... ./history-service/...`.
- [ ] `make lint` clean; `make sast` clean.
- [ ] Push branch. No commit (verification only).

---

# PHASE 2 — Harnesses (built runnable; numbers produced by running them)

## Task 2.1: Pure quality-metric functions

**Files:** `tools/searchquality/metrics.go` + `metrics_test.go` (package `main` or a `metrics` subpkg). Pure functions, fully unit-tested — no ES.

- [ ] **Step 1: Failing tests** (table-driven) for:
  - `RecallAtK(relevant []string, ranked []string, k int) float64` — fraction of `relevant` present in `ranked[:k]`. Cases: full recall=1.0, half, k>len, empty relevant → define as 1.0.
  - `Jaccard(a, b []string) float64` — |∩|/|∪|; identical→1, disjoint→0, both empty→1.
  - `RBO(a, b []string, p float64) float64` — rank-biased overlap; identical lists→~1.0 (assert ≥0.99 with p=0.9), reversed/disjoint low; document the standard finite formula used.

- [ ] **Step 2–4:** Red → implement (standard formulas; RBO via the cumulative-overlap finite series) → `make test SERVICE=tools/searchquality`.
- [ ] **Step 5: Commit** — `git commit -m "Add pure search-quality metric functions"`

---

## Task 2.2: `_analyze` parity oracle helper

**Files:** add an `Analyze` call to `pkg/searchengine` (method `Analyze(ctx, index, analyzer, text string) ([]string, error)` hitting ES `_analyze`) + test; or implement in `tools/searchquality` using the ES client directly.

- [ ] **Step 1: Failing test** — unit-test the request/response marshalling against a stubbed ES `_analyze` response (`{"tokens":[{"token":"foo"},...]}`) → `["foo",...]`.
- [ ] **Step 2–4:** Red → implement → `make test`.
- [ ] **Step 5: Commit** — `git commit -m "Add ES _analyze parity-oracle helper"`

---

## Task 2.3: Quality runner + report (integration)

**Files:** `tools/searchquality/runner.go`, `report.go`, `integration_test.go` (`//go:build integration`, `testutil.Elasticsearch`, `testutil.RunTests`). Also a `main.go` CLI so it's runnable standalone against a compose ES.

- [ ] **Step 1: Failing integration test** — seed a small multilingual corpus (English / CJK / HTML / mixed — a `testdata/corpus.json`) into index **C** (the real `custom_analyzer` template from `search-sync-worker/messages.go`) and the **blind** index (real enc template + `blindsearch`), run per-language query sets, compute `RecallAtK`/`Jaccard`/`RBO` of blind vs C, and assert each language meets the gate (recall@10 ≥ 0.95, RBO ≥ 0.90 — keep the corpus small enough to pass deterministically, or assert the runner *produces* metrics and writes the report rather than hard-gating in CI). Cross-check every `msganalyzer.Analyze` stream against `_analyze` and record divergences in the report.

- [ ] **Step 2–4:** Red → implement runner + markdown/CSV report writer (`report.md` with per-language tables + parity divergences) → `make test-integration SERVICE=tools/searchquality`.
- [ ] **Step 5: Commit** — `git commit -m "Add search-quality runner, parity oracle, and report"`

---

## Task 2.4: loadgen `search_latency` metric + collector

**Files:** modify `tools/loadgen/metrics.go`; new `tools/loadgen/search_collector.go` + tests.

- [ ] **Step 1: Failing test** — `SearchCollector` (model on `HistoryCollector`, `history_collector.go:40`): records `(arm, latency, at, err)`, `DiscardBefore(cutoff)` drops warmup by `at`, exposes per-arm samples + error tallies; assert percentile/Count helpers. Also assert `Metrics.SearchLatency` is a registered `*prometheus.HistogramVec` with label `arm` (observe one sample, gather, assert).

- [ ] **Step 2–4:** Red → add `SearchLatency: prometheus.NewHistogramVec({Name:"loadgen_search_latency_seconds",Buckets:buckets},[]string{"arm"})` to `NewMetrics` + `MustRegister`; implement collector → `make test SERVICE=tools/loadgen`.
- [ ] **Step 5: Commit** — `git commit -m "Add loadgen search latency metric and collector"`

---

## Task 2.5: loadgen search presets + generator

**Files:** new `tools/loadgen/preset_search.go`, `search_generator.go` + tests (mirror `history.go:26-44` and `HistoryGenerator` `history_generator.go:147,223,275`).

- [ ] **Step 1: Failing tests** — `BuiltinSearchPreset("search-small")` returns a populated preset (rate, duration, sizes, query pool incl. CJK); unknown → `(_, false)`. `SearchGenerator.requestOne` (with a fake `SearchRequester` mirroring `HistoryRequester`, `history_generator.go:124`) builds a `model.SearchMessagesRequest` with `Variant` = the configured arm, picks a query from the multilingual pool, issues the request to `subject.SearchMessages(account, siteID)`, and records into `SearchCollector`. Assert the captured subject + payload + that latency was recorded.

- [ ] **Step 2–4:** Red → implement (copy `HistoryGenerator` open-loop `Run`; per-arm `Variant`) → `make test SERVICE=tools/loadgen`.
- [ ] **Step 5: Commit** — `git commit -m "Add loadgen search presets and generator"`

---

## Task 2.6: loadgen `search-sustained` subcommand + CSV + dashboard

**Files:** new `tools/loadgen/search_main.go`; modify `tools/loadgen/main.go` (switch case + usage) and `deploy/grafana/dashboards/loadtest.json`.

- [ ] **Step 1: Failing test** — `runSearchSustained` flag parsing: `--arm`, `--preset`, `--duration`, `--rate`, `--csv` validate (bad `--arm` → non-zero/err like `ParseInjectMode`, `generator.go:26`); CSV writer emits `CSVSample{TimestampNs,Metric,LatencyNs}` rows per arm (assert via `writeSearchCSVFile` against a populated collector).

- [ ] **Step 2–4:** Red → implement `runSearchSustained` (model on `history_main.go:126-263`): metrics HTTP server, NATS requester (`natsHistoryRequester`-style), generator run under `context.WithTimeout`, CSV + `ComputePercentiles` + `DetermineExitCode`; add `case "search-sustained"` to `dispatch` (`main.go:104`) + usage (`main.go:65`); add a Grafana panel querying `histogram_quantile(0.95, sum(rate(loadgen_search_latency_seconds_bucket[30s])) by (le,arm))`. → `make test SERVICE=tools/loadgen`; `make lint`.
- [ ] **Step 5: Commit** — `git commit -m "Add loadgen search-sustained subcommand, CSV, and Grafana panel"`

---

## Task 2.7: Harness runbook + tiny VFS demo run

**Files:** new `docs/superpowers/runbooks/encrypted-search-harnesses.md`.

- [ ] **Step 1:** Write the runbook: (1) bring up deps via `docker-local/compose.deps.yaml` + services via `compose.services.yaml` with `ENC_ENABLED=true`, `SEARCH_ENC_ENABLED=true`, `SEARCH_BENCH_MODE_ENABLED=true`, a test `BLINDIDX_KEY`; (2) seed a small corpus; (3) run the quality runner → `report.md`; (4) run `loadgen search-sustained --preset search-small --arm C|A|B` and read `loadgen_search_latency_seconds{arm}` in Grafana / the CSV; (5) the Kibana/ES "encrypted-yet-searchable" demo: show an `enc-messages-*` doc (hashes + binary, no plaintext) then a search returning it.
- [ ] **Step 2 (the actual tiny run):** With Docker (VFS), stand up the **minimum** stack (NATS + ES + the two services + Mongo + a Vault dev or the fake-cipher build tag), seed ~20 docs, run `search-sustained --preset search-small --duration 20s --rate 2 --arm C` and one of `A`, export the CSV, and paste the resulting small percentile table + a note into the runbook. Keep numbers tiny (VFS is slow); the goal is "the harness runs and emits a report," not a real benchmark. If the full stack proves too heavy for VFS, document exactly how far it got and run the quality runner (lighter — single ES) to produce a real `report.md` instead.
- [ ] **Step 5: Commit** — `git add docs/superpowers/runbooks/ <generated report/csv> && git commit -m "Add encrypted-search harness runbook and sample small-scale results"`

---

## Task 2.8: Phase 2 verification gate

- [ ] `make test` green; `make test-integration SERVICE=tools/searchquality` green; `make lint` + `make sast` clean. Push.

---

# PHASE 3 — Cutover

## Task 3.1: Make encryption the default & confirm parity

- [ ] Flip `ENC_ENABLED`/`SEARCH_ENC_ENABLED` defaults to `true` in `deploy/docker-compose.yml` files (NOT in code `envDefault` — keep code default `false` so prod opt-in stays explicit; the compose/dev default flips). Update `SEARCH_ENC_DEFAULT_ARM` to the benchmark-chosen arm (default `A` per D6 unless Phase-2 numbers say otherwise — leave a note in the runbook recording the decision). Commit.

## Task 3.2: Drop plaintext content from the encrypted path

- [ ] Confirm the encrypted index template already omits `content`/`custom_analyzer` (Task 1.2). Ensure no enc-path query references `content`. (The plaintext index/template remain only as long as arm C is needed; once the encrypted index is primary, the plaintext `messages-*` write can be disabled behind a flag — add `PLAINTEXT_INDEX_ENABLED envDefault:"true"`, default-true in code, set false in dev compose post-cutover.) TDD any code change; commit.

## Task 3.3: Backfill historical messages into the encrypted index

- [ ] Implement a backfill mode reusing the `SYNC_MESSAGES_FROM` mechanism (`search-sync-worker` already threads `syncFrom`): a one-shot/replay that re-publishes or re-consumes historical canonical messages so the enc index is populated for `createdAt >= SYNC_MESSAGES_FROM`. Document operationally in the runbook; TDD the replay selection logic. Commit.

## Task 3.4: Update `docs/client-api.md`

- [ ] Document: `SearchMessagesRequest.variant` (benchmark-only, ignored unless bench mode), the new `chat.user.{account}.request.{siteID}.msg.batchget` subject + request/response, and that message search results are unchanged on the wire (content still returned plaintext to authorized clients; encryption is server-internal). Note error cases. Commit.

## Task 3.5: Final verification & branch finish

- [ ] `make test` (race) green; all integration suites green; coverage ≥80% on every changed package (target ≥90% for handlers/stores); `make lint` + `make sast` clean.
- [ ] Delete any `docs/reviews/` artifacts if present.
- [ ] Push. Then use **superpowers:finishing-a-development-branch** to open the PR (this is the point a PR is appropriate — the whole feature is complete).

---

## Self-Review (run during planning)

- **Spec coverage:** §5 → Task 1.1 (blindsearch) + Phase 0 (done). §6 (enc index fields, drop custom_analyzer) → 1.2. §4 dual-write + §14.1/.6 → 1.3–1.7. §7 query path (blind match, preserve filters, A decrypt, B fetch) → 1.9–1.14. §14.4 batch RPC → 1.11. §14.5 variant/bench → 1.8, 1.13. §8a quality harness → 2.1–2.3, 2.7. §8b perf harness (search workload, arms, `loadgen_search_latency_seconds{arm}`, presets, CSV/Grafana) → 2.4–2.7. §9 phasing/non-destructive → Phase-1 parallel index + flag; Phase-3 cutover/backfill/client-api → 3.1–3.4. Carry-forward flags: #2 empty-content → 1.1/1.9 (match_none); #3 key decode → 1.1 LoadHasher; #1 Unicode-whitespace parity → measured in 2.3; #4 collision rate → measured in 2.3. **No spec item unmapped.**
- **Placeholder scan:** novel logic (blindsearch, enc template/doc, query swap, decrypt/fetch, NAK semantics, metrics) has concrete code; large mechanical ports (loadgen generator, atrest wiring, history handler) reference an exact existing pattern by file:line for the implementer to mirror — deliberate for a multi-service plan, not a hand-wave.
- **Type consistency:** `EncMessageDoc`/`buildEncDocument`/`encIndexName`/`encOptions`/`encCipher`/`AuxTemplates`/`NamedTemplate`/`buildEncMessageQuery`/`encContentClause`/`retrieveContentA`/`retrieveContentB`/`historyBatchClient`/`SearchMessagesRequest.Variant`/`MsgBatchGet`/`SearchCollector`/`Metrics.SearchLatency` are used consistently across the tasks that define and consume them.
- **Risk note for the executor:** Task 1.4 widens `BuildAction` to take `context.Context` — that ripples to `handler.go` and all three collections; do it as the first edit in 1.4 so the package compiles. Tasks 1.11 (history-service) and 1.13 (search-service main wiring) are the heaviest; budget extra review there.
