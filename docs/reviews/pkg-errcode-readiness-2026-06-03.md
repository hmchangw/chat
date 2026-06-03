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

## Chapter 4 — Test coverage

**Score: 5 / 5**

### Coverage numbers

| Package | Coverage |
|---|---|
| `pkg/errcode` | **100.0%** |
| `pkg/errcode/errhttp` | **100.0%** |
| `pkg/errcode/errnats` | 73.3% (4 uncovered statements — log-on-Respond-error + 2 marshal-fail fallbacks) |
| `pkg/errcode/errtest` | 88.9% |
| **Total** | **95.2%** |

All four packages well above the 80% floor in CLAUDE.md Section 4.

### Test pass/fail

**PASS.** `go test -race -count=1 ./pkg/errcode/...` is clean across all four packages in ~1s each. Race detector enforced via Makefile and verified explicitly.

### Mock staleness

**Clean for `pkg/errcode`.** `make generate` fails repo-wide due to a `mockgen` toolchain mismatch (built against go1.24, several services on go1.25 — `broadcast-worker`, `history-service`, `message-gatekeeper`, `message-worker`, `room-worker`, `search-sync-worker`, `tools/nats-debug`). **No `pkg/errcode` mocks are stale.** The repo-wide failure is out of audit scope but worth flagging separately.

### Integration tests

None present, appropriately. `errnats` tests spin up an in-process `nats-server` directly (`reply_test.go:23-36`) without the integration tag.

### Findings

- **`low` — `errnats.Reply` / `ReplyQuiet` log-on-Respond-error branches uncovered** (`errnats/reply.go:42-44, 49-51`). Triggering requires `nc.Close()` before `Respond`; awkward but possible. Two-statement fallback — cosmetic.
- **`low` — `errnats.Marshal` / `MarshalQuiet` `json.Marshal` failure fallbacks uncovered** (`errnats/reply.go:20-22, 33-37`). Effectively dead-branch defensive code — `*Error` contains only `string` / `map[string]string` / canonical-set enums, all of which marshal infallibly.
- **`low` — `errtest.AssertCode` / `AssertReason` early-return-after-Decode-failure paths uncovered** (`errtest/assert.go:26-28, 38-40`). Easy fix: pass a non-envelope payload to `AssertCode(rt, …)` with a `recordingT`. ~6 lines of test, lifts errtest to 100%.
- **`nitpick` — `category_test.go` is cursorier than its siblings.** 22 LOC, 100% coverage. No action.

### Strengths

- **Wire-shape invariants are explicitly asserted at three layers:** `error_test.go:25-44` (`MarshalJSON_NeverLeaksCause`), `classify_test.go:62-83`, `errnats/reply_test.go:128-142` (`raw cause must NOT appear on the wire` + `must appear in the SERVER log`). The single most important invariant in the package has triple-redundant test coverage.
- **All four panic invariants have dedicated tests** — `New` non-canonical Code, `New` empty message, `WithMetadata` odd args, `WithCause` nested errcode (both direct and `%w`-wrapped).
- **Log-level matrix is explicit** — `classify_test.go:121-143` table-drives 4xx→INFO, internal→ERROR, unavailable→ERROR.
- **`errtest` helpers are themselves tested** via a `recordingT` fixture (`assert_test.go:21-48`).
- **TDD evidence is strong** — every exported function has at least one dedicated test, most with multiple scenarios.

### Recommendations

1. **`low`** — Add the 6-line `errtest` test that exercises the guarded early-returns in `AssertCode` / `AssertReason`. Lifts `errtest` from 88.9% to 100%.
2. **`low`** — Optionally cover the `Reply` Respond-error path by closing the connection before responding. Lifts `errnats` from 73.3% to ~95%. Skip if you treat that branch as dead-defensive.
3. **`nitpick`** — Consider a `testing.F` fuzz test for `Parse` to harden cross-site envelope decoding for Tier-3 callers (`memberlist_client.go`).
4. **`nitpick`** — Add a "Classify is not idempotent" test — calling `Classify(ctx, Classify(ctx, err))` should log twice. Pins the per-boundary log-once contract from `docs/error-handling.md`.
5. **`low`** *(out of scope but flagged)* — Fix the repo-wide `mockgen` toolchain mismatch so `make generate` is a reliable staleness signal again.

