# `pkg/errcode` — Production Readiness Audit

**Target:** library package `pkg/errcode` (and adapter subpackages `errhttp/`, `errnats/`, `errtest/`)
**Date:** 2026-06-03
**Branch:** `claude/sharp-hopper-qzm6W`
**Scope note:** the `production_readiness` skill is shaped for top-level services. This audit is a tailored library-package variant — six dimensions, with **API design** in place of Architecture and **Consumer ergonomics** in place of Integration; the other four (Code quality, Test coverage, Maintainability, Performance) are unchanged.

---

## Executive summary

`pkg/errcode` is a deliberate, small, code-enforced library that does real load-bearing work across every service: it defines the wire contract for client-facing errors, enforces the "log once, never leak the cause" invariant at the boundary, and ships purpose-built adapters for NATS (raw + router) and HTTP. Coverage is 95.2% with explicit invariant tests for the four panics and the JSON-leak guarantee; SAST is clean against the package (the two open `govulncheck` advisories are stdlib and unrelated). The only soft spot is performance discipline: the package sits on every reply hot path and ships zero benchmarks. Three contract-level gaps deserve attention before the next service migrates onto it — a frontend↔backend reason-catalog drift, a stale "7 generic categories" line in client docs, and one naming asymmetry (`WithLogValues` looks like an `Option` but isn't) that a new contributor will trip over.

**Overall: 4.5 / 5 — production-ready, with a short list of pre-scale follow-ups.**

### Dimension scores

| Dimension | Score | One-liner |
|---|---|---|
| Code quality | **5.0** | Idiomatic, doc-driven, SAST-clean; one `nolint`, justified. |
| API design | **4.5** | Minimal surface; every "advanced" symbol has real callers. `WithLogValues` naming collision is the one wart. |
| Test coverage | **5.0** | 95.2% total, 100% in core. All four panics and the leak guarantee have explicit tests. |
| Maintainability | **4.5** | ≤75 LOC per file, clean SRP. `codes_*.go` registry will get awkward around the 20th service. |
| Consumer ergonomics | **4.5** | Tier-1 handler API is one line. Two reasons declared backend-side are missing from the TS `REASON_COPY` map. |
| Performance | **3.5** | Sensible code, but zero benchmarks on a library that runs on every reply. |

### Findings by severity

| Severity | Count |
|---|---|
| critical | 0 |
| high | 3 |
| medium | 10 |
| low | 13 |
| nitpick | 12 |

### Top-line risk assessment

No blockers. The package is shippable as-is to any new service. The **only** items that warrant attention before the next migration:

1. Wire the TS↔Go reason-catalog parity check (2 reasons currently fall through to raw English on the client).
2. Fix the "one of 7 generic categories" line in `docs/client-api.md` (it's 8).
3. Add benchmarks — `Classify` + the two adapters — so future micro-changes (`slog.Attr` migration, `sync.Pool` introduction) have a regression signal.

The remaining items are quality polish: rename `WithLogValues` (the package func) to break its visual collision with `Option`-returning `With*`; auto-derive `allReasons` so the dual-list maintenance disappears; document the `errnats.Marshal` async-reply pattern as a first-class tier rather than a "specialist" footnote.

---

## Chapter 2 — Code quality

**Score: 5 / 5**

### SAST result

- **gosec**: PASS.
- **semgrep**: PASS — 59 rules including the project-local `.semgrep/errcode.yml`, zero findings.
- **govulncheck**: FAIL **at repo level, not against this package**. Two stdlib advisories:
  - `GO-2026-5039` in `net/textproto` (trace: `pkg/searchengine/adapter.go:245`).
  - `GO-2026-5037` in `crypto/x509` (trace: `search-service/main.go:198`, `pkg/idgen/idgen.go:155`).
  Both are cleared by bumping `GOTOOLCHAIN` to `go1.25.11`. No vuln trace touches `pkg/errcode/`.

### Invariants verified end-to-end

- **`cause` never reaches the wire.** `error.go:10` declares `cause error` unexported; no custom `MarshalJSON`; `encoding/json` uses default field reflection. Tests at `error_test.go:25-44` (`MarshalJSON_NeverLeaksCause`), `classify_test.go:62-83`, and `errnats/reply_test.go:128-142` pin the invariant at three independent layers.
- **`WithCause` rejects nested `*Error`.** `options.go:62-71` uses `errors.As`, catching both direct and `%w`-wrapped cases. Tests at `options_test.go:75-94`.
- **`New` panics on non-canonical `Code` or empty message.** `options.go:11-18`, tests `options_test.go:96-121`.
- **`Permanent(nil)` panics.** `permanent.go:17-20`.
- **`WithMetadata` panics on odd-length args.** `options.go:48-50`, tests `options_test.go:59-66`.

### Findings

- **`low` — `Classify` allocates a per-call `[]any` attr slice.** `classify.go:32-39` builds `attrs := []any{"code", ..., "reason", ..., "cause", ...}` and passes via variadic. Hot path for every 4xx reply. Migration to `slog.LogAttrs` with typed `slog.Attr` would dodge `any`-boxing and let escape analysis stack-allocate in handler call frames.
- **`low` — `errnats.Reply` / `ReplyQuiet` log the *reply-failed* fallback via global `slog.ErrorContext`** (`errnats/reply.go:43, 50`) instead of the `loggerFrom(ctx)` chain. Cosmetic asymmetry; would require exporting `loggerFrom` or duplicating it in `errnats`.
- **`nitpick` — `Code.HTTPStatus()` uses bare integer literals** (`category.go:30-49`). `http.StatusBadRequest`-style constants are more grep-able and add no real cost.
- **`nitpick` — `Parse` swallows `json.Unmarshal` errors silently** (`parse.go:13-15`). Already mitigated with `//nolint:nilerr` + reason comment per CLAUDE.md "comment if intentionally discarded" rule. Acceptable as-is.
- **`nitpick` — `MarshalQuiet` doesn't panic on `nil err`** (`errnats/reply.go:30-32`). It silently produces an "internal error" envelope. For parity with `Permanent(nil)` it could panic, but the value is marginal.

### Idiom fit at call sites

Spot-checked `room-service/handler.go:161, 318-321, 423-424, 856, 883`, `auth-service/handler.go:79-167`, `room-worker/handler.go:118-1854`, `message-gatekeeper/handler.go:66-103`. Every site follows the Tier-1 pattern documented in `docs/error-handling.md`: named constructor + optional `WithReason` + occasional `WithMetadata`. No call site reaches past the documented API.

### Recommendations

1. **`nitpick`** — Replace HTTP int literals in `category.go:30-49` with `net/http` constants.
2. **`low`** — Migrate `Classify`'s log call to `slog.LogAttrs` with typed `slog.Attr` for fewer allocations and cleaner intent.
3. **`low`** — Route the "reply failed" log in `errnats.Reply`/`ReplyQuiet` through the ctx logger so it honors `errcode.WithLogger` in tests.
4. **`low`** — Add an explicit `Code.Valid()` example to `Parse`'s godoc for cross-site consumers.
5. **`low`** *(out of scope but flagged)* — Bump `GOTOOLCHAIN` to `go1.25.11` to clear the two open `govulncheck` advisories.

---

