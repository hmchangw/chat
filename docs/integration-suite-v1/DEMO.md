# Demo Runbook — integration-suite architecture

A 15-minute presenter script. Pairs with `ARCHITECTURE.md` (open its
diagrams alongside). Everything here runs on a laptop with **no kind /
docker-compose** — only the final green run needs the live stack, which
is intentionally the last beat.

Run all commands from the repo root.

---

## Act 0 — The one-sentence framing (30s, no command)

> "This is a black-box conformance suite. It owns no infrastructure. It
> logs in as a real user, drives the already-running services over the
> transport they actually speak, and classifies the reply against the
> documented design. It is not load testing — load testing repeats one
> thing to measure speed; this walks distinct scenarios to find the
> zeros: places where behavior diverges from, or isn't covered by, the
> design."

Show `ARCHITECTURE.md` §1 (the context diagram). Point at the boundary:
suite on one side, live stack on the other, two arrows — HTTP to
auth-service only, NATS request/reply to everything else.

---

## Act 1 — The package boundary (2 min)

**Say:** "Two packages, and the split is enforced by the Go compiler,
not convention."

**Run:**
```bash
ls tools/integration-suite
ls tools/integration-suite/internal/harness
```

**Point out:**
- Top level = `package integrationsuite`: `main_test.go` + `*_steps_test.go`. The Gherkin vocabulary. This is what authors (and AI) touch.
- `internal/harness` = `package harness`: config, World, classifier, reporter, coverage, audit. The framework. `internal/` makes it un-importable from outside the tool — a hard "don't edit this" signal.

**Why it matters:** authors extend the vocabulary; they cannot
accidentally reshape the scoring engine. The blast radius of a new
scenario is one feature file.

---

## Act 2 — The harness is correct on its own (2 min)

**Say:** "Before we ever touch the live system, the scoring engine
itself is unit-tested — classifier, two-score math, blindspot logic,
coverage scorer."

**Run:**
```bash
go test ./tools/integration-suite/internal/harness/... -count=1
```

**Point out:** one `ok` line, sub-second, zero infrastructure. The
thing that decides pass/fail is itself under test. This is the "is the
verdict trustworthy" question answered at the unit level (the audit
loop answers it empirically later).

---

## Act 3 — The vocabulary, and transport-by-architecture (3 min)

**Say:** "Authors never choose HTTP vs NATS. They pick a step verb; the
verb picks the transport via which subject builder it calls."

**Run:**
```bash
make integration-suite-steps 2>/dev/null | grep -E '^\s*(Given|When|Then)' | sort -u | head -20
```

**Point out a few lines**, e.g. `Given user "alice" is authenticated`
(this one bootstraps over HTTP `POST /auth`, mints a NATS JWT) vs
`When "alice" submits a message ...` (NATS request/reply). Same author
experience; different wire underneath.

Open `ARCHITECTURE.md` §4. Walk the dispatch: `LastResponse.Transport`
→ `ClassifyNATS` / `ClassifyHTTP` → one of `None / Validation / Auth /
HandlerError / Timeout / Persistence / RouteNotFound / Downstream`.

**The honest beat:** "Services sanitize error text. A design-level
*forbidden* often arrives as a generic `HandlerError`. We do not fudge
that green — we record it as a blindspot." Segue to Act 4.

---

## Act 4 — Governance: the three orthogonal axes (4 min)

Open `ARCHITECTURE.md` §5 (the table). Say the three questions out loud:

1. **Soundness** — did behavior match design? → pass/fail, two-score.
2. **Completeness** — is every documented case tested? → coverage register.
3. **Trust** — is the verdict itself right? → audit confusion matrix.

### 4a. Soundness — two-score, blindspots

**Run:**
```bash
make integration-suite-lint
```
**Say:** "Every `@blindspot:<slug>` in a feature must have a matching
entry in `blindspots.md`. Lint enforces the link. A blindspot *fails*
the scenario — undocumented behavior can never silently pass."

