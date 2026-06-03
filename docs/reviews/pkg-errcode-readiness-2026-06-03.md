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

