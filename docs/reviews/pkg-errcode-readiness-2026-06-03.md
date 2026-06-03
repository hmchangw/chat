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

## Chapter 3 — API design

**Score: 4.5 / 5**

A tight, intentional library. The wire contract is minimal, the daily handler API is two patterns (`errcode.X(msg, opts...)` + one adapter call), invariants are enforced in code, and every "advanced" symbol has at least one external caller (verified — see Surface table below). The few warts are real but small.

### Surface size — every advanced symbol earns its place

| Symbol | External callers | Verdict |
|---|---|---|
| `Classify` | `room-worker/handler.go:136, 1854` (outbox AsyncError formatting); tests in `search-service`, `history-service`, `pkg/natsrouter` | Justified — workers need it to build async-error envelopes for the outbox before publishing. |
| `Parse` | `room-service/memberlist_client.go:66`; `message-gatekeeper/fetcher_history.go:62` | Justified — cross-site reply decoding. |
| `New` | `room-service/memberlist_client.go:99` | Justified — the only legitimate path is Parse-then-New with a dynamically-chosen `Code`. Doc could call this out explicitly. |
| `Permanent` / `IsPermanent` / `ErrPermanent` / `PermanentError` | `inbox-worker/main.go:304`, `inbox-worker/handler.go:192`; `room-worker/handler.go:36, 130, 193, 1691` | Justified — JetStream Ack-vs-Nak decisions. |
| `errnats.Marshal` | `message-gatekeeper/handler.go:75, 88, 103` | Justified — gatekeeper publishes the envelope to a derived subject, not the reply subject; needs raw bytes. |
| `errnats.MarshalQuiet` / `ReplyQuiet` | `pkg/natsrouter/middleware.go:64`, `router.go:124, 207` | Justified — panic backstop / admission overflow paths that already logged. |
| `WithLogger` | Tests only | Could move to `errtest` and unexport, but saving 3 lines isn't worth breaking symmetry with `WithLogValues`. Leave it. |

### Findings

- **`high` — `WithLogValues` looks like an `Option` but isn't.** `logctx.go:18` is a `context.Context` mutator. `WithReason` / `WithMetadata` / `WithCause` all return `Option`. A reader skimming `options.go` reasonably expects `WithLogValues` to match the family. The collision is documented at `doc.go:50-58` ("Never call the package func `errcode.WithLogValues` with a `*natsrouter.Context` as parent"), but documentation isn't enough — rename the package func to break the visual parallelism. Suggested: `WithLogContext(ctx, args...)` or `LogValuesContext(ctx, args...)`.
- **`medium` — `errnats` vs `errhttp` shape divergence.** `errhttp.Write` is 3 lines (`errhttp/write.go:13-16`). `errnats` exposes four public symbols (`Marshal`, `MarshalQuiet`, `Reply`, `ReplyQuiet` — `errnats/reply.go:18-52`). The `Marshal`/`Reply` split is justified by `message-gatekeeper`'s delayed-reply pattern. But `errhttp` has no analogous `Marshal`, even though a Gin handler that wants to inject the envelope into a multi-payload non-2xx response would need one. Today it's "we haven't needed it yet" silence — document or add for symmetry.
- **`medium` — `Quiet` variants are a discoverability hazard.** `MarshalQuiet` / `ReplyQuiet` exist solely to suppress double-logging from `Classify`. Nothing prevents a handler author from reaching for `ReplyQuiet` to silence routine errors, breaking the log-once invariant repo-wide. Consider moving them behind `pkg/errcode/errnats/internal/quiet/` re-exported under a discouraging name, or at minimum a `// Deprecated for handler use` tag.
- **`low` — `New` exported but should arguably stay so.** Single external non-test caller (`memberlist_client.go:99`); semgrep rule `errcode-prefer-named-constructor` already discourages common misuse. Keeping it exported is correct, but its godoc should name the cross-site re-emission use case so reviewers don't flag the lone call site as "should refactor."
- **`low` — `Parse` does not validate `Code` even though `New` panics on invalid code.** `parse.go:11-18` happily returns a non-canonical `Code`; downstream callers must `Code.Valid()` before forwarding (both real callers do — `memberlist_client.go:82`, `fetcher_history.go:62`). Documented at `parse.go:9-10` but a `ParseValidated` helper would encode the warning as a type-level guarantee.
- **`nitpick` — `Reason` is an unenforced open string.** `reason.go:1-5` allows `errcode.WithReason("ad_hoc")` outside any `codes_*.go`. The semgrep rule `errcode-no-reason-literal-outside-catalog` catches it at lint, but not the compiler. Acceptable for an open set.
- **`nitpick` — `errhttp` / `errnats` location.** Living under `pkg/errcode/` rather than as separate `pkg/errhttp`, `pkg/errnats` keeps the adapter contract physically adjacent to the type it adapts. Today this is fine — every caller already depends on both gin and nats.go. No change recommended.

### Recommendations

1. **`high`** — Rename `errcode.WithLogValues` (the package func) to break the visual collision with `Option`-returning `With*` constructors. Update `doc.go §Logging` and the call sites at `room-service/handler.go:78`, `auth-service/handler.go:79, 119, 149, 164`, `message-gatekeeper/handler.go:66, 82`, `room-worker/handler.go:118, 1857`.
2. **`medium`** — Add `errhttp.Marshal(ctx, err) ([]byte, int)` for shape symmetry with `errnats.Marshal`, OR remove `errnats.Marshal` if symmetry the other way is preferred (probably not — gatekeeper needs it).
3. **`medium`** — Move `MarshalQuiet` / `ReplyQuiet` behind an internal package, or rename to `…AfterLog` to encode the invariant in the name.
4. **`low`** — Add `ParseCanonical(data) (*Error, bool)` that rejects non-canonical codes upstream; deprecate raw `Parse` for new cross-site code.
5. **`low`** — Expand `New`'s godoc to name its single legitimate use case (Parse → New for cross-site envelopes).
6. **`nitpick`** — Add a `doc.go` subsection that lists symbols by tier ("Handler API: BadRequest, NotFound, …; Boundary: Classify, Reply, Write; Worker: Permanent, IsPermanent; Cross-site: Parse, New") so the conceptual map surfaces from `go doc pkg/errcode`.

---