### 4b. Completeness — coverage register (report-only)

**Run:**
```bash
make integration-suite-coverage 2>/dev/null | tail -15
```
**Point out:** the catalog of documented behavior cases; each is
`covered` only when a `@covers:` scenario actually passed,
`known-gap` if it's a registered blindspot, else `uncovered`. Stress:
**report-only — low coverage never breaks CI.** It's a map of what the
design promises vs what we've proven, not a gate.

### 4c. Trust — calibration loop (describe, don't run)

Point at `ARCHITECTURE.md` §5: `cmd/audit` samples N scenarios → a
human labels each TP/TN/FP/FN → `cmd/audit-tally` appends an
accuracy / false-positive / false-negative line to `audit-log.md`.
"This is how we measure whether the classifier's verdicts are
themselves correct — the suite auditing the suite."

---

## Act 5 — One scenario, end to end (2 min)

Open `ARCHITECTURE.md` §3 (lifecycle) and a real feature file:

```bash
cat tools/integration-suite/features/service/room.feature
```

Trace it against §3 out loud: Before hook installs the
`it-<runID>-<scenarioID>-` ID prefix (collision-free, no shared
fixtures) → `Given` mints nkey + JWT → `When` builds a `pkg/model`
request on a `pkg/subject` subject and does the pooled NATS round-trip
→ `Then` compares `LastResponse.Class()` → After hook enforces any
`@blindspot`.

**Say:** "Every scenario is independent: fresh creds, fresh ID prefix,
fresh connection pool. Order-independent, parallel-safe, repeatable
against a stack that already has noise in it."

---

## Act 6 — The live run (closing, needs the stack)

If the kind infra + docker-compose services are up:

```bash
export SITES=tw PRIMARY_SITE=tw
export AUTH_SERVICE_URL_TW=<auth host:port>
export NATS_URL_TW=<nats host:port>
make integration-suite SCOPE=service
cat docs/integration-suite-v1/last-run.md
```

Show the **APPROVED vs DRAFT** split, the classified failures with
trace IDs (paste one into Jaeger), and the COVERAGE block.

If the stack is **not** up, run the same with `example.invalid` URLs
and show that `last-run.md` is still produced — scenarios fail, but the
**reporting/scoring pipeline is exercised end to end** without infra.
Then state the Part-1 boundary (`ARCHITECTURE.md` §6): JetStream and
DB-state assertions are Part-2; the contracts that need them are
already registered as blindspots that auto-green when the primitives
land.

---

## Anticipated questions

- **"Why not just hit an HTTP API?"** Only auth-service has one. Every
  other service is NATS request/reply. The suite must be a NATS client
  — that's what a real user (the frontend) is.
- **"Isn't this just E2E tests?"** It is E2E, plus a governance model:
  draft/approved scoring, blindspots-as-failures, a coverage register,
  and a calibration loop that measures the suite's own accuracy.
- **"Who writes scenarios?"** Humans or AI, same rules
  (`AUTHORING.md`): expected behavior must cite a real design doc,
  never invented; new scenarios are draft until a human-reviewed PR
  promotes them.
- **"Does low coverage block us?"** No — coverage and audit are
  report-only. Only the APPROVED two-score gates CI.
- **"What stops an AI writing a test that passes on wrong behavior?"**
  Draft-by-default + the audit confusion matrix surfacing false
  positives + blindspots failing rather than silently passing.

---

## Cheat sheet (commands in order)

```bash
ls tools/integration-suite && ls tools/integration-suite/internal/harness
go test ./tools/integration-suite/internal/harness/... -count=1
make integration-suite-steps 2>/dev/null | grep -E '^\s*(Given|When|Then)' | sort -u | head -20
make integration-suite-lint
make integration-suite-coverage 2>/dev/null | tail -15
cat tools/integration-suite/features/service/room.feature
# live (if stack up): make integration-suite SCOPE=service && cat docs/integration-suite-v1/last-run.md
```