---

## Chapter 5 — Maintainability

**Score: 4.5 / 5**

A senior Go engineer can extend this confidently with near-zero hand-holding. The package is small, well-factored, internally consistent, and backed by an unusually thorough design doc. The one real soft spot is the `codes_*.go` registry as service count grows.

### File-size table

| File | LOC |
|---|---|
| `pkg/errcode/category.go` | 49 |
| `pkg/errcode/classify.go` | 51 |
| `pkg/errcode/codes_auth.go` | 10 |
| `pkg/errcode/codes_message.go` | 11 |
| `pkg/errcode/codes_platform.go` | 11 |
| `pkg/errcode/codes_room.go` | 24 |
| `pkg/errcode/doc.go` | 63 |
| `pkg/errcode/error.go` | 21 |
| `pkg/errcode/logctx.go` | 28 |
| `pkg/errcode/match.go` | 15 |
| `pkg/errcode/options.go` | 71 |
| `pkg/errcode/parse.go` | 18 |
| `pkg/errcode/permanent.go` | 41 |
| `pkg/errcode/reason.go` | 5 |
| `pkg/errcode/errhttp/write.go` | 16 |
| `pkg/errcode/errnats/reply.go` | 52 |
| `pkg/errcode/errtest/assert.go` | 44 |
| **Production total** | **567** |
| Tests total | 780 |

No file exceeds 75 lines of production code. SRP holds throughout.

### Findings

- **`medium` — `codes_*.go` registry doesn't scale by construction.** `codes_test.go:8-18` hard-codes `allReasons` as a literal slice. Each new reason needs an entry in *two* places: the catalog file AND `allReasons`. With 4 catalogs and 24 reasons today it's fine; at the 20th service this becomes a merge-conflict hotspot and a place to silently forget. `docs/error-handling.md:226-230` lists step (3) "Add the constant to allReasons" but nothing enforces it.
- **`medium` — `codes_platform.go` blurs catalog ownership.** `codes_<service>.go` is named for ownership, but `codes_platform.go:1-11` holds reasons emitted by *middleware*, not a service. The naming convention is now "per-service OR cross-cutting", which weakens the rule a new contributor would learn.
- **`low` — `MarshalQuiet` / `ReplyQuiet` have very narrow legitimate use.** Three production call sites (`pkg/natsrouter/middleware.go:64`, `router.go:124, 207`). The doc warns repeatedly that "Quiet" is a footgun — with such a small caller set this pair is one mis-use away from a silent error.
- **`low` — `Marshal` in `errnats/reply.go:18-24` is essentially a half-Reply.** Only used in `message-gatekeeper/handler.go:75, 88, 103` because the gatekeeper has a custom `sendReply` that publishes to a derived subject. Promote to a documented Tier-3 specialist in `doc.go` (currently only in `CLAUDE.md`).
- **`low` — `Code.HTTPStatus` returns `int` literals instead of `http.StatusXxx`** (`category.go:30-49`). Cosmetic, but `net/http` constants are more grep-able.
- **`low` — `Classify`'s cause/underlying double-attribute is subtle** (`classify.go:28-39`). When `hasErrcode && err == e`, `cause` repeats the user-safe message; when there's a wrapping `fmt.Errorf` on top of a typed error, `cause` is the wrapper's `err.Error()` and `underlying` is the typed `e.cause.Error()`. The inline comment at L26-27 explains the optimization but not the three-state matrix. Extract a `causeAttr` helper or add an ASCII matrix comment.
- **`low` — `Parse` returns non-validated `Code`** (`parse.go:11-18`). The comment warns callers to call `Code.Valid()`; both real callers do. Safer would be `Parse` itself rejecting non-canonical `Code`, with a `ParseLenient` escape hatch.
- **`nitpick` — `reason.go` is 5 lines.** Could be folded into `codes_*.go` or `error.go`. Standalone is defensible (mirrors `category.go` for `Code`).
- **`nitpick` — No dead code, no `TODO`/`FIXME` markers, every exported symbol carries an accurate doc comment.** Verified across all files.
- **`nitpick` — Adapter import cycle is one-way and minimal.** `errnats` / `errhttp` / `errtest` each import only `pkg/errcode`; core has no adapter import. Clean.

