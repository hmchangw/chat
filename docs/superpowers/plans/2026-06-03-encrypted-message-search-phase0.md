# Encrypted Message Search — Phase 0: Foundation Packages — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the two pure, dependency-free packages that all later phases depend on — `pkg/msganalyzer` (a Go port of the ES message analyzer) and `pkg/blindidx` (deterministic HMAC blind-index hashing).

**Architecture:** `msganalyzer.Analyze(text)` reproduces the old ES pipeline (`html_strip → pattern tokenizer → word_delimiter_graph → cjk_bigram → lowercase`) entirely in Go, returning an ordered token stream. `blindidx.Hasher` keyed-hashes each token with HMAC-SHA256 (truncated to 128 bits, hex-encoded) so equal plaintext tokens produce equal, non-reversible search terms. Both are used identically at index time (`search-sync-worker`, later phase) and query time (`search-service`, later phase). Neither package imports anything from the rest of the repo.

**Tech Stack:** Go 1.25, stdlib only (`regexp`, `html`, `unicode`, `strings`, `crypto/hmac`, `crypto/sha256`, `encoding/hex`); tests with `stretchr/testify`.

**Scope note:** This plan covers Phase 0 of `docs/superpowers/specs/2026-06-03-encrypted-message-search-design.md` only. Phases 1–3 (parallel encrypted index, benchmark harnesses, cutover) each get their own plan after this lands.