### Recommendations

1. **`medium`** — Auto-derive `allReasons` via `go:generate` (walk the package AST for `Reason`-typed constants) or a reflection-based init test. Removes the dual-list maintenance and the "20th service" footgun.
2. **`medium`** — Add a "Catalogs" section to `doc.go` describing the `codes_*.go` convention, the two flavors (per-service / cross-cutting), and the four-step add-a-reason checklist (currently only in `docs/error-handling.md`).
3. **`low`** — Make `Parse` strict-by-default: reject envelopes with `!Code.Valid()`. Expose `ParseLenient` for cross-site fan-in.
4. **`low`** — Replace integer literals in `category.go:30-49` with `http.StatusXxx`.
5. **`low`** — Reduce `errnats` adapter surface to `Reply` + `ReplyQuiet` + (deliberately) `Marshal`, and document the latter two as Tier-3 in `doc.go`.
6. **`low`** — Add a 1-line `// Adding a new category:` note in `category.go` pointing to the 3 places to keep in sync (`Code` const, `Valid()` switch, `HTTPStatus()` switch) — or fold those into one `var codeStatus = map[Code]int{…}` and derive `Valid()` from it.
7. **`nitpick`** — In `classify.go:28-39`, extract a `causeAttr(err, e, hasErrcode)` helper or add a 3-line ASCII matrix comment.

### Highlights worth preserving

- `doc.go:1-63` gives the mental model in under a minute, including the "two types by design", leak guarantee, wrapping invariant, and the why behind the `*natsrouter.Context` method/func split.
- Constructors `panic` early on programmer errors (`options.go:11-23, 49-50, 65-67`; `permanent.go:17-20`) — the right call at a library boundary.
- The Tier-1/2/3 framing in `CLAUDE.md` plus semgrep enforcement (`docs/error-handling.md:269-278`) keeps most call sites one-line and uniform.
- Every public symbol carries an accurate Go doc comment, and comments earn their place — they explain *why* (e.g. `classify.go:26-27`, `permanent.go:7-11`, `options.go:26-27`).

---

## Chapter 6 — Consumer ergonomics

**Score: 4.5 / 5**

Among the strongest library APIs in the repo. A new contributor lands in `doc.go`, picks a named constructor, returns it, and the boundary adapter does everything else. The footguns that exist are either guarded by runtime panics, semgrep, or both. One real wire-contract gap and a handful of nitpicks keep this from a perfect 5.

### Reasons declared backend-side vs frontend `REASON_COPY` coverage

23 reasons declared across `pkg/errcode/codes_*.go`; 17 mapped in `chat-frontend/src/api/_transport/asyncJob.ts:94-112`.

**Missing from `REASON_COPY` but emitted by backend:**

| Reason | Source | Status |
|---|---|---|
| `non_channel_operation` (`RoomNonChannelOperation`) | `codes_room.go:23`; emitted at `room-service/helper.go:30, 31, 64`, `room-worker/handler.go:312` | **High** — actually emitted, no humanized copy |
| `request_id_required` (`RequestIDRequired`) | `codes_platform.go:10`; emitted by `natsutil.RequireRequestID` on every strict path | **High** — actually emitted, no humanized copy |
| `sso_token_expired`, `invalid_sso_token` | `codes_auth.go:5-6` | OK — intentional per TS comment (drives redirect) |
| `invalid_request`, `invalid_nkey`, `missing_fields` | `codes_auth.go:7-9` | OK — auth form-validation, surfaced via own UX |

Two client-facing reasons fall through to raw English on the client today.

### Findings

- **`high` — Catalog drift between Go reasons and TS `REASON_COPY` is not enforced.** `non_channel_operation` and `request_id_required` (see table above). The snake-case test (`codes_test.go:22-31`) and `docs/error-handling.md:230-236` step 5 ask contributors to update `client-api.md` and the TS map, but nothing fails CI when they forget.
- **`high` — `docs/client-api.md:2186` says "one of 7 generic categories"; the closed set is 8.** `CodeUnavailable` (`category.go:13`) is in the catalog table at `docs/client-api.md:2209` but the prose count is stale. The frontend TS comment (`asyncJob.ts:33`) correctly reads "7+1". Minor doc bug, but the first paragraph a client integrator reads.
- **`medium` — Boilerplate at raw-NATS call sites.** Every `room-service/handler.go` entry handler spends ~5 lines on `wrappedCtx` + double `errnats.Reply` pattern (e.g., `handler.go:126-139`, repeated at `:373, :389, :403, :466, :626, :713, :985, :1123, :1244, :1340, :1474, :1533`). ~75 lines of pure plumbing across one file. `pkg/natsrouter` solves this for `history-service` (one line per handler — see `messages.go:28-36`). Recommendation: migrate room-service to `natsrouter`.
- **`medium` — `WithCause` panic vs the `Permanent(Internal(WithCause(ErrX)))` idiom.** `room-worker/handler.go:1240, 1885` does `permanent(errcode.Internal("room key absent", errcode.WithCause(errRoomKeyAbsent)))`. Safe today because `errRoomKeyAbsent` is a raw sentinel, but `room-service/helper.go:73` defines a same-named sentinel that IS an `*errcode.Error`. A new contributor reusing the wrong package's sentinel would trip `options.go:66`'s panic at runtime, in a JetStream handler, on a code path that only fires under cache miss. Semgrep catches the `WithCause(errcode.X(...))` literal but not `WithCause(somePkgLevelVar)` where the var is typed `*Error`.
- **`medium` — `errnats.Marshal` is on the "specialist" tier but used in mainline code.** `message-gatekeeper/handler.go:75, 88, 103`. The doc could give it a first-class section: "When the reply target is computed (async-job pattern), use Marshal + publish; do NOT use Reply."
- **`medium` — Two diverging ways to attach log values.** `errcode.WithLogValues(ctx, …)` (package func) for Gin/raw NATS, `c.WithLogValues(…)` (method) for natsrouter. Documented at `doc.go:52-58` and `error-handling.md:204-216`, but easy to confuse.
- **`low` — `Parse` returns `(*Error, bool)` with no Code validation.** Documented at `parse.go:7-10`. Today only `inbox-worker` cross-site path uses it. Safe today; brittle if anyone uses Parse to reconstruct without the `Code.Valid()` check.
- **`low` — No constructor for `request_id_required` on auth-service.** Strict path in `natsutil.RequireRequestID` covers NATS handlers; the equivalent HTTP path in `auth-service/middleware.go:18-30` runs mint-on-missing. Fine for auth (no dedup); a contributor adding a dedup-critical HTTP endpoint won't find a helper.
- **`nitpick` — `errtest.Decode` returns `nil` on Fatalf path** (`errtest/assert.go:17`). Comment explains it's for recording mocks. Nil-check pattern in `AssertCode`/`AssertReason` (`assert.go:26, 38`) is non-obvious.
- **`nitpick` — `errnats.Reply` swallows the message body on a marshal failure.** `reply.go:42` falls back to a hard-coded `"internal"` envelope (`reply.go:15`); drops the original `request_id` from the log. Low impact (marshal errors on `*Error` are essentially impossible).
- **`nitpick` — `room-worker` defines local aliases `errPermanent` (`handler.go:36`) and `permanent(ec)` (`handler.go:130`).** Pattern works, but if four services each invent their own shim, the "use the named API" guidance fragments.

### Adapter symmetry

`errnats.Reply(ctx, msg, err)` / `errhttp.Write(ctx, c, err)` are perfectly symmetric: 1-line, identical signature shape, both classify+log+marshal. `natsrouter` handlers are zero-line (return the error). The asymmetric case is `errnats.Marshal` for the gatekeeper/async-job pattern — works but is filed as "Tier 3 specialist" while serving mainstream traffic.

### Discoverability

`doc.go` is a strong entry point but does NOT link to `docs/error-handling.md` or `docs/client-api.md §6`. A contributor landing via `go doc` won't discover them.

### Footguns — what the package catches vs trusts