**Analyzer contract (the spec the tests enforce):** We own both index-time and query-time analysis, so the goal is a **clear, self-consistent** specification — not bug-for-bug Lucene parity. Exact parity is measured later in the Phase 2 quality harness against ES `_analyze`. The contract:
1. `stripHTML` — remove `<…>` tags (replace each with a space so words don't merge across tags), then decode HTML entities.
2. `tokenizePattern` — split on `[\s,;!?()\[\]{}"'<>]+`, drop empty tokens. Underscores, hyphens, dots, `@`, `&`, etc. are **not** split here.
3. `wordDelimiter` — split each token into maximal alphanumeric (Unicode letter/digit) runs. Case changes and letter/digit boundaries do **not** split. If the token contained any delimiter, the original token is preserved **ahead of** its parts; an all-delimiter token yields nothing.
4. `cjkBigram` — convert maximal CJK runs (Han/Hiragana/Katakana/Hangul) to overlapping 2-grams; a lone CJK char is emitted as a unigram; non-CJK segments pass through unchanged.
5. lowercase every surviving token; drop empties.

---

## File Structure

**`pkg/msganalyzer/`** (white-box tests in `package msganalyzer`, mirroring the need to test unexported stages):
- `analyze.go` — package doc + `Analyze(text string) []string` composing the pipeline.
- `htmlstrip.go` — `stripHTML(string) string`.
- `tokenize.go` — `tokenizePattern(string) []string`.
- `worddelimiter.go` — `wordDelimiter(string) []string` + `isWordRune(rune) bool`.
- `cjkbigram.go` — `cjkBigram(string) []string` + `isCJK(rune) bool`.
- `htmlstrip_test.go`, `tokenize_test.go`, `worddelimiter_test.go`, `cjkbigram_test.go`, `analyze_test.go`.

**`pkg/blindidx/`** (black-box tests in `package blindidx_test`, mirroring `pkg/msgbucket`):
- `blindidx.go` — package doc + `Hasher`, `New`, `Version`, `Hash`, `Tokens`.
- `blindidx_test.go`.

---

## Task 1: `stripHTML` char filter

**Files:**
- Create: `pkg/msganalyzer/htmlstrip.go`
- Test: `pkg/msganalyzer/htmlstrip_test.go`

- [ ] **Step 1: Write the failing test**

```go
package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text untouched", in: "hello world", want: "hello world"},
		{name: "tags become spaces", in: "<b>bold</b>", want: " bold "},
		{name: "tag between words does not merge them", in: "a<br>b", want: "a b"},
		{name: "entity decoded", in: "a&amp;b", want: "a&b"},
		{name: "lone lt without close survives", in: "a < b", want: "a < b"},
		{name: "multiline tag stripped", in: "x<div\nclass='y'>z", want: "x z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripHTML(tt.in))
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: FAIL — `undefined: stripHTML`.

- [ ] **Step 3: Write minimal implementation**

```go
package msganalyzer

import (
	"html"
	"regexp"
)

// htmlTagRE matches a single HTML tag, including multi-line tags. (?s) makes
// `.`-class behavior span newlines via the negated class already, but we keep
// it explicit for clarity.
var htmlTagRE = regexp.MustCompile(`(?s)<[^>]*>`)

// stripHTML removes HTML tags and decodes HTML entities, mirroring the
// html_strip char filter that preceded tokenization in the old ES analyzer.
// Each tag is replaced with a space so adjacent words are not merged.
func stripHTML(s string) string {
	return html.UnescapeString(htmlTagRE.ReplaceAllString(s, " "))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/msganalyzer/htmlstrip.go pkg/msganalyzer/htmlstrip_test.go
git commit -m "Add stripHTML stage to msganalyzer"
```

---

## Task 2: `tokenizePattern`

**Files:**
- Create: `pkg/msganalyzer/tokenize.go`
- Test: `pkg/msganalyzer/tokenize_test.go`

- [ ] **Step 1: Write the failing test**

```go
package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenizePattern(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "spaces", in: "hello world", want: []string{"hello", "world"}},
		{name: "punctuation splits", in: "a,b;c!d?e", want: []string{"a", "b", "c", "d", "e"}},
		{name: "brackets and quotes split", in: `("foo")[bar]{baz}`, want: []string{"foo", "bar", "baz"}},
		{name: "underscore is preserved", in: "foo_bar baz", want: []string{"foo_bar", "baz"}},
		{name: "hyphen and dot preserved", in: "a-b.c", want: []string{"a-b.c"}},
		{name: "angle brackets split", in: "a<b>c", want: []string{"a", "b", "c"}},
		{name: "leading and trailing delimiters", in: "  hi  ", want: []string{"hi"}},
		{name: "empty input", in: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tokenizePattern(tt.in))
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: FAIL — `undefined: tokenizePattern`.

- [ ] **Step 3: Write minimal implementation**

```go
package msganalyzer

import "regexp"

// splitRE mirrors the ES "underscore_preserving" pattern tokenizer: it splits
// on whitespace, common punctuation, brackets, quotes, and angle brackets, but
// deliberately NOT on underscores, hyphens, or dots.
var splitRE = regexp.MustCompile(`[\s,;!?()\[\]{}"'<>]+`)

// tokenizePattern splits text into tokens, dropping empty results produced by
// leading, trailing, or repeated delimiters.
func tokenizePattern(s string) []string {
	var out []string
	for _, t := range splitRE.Split(s, -1) {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/msganalyzer/tokenize.go pkg/msganalyzer/tokenize_test.go
git commit -m "Add pattern tokenizer stage to msganalyzer"
```

---

## Task 3: `wordDelimiter`

**Files:**
- Create: `pkg/msganalyzer/worddelimiter.go`
- Test: `pkg/msganalyzer/worddelimiter_test.go`

- [ ] **Step 1: Write the failing test**

```go
package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWordDelimiter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "no delimiter passes through", in: "hello", want: []string{"hello"}},
		{name: "underscore splits with original preserved first", in: "foo_bar", want: []string{"foo_bar", "foo", "bar"}},
		{name: "case change does NOT split", in: "camelCase", want: []string{"camelCase"}},
		{name: "letter-digit boundary does NOT split", in: "foo123", want: []string{"foo123"}},
		{name: "mixed delimiter and digits", in: "foo_bar123", want: []string{"foo_bar123", "foo", "bar123"}},
		{name: "hyphen splits", in: "a-b-c", want: []string{"a-b-c", "a", "b", "c"}},
		{name: "all-delimiter yields nothing", in: "---", want: nil},
		{name: "empty yields nothing", in: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, wordDelimiter(tt.in))
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: FAIL — `undefined: wordDelimiter`.

- [ ] **Step 3: Write minimal implementation**

```go
package msganalyzer

import (
	"strings"
	"unicode"
)

// isWordRune reports whether r is part of a word: a Unicode letter or digit.
// CJK characters are letters and so pass here; they are split into bigrams by
// a later stage, not this one.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordDelimiter mirrors word_delimiter_graph with split_on_case_change=false,
// split_on_numerics=false, preserve_original=true. It splits a token into
// maximal alphanumeric runs; if the token contained any delimiter, the original
// is preserved ahead of its parts. An all-delimiter token yields nothing.
func wordDelimiter(tok string) []string {
	var parts []string
	var b strings.Builder
	hasDelim := false
	flush := func() {
		if b.Len() > 0 {
			parts = append(parts, b.String())
			b.Reset()
		}
	}
	for _, r := range tok {
		if isWordRune(r) {
			b.WriteRune(r)
		} else {
			hasDelim = true
			flush()
		}
	}
	flush()

	if len(parts) == 0 {
		return nil
	}
	if !hasDelim {
		return parts
	}
	out := make([]string, 0, len(parts)+1)
	out = append(out, tok)
	return append(out, parts...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/msganalyzer/worddelimiter.go pkg/msganalyzer/worddelimiter_test.go
git commit -m "Add word_delimiter stage to msganalyzer"
```

---

## Task 4: `cjkBigram`

**Files:**
- Create: `pkg/msganalyzer/cjkbigram.go`
- Test: `pkg/msganalyzer/cjkbigram_test.go`

- [ ] **Step 1: Write the failing test**

```go
package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCJKBigram(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "ascii passes through whole", in: "hello", want: []string{"hello"}},
		{name: "two han chars to one bigram", in: "中文", want: []string{"中文"}},
		{name: "three han chars to overlapping bigrams", in: "中文字", want: []string{"中文", "文字"}},
		{name: "lone cjk char is a unigram", in: "中", want: []string{"中"}},
		{name: "cjk then ascii", in: "中文abc", want: []string{"中文", "abc"}},
		{name: "ascii surrounding single cjk", in: "a中b", want: []string{"a", "中", "b"}},
		{name: "empty", in: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cjkBigram(tt.in))
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: FAIL — `undefined: cjkBigram`.

- [ ] **Step 3: Write minimal implementation**

```go
package msganalyzer

import "unicode"

// isCJK reports whether r is a CJK character handled by bigramming.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// cjkBigram mirrors the cjk_bigram filter: maximal CJK runs become overlapping
// 2-grams (a lone CJK char stays a unigram), while non-CJK segments pass
// through unchanged and in order.
func cjkBigram(tok string) []string {
	runes := []rune(tok)
	var out []string
	for i := 0; i < len(runes); {
		if isCJK(runes[i]) {
			j := i
			for j < len(runes) && isCJK(runes[j]) {
				j++
			}
			run := runes[i:j]
			if len(run) == 1 {
				out = append(out, string(run))
			} else {
				for k := 0; k+1 < len(run); k++ {
					out = append(out, string(run[k:k+2]))
				}
			}
			i = j
			continue
		}
		j := i
		for j < len(runes) && !isCJK(runes[j]) {
			j++
		}
		out = append(out, string(runes[i:j]))
		i = j
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/msganalyzer/cjkbigram.go pkg/msganalyzer/cjkbigram_test.go
git commit -m "Add cjk_bigram stage to msganalyzer"
```

---

## Task 5: `Analyze` pipeline composition

**Files:**
- Create: `pkg/msganalyzer/analyze.go`
- Test: `pkg/msganalyzer/analyze_test.go`

- [ ] **Step 1: Write the failing test**

```go
package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "lowercases", in: "Hello WORLD", want: []string{"hello", "world"}},
		{
			name: "html stripped then tokenized",
			in:   "<p>Hello, World!</p>",
			want: []string{"hello", "world"},
		},
		{
			name: "underscore identifier keeps original and parts, lowercased",
			in:   "Foo_Bar",
			want: []string{"foo_bar", "foo", "bar"},
		},
		{
			name: "cjk bigrammed and lowercased ascii",
			in:   "中文字 ABC",
			want: []string{"中文", "文字", "abc"},
		},
		{
			name: "mixed html cjk and identifier",
			in:   "<b>用户_Name</b> 中文",
			want: []string{"用户_name", "用户", "name", "中文"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Analyze(tt.in))
		})
	}
}

// Self-consistency is the real invariant: the same text analyzed twice must
// produce identical token streams (index-time == query-time).
func TestAnalyze_Deterministic(t *testing.T) {
	in := "<i>Quarterly</i> revenue_报告 up 12%"
	assert.Equal(t, Analyze(in), Analyze(in))
}
```

Note on the `用户_name` case: `用户` are Han characters, so `wordDelimiter` keeps `用户_Name` as one token then splits on `_` into original `用户_Name` + parts `用户`, `Name`; `cjkBigram` turns the 2-char run `用户` into the single bigram `用户` and leaves `Name`; lowercase yields the listed output.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: FAIL — `undefined: Analyze`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package msganalyzer is a Go port of the Elasticsearch message-content
// analyzer (html_strip → pattern tokenizer → word_delimiter_graph →
// cjk_bigram → lowercase). It exists so message content can be tokenized
// outside Elasticsearch, allowing the resulting tokens to be blind-hashed
// (see pkg/blindidx) before indexing. The SAME Analyze function must be used
// at index time and query time so hashed terms line up.
package msganalyzer

import "strings"

// Analyze runs the full pipeline and returns the ordered token stream.
func Analyze(text string) []string {
	stripped := stripHTML(text)
	var out []string
	for _, tok := range tokenizePattern(stripped) {
		for _, wd := range wordDelimiter(tok) {
			for _, cj := range cjkBigram(wd) {
				if low := strings.ToLower(cj); low != "" {
					out = append(out, low)
				}
			}
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/msganalyzer`
Expected: PASS.

- [ ] **Step 5: Run lint, then commit**

Run: `make lint`
Expected: no findings in `pkg/msganalyzer`.

```bash
git add pkg/msganalyzer/analyze.go pkg/msganalyzer/analyze_test.go
git commit -m "Compose msganalyzer Analyze pipeline"
```

---

## Task 6: `blindidx.Hasher`

**Files:**
- Create: `pkg/blindidx/blindidx.go`
- Test: `pkg/blindidx/blindidx_test.go`

- [ ] **Step 1: Write the failing test**

```go
package blindidx_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/blindidx"
)

// 32-byte test key; never used outside tests.
var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestNew_RejectsShortKey(t *testing.T) {
	_, err := blindidx.New([]byte("too-short"), "v1")
	require.Error(t, err)
}

func TestNew_RejectsEmptyVersion(t *testing.T) {
	_, err := blindidx.New(testKey, "")
	require.Error(t, err)
}

func TestHasher_Version(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", h.Version())
}

func TestHash_DeterministicAndOpaque(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)

	got := h.Hash("salary")
	// Deterministic: same input, same output.
	assert.Equal(t, got, h.Hash("salary"))
	// Opaque: 32 lowercase hex chars (128-bit truncation), never the plaintext.
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), got)
	assert.NotContains(t, got, "salary")
}

func TestHash_DistinctTokensDiffer(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)
	assert.NotEqual(t, h.Hash("salary"), h.Hash("bonus"))
}

func TestHash_DifferentKeysProduceDifferentTerms(t *testing.T) {
	h1, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)
	otherKey := []byte("FEDCBA9876543210FEDCBA9876543210")
	h2, err := blindidx.New(otherKey, "v1")
	require.NoError(t, err)
	assert.NotEqual(t, h1.Hash("salary"), h2.Hash("salary"))
}

func TestTokens_PreservesOrderAndLength(t *testing.T) {
	h, err := blindidx.New(testKey, "v1")
	require.NoError(t, err)

	in := []string{"a", "b", "a"}
	got := h.Tokens(in)
	require.Len(t, got, 3)
	assert.Equal(t, h.Hash("a"), got[0])
	assert.Equal(t, h.Hash("b"), got[1])
	// Equal plaintext tokens map to equal terms (required for matching).
	assert.Equal(t, got[0], got[2])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/blindidx`
Expected: FAIL — `undefined: blindidx.New` (package does not compile yet).

- [ ] **Step 3: Write minimal implementation**

```go
// Package blindidx produces deterministic, non-reversible search terms
// ("blind index" tokens) by keyed-hashing analyzer tokens with HMAC-SHA256.
// Equal plaintext tokens hash to equal terms so Elasticsearch can match them,
// but the plaintext cannot be recovered from a term. The key is a single
// federation-wide secret, kept distinct from any pkg/atrest encryption key
// (key separation: one key per cryptographic purpose).
package blindidx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const (
	minKeyLen = 32 // HMAC-SHA256 key floor
	termBytes = 16 // 128-bit truncation -> 32 hex chars, whitespace-free
)

// Hasher hashes tokens with a fixed key and carries the active key version so
// callers can stamp documents for future rotation (versioned reindex).
type Hasher struct {
	key     []byte
	version string
}

// New returns a Hasher. The key must be at least 32 bytes and the version must
// be non-empty. The key is copied so the caller may reuse its buffer.
func New(key []byte, version string) (*Hasher, error) {
	if len(key) < minKeyLen {
		return nil, errors.New("blindidx: key must be at least 32 bytes")
	}
	if version == "" {
		return nil, errors.New("blindidx: version must not be empty")
	}
	k := make([]byte, len(key))
	copy(k, key)
	return &Hasher{key: k, version: version}, nil
}

// Version returns the key version label for this Hasher.
func (h *Hasher) Version() string { return h.version }

// Hash returns the blind-index term for a single token: the first 16 bytes of
// HMAC-SHA256(key, token), hex-encoded.
func (h *Hasher) Hash(token string) string {
	mac := hmac.New(sha256.New, h.key)
	mac.Write([]byte(token))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:termBytes])
}

// Tokens hashes a token stream in order, preserving length.
func (h *Hasher) Tokens(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = h.Hash(t)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/blindidx`
Expected: PASS.

- [ ] **Step 5: Run lint, then commit**

Run: `make lint`
Expected: no findings in `pkg/blindidx`.

```bash
git add pkg/blindidx/blindidx.go pkg/blindidx/blindidx_test.go
git commit -m "Add blindidx HMAC blind-index hasher"
```

---

## Task 7: Phase-0 verification gate

**Files:** none (verification only).

- [ ] **Step 1: Run the full unit suite with the race detector**

Run: `make test`
Expected: PASS across the repo (the two new packages included; nothing else changed).

- [ ] **Step 2: Confirm coverage meets the 80% floor for the new packages**

Run: `go test -cover ./pkg/msganalyzer/... ./pkg/blindidx/...`
Expected: each package reports ≥ 80% (these are pure functions exercised by table tests; expect well above 90%). If below, add cases for the uncovered branch and re-run.

- [ ] **Step 3: Run SAST (blocking CI gate)**

Run: `make sast`
Expected: no medium+ findings. (HMAC-SHA256 and stdlib regexp/html are clean; there is no `InsecureSkipVerify` or unsafe conversion here.)

- [ ] **Step 4: Final lint**

Run: `make lint`
Expected: clean.

No commit — this task only verifies the gate before Phase 1.

---

## Self-Review (completed during planning)

- **Spec coverage:** Phase 0 of the spec lists exactly `pkg/msganalyzer` (the five-stage pipeline) and `pkg/blindidx` (HMAC, 16-byte truncation, key separation, version seam). Tasks 1–5 build and compose the five stages; Task 6 builds the hasher including the `Version()` rotation seam (spec D5) and key-separation doc; Task 7 enforces the spec's ≥80% coverage and SAST gates. No Phase-0 requirement is unmapped.
- **Placeholder scan:** every code and test step contains complete, runnable Go; no TBD/TODO/"handle edge cases".
- **Type consistency:** `Analyze(string) []string`, `stripHTML`, `tokenizePattern`, `wordDelimiter`, `cjkBigram`, `isWordRune`, `isCJK` are used consistently across tasks; `blindidx.New(key []byte, version string) (*Hasher, error)`, `Version()`, `Hash(string) string`, `Tokens([]string) []string` are consistent between Task 6's test and implementation. The `用户_name` expected output in Task 5 is traced through all four stages in the accompanying note.
- **Convention check:** package layout, testify usage, table-driven `t.Run`, and package-doc style match `pkg/msgbucket`; commands use `make` targets (`make test SERVICE=pkg/<name>` → `go test -race ./pkg/<name>/...`).