| Footgun | Detection |
|---|---|
| `WithCause(otherErrcodeErr)` | runtime panic (`options.go:66`) + semgrep |
| Multi-`%w` two errcode errors | semgrep only |
| Inline `Reason("foo")` outside catalog | semgrep only |
| `errcode.New(CodeBadRequest, …)` instead of `BadRequest(…)` | semgrep WARNING only |
| `New` with non-canonical Code | runtime panic (`options.go:11`) |
| Empty message | runtime panic (`options.go:16`) |
| `WithMetadata` odd-len | runtime panic (`options.go:48`) |
| log-then-reply double-log | **trust contributor** (doc warns; nothing detects) |
| New `Reason` without TS REASON_COPY | **trust contributor** (no test) |
| Frontend reading raw English on a missing reason | **silently degrades** to `err.message` |

### Recommendations

1. **`high`** — Add a TS↔Go catalog parity check. A small Go test that reads `chat-frontend/src/api/_transport/asyncJob.ts` `REASON_COPY` keys and diffs against `allReasons` (excluding the explicit auth allowlist of redirect-/form-only reasons). Add `non_channel_operation` and decide whether `request_id_required` deserves humanized copy or stays raw.
2. **`high`** — Fix the "7 generic categories" → "8" count in `docs/client-api.md:2186`.
3. **`medium`** — Migrate `room-service` to `pkg/natsrouter` so `wrappedCtx` and the double `errnats.Reply` collapse to the one-line-per-handler shape `history-service` enjoys. Deletes ~75 boilerplate lines.
4. **`medium`** — Expose `errnats.Publish(ctx, nc, subject, err)` (or document the gatekeeper pattern as a first-class tier-2 adapter in CLAUDE.md).
5. **`medium`** — Have `doc.go` link to `docs/error-handling.md` and `docs/client-api.md §6` explicitly so `go doc errcode` surfaces them.
6. **`low`** — Add `errtest.AssertMetadata(t, data, k, v)`. Trivial; closes the last common assertion.
7. **`low`** — Drop the local `errPermanent` / `permanent(...)` aliases in `room-worker/handler.go:36, 130` in favor of the package API; document a "no service-local shim" rule in CLAUDE.md before a second service copies the pattern.

---

## Chapter 7 — Performance

**Score: 3.5 / 5**

Code is sensibly written and avoids obvious traps, but the package sits on every reply hot path and ships **zero benchmarks**, which is a real gap for a library with this footprint (246 non-test call sites, 46 reply/write sites).

### Allocations per reply (estimated, BadRequest no-cause path)

- `errcode.Classify`: `attrs := []any{...}` slice header + backing array (1 alloc, ~64B for 6 elements; no boxing — `Code`/`Reason` are `string`-kinded so they fit in the iface header).
- `json.Marshal(e)`: encoder state + output byte slice (~2-3 allocs, ~80-120B for a typical 4-field envelope).
- `loggerFrom`: 0 allocs (typed assertion on a pointer).
- `slog.Logger.Log`: depends on handler; the variadic `attrs ...any` is already a `[]any`.
- `errors.As` walk: 0 allocs when chain is shallow.
- **Total: ~3-4 allocs / ~150-250B per BadRequest reply**, plus whatever the slog handler does. Internal-with-cause adds 1 alloc for `err.Error()` and 1 for the `"underlying"` append.

### Bench presence

**FAIL.** `grep -l "func Benchmark" pkg/errcode/ -r` returns nothing. For a library on every request reply path repo-wide, this is a `medium` finding on its own.

### Findings

- **`medium` — No benchmarks for a hot-path library.** Whole `pkg/errcode/` tree has no `Benchmark*` functions. No perf regression signal for `slog.Attr` rework, accidental `fmt.Sprintf`, or `attrs` shape changes. `classify.go`, `errnats/reply.go`, `errhttp/write.go` each deserve at least a 10-line bench.
- **`medium` — No "perf invariants" section in `doc.go`.** A short list would codify what's currently implicit: (a) Classify ≤4 allocs on the BadRequest path, (b) no `fmt.Sprintf` in Classify or the adapters, (c) no mutex anywhere (the package is currently lock-free — `logctx.go` only does `ctx.Value`, `classify.go` has no shared state, `error.go` is immutable post-construction).
- **`low` — `attrs []any` literal escapes to heap on every Classify call** (`classify.go:32-39`). The escape analyzer cannot prove the slog handler doesn't retain it, so this is a guaranteed heap alloc per reply. Migration to `slog.LogAttrs` with typed `slog.Attr` would let escape analysis stack-allocate in many cases AND avoid the `any`-boxing of `string(e.Code)`.
- **`low` — `err.Error()` allocation on the non-errcode path** (`classify.go:29-31`). For the Internal-wrap-of-raw-infra-error path (very common — every DB failure), `cause = err.Error()` produces a fresh string. Unavoidable unless slog formats lazily; the guard at L28 already dodges it for the "bare errcode" case.
- **`low` — `WithCause` runs `errors.As` on every construction** (`options.go:64-65`). Defensive, prevents nested errcode chains, panics if violated. Amortized away on the happy path; semgrep already catches static cases. For high-frequency `Unavailable(... WithCause(err))` paths (e.g., `message-gatekeeper/handler.go:294`) it's an extra chain-walk per error. Consider gating behind a build-tag debug-mode check if benches ever flag it.
- **`low` — No `sync.Pool` for `[]any` attrs or marshal buffer.** Two candidates: (a) the `attrs` slice in `classify.go:32` (fixed cap 6 or 8), (b) a `bytes.Buffer` for `json.Marshal` via `Encoder.Encode`. Both would shave allocs at sustained 10k+ QPS. Premature without benches.
- **`nitpick` — Redundant `string(...)` conversions in `classify.go:33-34`.** `Code` and `Reason` are already `type X string`. The conversions are no-ops at runtime; `any`-boxing happens either way.
- **`nitpick` — `loggerFrom(ctx)` cost** (`logctx.go:23-28`). Single `ctx.Value` lookup, walks ctx ancestry linearly. ctx depth in this codebase is ~4-6 frames. Not a problem.
- **`nitpick` — `PermanentError` allocation per poison message** (`permanent.go:16-21`). Workers only, low volume by definition.
- **`nitpick` — `json.Marshal(e)` per reply, no pre-marshal of common envelopes.** A `(Code, Reason, Message, len(Metadata)==0)` LRU could intern the ~10-20 most common envelopes (`BadRequest("internal error")`, `Unauthenticated("token expired")`, etc.). Probably not worth the complexity.
- **`nitpick` — `errors.As` chain depth concern.** Worst-case chain in this codebase: typed errcode + 1-2 `fmt.Errorf("...: %w", err)` wraps = depth 2-3. Not a concern.
- **`nitpick` — `MarshalQuiet` duplicates `errors.As` work** (`errnats/reply.go:30-31`). Cosmetic.

### Recommendations

1. **`medium`** — Add `classify_bench_test.go`, `errnats/reply_bench_test.go`, `errhttp/write_bench_test.go` with `Benchmark_BadRequest`, `Benchmark_InternalWithCause`, `Benchmark_PermanentWrap`. Wire into a `make bench` target as informational (not gating) CI so regressions show in PR diffs.
2. **`medium`** — Add a brief "perf invariants" section to `doc.go` codifying the alloc budget, the no-`fmt.Sprintf` rule, and the lock-free guarantee.
3. **`low`** — Migrate `classify.go:32-39` from `[]any` variadic to typed `slog.Attr` (`slog.String`, `slog.Group`) and call `LogAttrs`. Cleaner, faster slog dispatch, better escape analysis.
4. **`low`** — Pre-size the `attrs` slice with `make([]any, 0, 8)` instead of literal — saves a slot for `"underlying"` without a reslice.
5. **`low`** — Replace `json.Marshal(e)` in `errnats/reply.go:19` and `errhttp/write.go:15` with an `Encoder` over a pooled `bytes.Buffer`. Defer until benches justify it.
6. **`nitpick`** — Drop redundant `string(...)` conversions in `classify.go:33-34`.

**Bottom line:** nothing here is on fire. The package is allocation-aware in spirit but unmeasured in fact. Add benches, take the easy `LogAttrs` win, and the package is solid at 10k QPS per service.

---

## Chapter 8 — Prioritized action list

Top items across all dimensions, ordered by severity then impact ÷ effort.

| # | Severity | Action | Dimension | Where | Why |
|---|---|---|---|---|---|
| 1 | **high** | Wire a TS↔Go reason-catalog parity test that diffs `chat-frontend/src/api/_transport/asyncJob.ts` `REASON_COPY` against `allReasons`, and add `non_channel_operation` to `REASON_COPY` now. | Consumer ergonomics | `pkg/errcode/codes_test.go`, `chat-frontend/src/api/_transport/asyncJob.ts:94` | Two emitted backend reasons currently fall through to raw English on the client. CI gives no signal today. |
| 2 | **high** | Rename the package func `errcode.WithLogValues` to break the visual collision with `Option`-returning `With*` constructors (suggested `WithLogContext`). Update doc + call sites. | API design | `pkg/errcode/logctx.go:18`; callers in `room-service`, `auth-service`, `message-gatekeeper`, `room-worker` | Looks like an `Option` but is a ctx mutator. Documented footgun in `doc.go:56`; rename eliminates the trap. |
| 3 | **high** | Fix the "one of 7 generic categories" → "8" in client docs and confirm the catalog table is complete. | Consumer ergonomics | `docs/client-api.md:2186` | First paragraph a client integrator reads, currently stale. |
| 4 | **medium** | Add benchmarks: `Benchmark_BadRequest`, `Benchmark_InternalWithCause`, `Benchmark_PermanentWrap` in `classify_bench_test.go`, `errnats/reply_bench_test.go`, `errhttp/write_bench_test.go`. Wire a `make bench` informational job. | Performance | new files under `pkg/errcode/` | Library on every reply path with zero perf regression signal. |
| 5 | **medium** | Auto-derive `allReasons` (go:generate or reflection-based init test) so the dual-list maintenance disappears. | Maintainability | `pkg/errcode/codes_test.go:8-18` | Won't scale past ~10 services without becoming a merge-conflict hotspot. |
| 6 | **medium** | Migrate `room-service` to `pkg/natsrouter` so `wrappedCtx` + double `errnats.Reply` collapse to the one-liner shape `history-service` uses. | Consumer ergonomics | `room-service/handler.go:126-1533` | Deletes ~75 lines of pure plumbing and removes one of two log-attach styles new contributors must learn. |
| 7 | **medium** | Move `MarshalQuiet` / `ReplyQuiet` behind an internal package OR rename to `…AfterLog` so the log-once invariant is encoded in the name. | API design | `pkg/errcode/errnats/reply.go:28-52` | One mis-use away from breaking the log-once contract repo-wide. |
| 8 | **medium** | Add a "Catalogs" section to `doc.go` + the four-step add-a-reason checklist; link to `docs/error-handling.md` and `docs/client-api.md §6` from doc.go so `go doc` surfaces them. | Maintainability / Consumer ergonomics | `pkg/errcode/doc.go` | Contributors landing via `go doc` currently can't find the longer guides. |
| 9 | **medium** | Add a "perf invariants" section to `doc.go` codifying ≤4 allocs / no `fmt.Sprintf` / lock-free. | Performance | `pkg/errcode/doc.go` | Codifies what's implicit so future PRs don't accidentally regress it. |
| 10 | **low** | Migrate `classify.go:32-39` to `slog.LogAttrs` with typed `slog.Attr` — cleaner, faster slog dispatch, better escape analysis. | Code quality / Performance | `pkg/errcode/classify.go:32-39` | Easy alloc win on every reply; pairs with item #4 for measurable evidence. |

### Out-of-scope items surfaced

- `make generate` is broken repo-wide (toolchain mismatch — `mockgen` built against go1.24, services on go1.25). Not a `pkg/errcode` finding, but blocks the mock-staleness check.
- `govulncheck` reports `GO-2026-5039` (`net/textproto`) and `GO-2026-5037` (`crypto/x509`). Cleared by bumping `GOTOOLCHAIN` to `go1.25.11`. No trace touches `pkg/errcode/`.
