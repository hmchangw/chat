# integration-suite

A scenario-driven black-box integration test suite for the chat
backend. See `docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`
for the full design.

This tool does NOT own infrastructure. It connects to whatever NATS /
MongoDB / Cassandra / service URLs are provided via env vars, and
acts as an authenticated NATS user — minting a NATS JWT via
auth-service and using it to talk to the same NATS the services use.

## Quick start

### 1. Bring up infra and services yourself

The suite does not manage either. Bring them up the way you already do.

For the **docker-local single-site stack** (`make deps-up && make up`),
the convenience target below auto-fills the env vars. Skip to step 3.

### 2. Export config (custom stacks only)

```bash
export SITES=tw
export PRIMARY_SITE=tw
export AUTH_SERVICE_URL_TW=http://localhost:8081
export ROOM_SERVICE_URL_TW=http://localhost:8082   # placeholder; v1 doesn't call HTTP on room-service
export NATS_URL_TW=nats://localhost:14222
```

For multi-site (Part 2), add `_US` variants and update `SITES`.

### 3. Run the suite

The suite has its own self-contained `Makefile`. Targets are invoked
via `make -C tools/integration-suite <target>` from the repo root, or
`cd tools/integration-suite && make <target>` from this directory.

```bash
# After `make deps-up && make up` — single-site convenience, zero env:
make -C tools/integration-suite local            # auto-fills site-local URLs
make -C tools/integration-suite local SCOPE=service
make -C tools/integration-suite local TAGS=@smoke
make -C tools/integration-suite local FEATURE=tools/integration-suite/features/service/room.feature

# Custom topology (you provide all env vars):
SITES=tw PRIMARY_SITE=tw AUTH_SERVICE_URL_TW=… NATS_URL_TW=… \
  make -C tools/integration-suite run
```

`make -C tools/integration-suite help` lists every target. `local` and
`run` accept `SCOPE` / `TAGS` / `FEATURE` knobs.

**Pre-flight** (`local` only): the suite verifies `chat-local-nats` is
running and `auth-service /healthz` responds before delegating to the
godog run. It also prints which services the chosen scope assumes are
running (informational; missing NATS-only services surface at runtime
as `RouteNotFound`).

**Auth-service dev mode.** The suite's auth step posts `{account,
natsPublicKey}`; auth-service accepts that only when `DEV_MODE=true`
(otherwise it requires a Keycloak SSO token). Its
`deploy/docker-compose.yml` defaults `DEV_MODE=true`, so `make up`
out of the box is good. If you need to flip it on explicitly:

```bash
grep -q '^DEV_MODE=' docker-local/.env || echo 'DEV_MODE=true' >> docker-local/.env
docker compose -f docker-local/compose.services.yaml up -d --no-deps --force-recreate auth-service
```

### 4. Read the report

```bash
cat docs/integration-suite-v1/last-run.md
```

The report shows two scores side by side:

- **APPROVED** — `@status:approved` scenarios; this gates CI
- **DRAFT** — everything else; informational

### 5. Periodically audit (calibration)

```bash
make -C tools/integration-suite audit SAMPLE=30
# Review the generated markdown file, fill in Reviewer says / Class
make -C tools/integration-suite audit-tally IN=docs/integration-suite-v1/audit-<runID>.md
```

The tally is appended to `docs/integration-suite-v1/audit-log.md`.

### 6. Coverage (completeness)

`docs/integration-suite-v1/coverage.md` is the catalog of **documented
behavior cases** (each `## <slug>` cites a design source). A scenario
claims a case with a `@covers:<slug>` tag. Every run appends a
`COVERAGE` block to `last-run.md`, and:

```bash
make -C tools/integration-suite coverage   # score without re-running tests
```

prints per-service **covered / known-gap / uncovered**. A case is
*covered* only when a `@covers` scenario actually passed; `Status:
blindspot` entries count as *known-gap*; everything else is *uncovered*.
Report-only — low coverage never fails a run. This is the completeness
signal; the pass/fail two-score is soundness; the audit matrix is
classifier trust. To add a case: append an entry to `coverage.md` with
a real `Source:`, then `@covers:` it from a scenario (or register a
`@blindspot` and set `Status: blindspot`).

## How the platform talks to services

The suite is a **platform** that supports the transports services
actually speak:

- **auth-service** speaks HTTP. The suite POSTs `/auth` with the
  account name + a freshly-generated NATS user nkey public key, and
  stashes the returned NATS JWT + nkey seed in the World.
- **All other services** (room-service, room-worker, history-service,
  search-service, all workers) speak NATS request/reply. The suite
  opens a NATS connection per authenticated user (using their JWT +
  seed) and sends requests on subjects like
  `chat.user.<account>.request.rooms.create`.

Scenario authors do not pick a transport — they pick the right step
verb, and the step uses whichever primitive matches the architecture.

## Layout

The top-level directory is the **author-facing surface**; framework
internals are tucked into `internal/harness/`:

```
tools/integration-suite/
  features/            Gherkin scenarios — what you write
  *_steps_test.go      step definitions (Given/When/Then glue)
  main_test.go         godog entrypoint (TestFeatures)
  README.md AUTHORING.md
  cmd/                 standalone CLIs (steps, lint, audit)
  internal/harness/    framework: World, classifier, NATS/HTTP
                       transport, tracing, reporter, config,
                       audit, lint  — you do not edit this
```

Step files dot-import `internal/harness`, so scenarios and the
`AUTHORING.md` skeleton stay unqualified (`suiteWorld`, `natsRequest`,
…). Go requires `*_steps_test.go` and the runner to stay top-level
(test files must sit with the package they test).

> Note: this `internal/harness` split intentionally diverges from the
> flat layout described in
> `docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`
> — the spec predates the split. Behavior, targets, and the authoring
> contract are unchanged.

For how the pieces fit together (context, package boundary, scenario
lifecycle, transport/classifier dispatch, governance, Part-1 boundary)
see `ARCHITECTURE.md`.

## Authoring new scenarios

See `AUTHORING.md`. Both humans and AI agents follow the same rules.

## Available targets

All targets are in `tools/integration-suite/Makefile`. Invoke via
`make -C tools/integration-suite <target>` (or `cd` in and drop the `-C`).

| Target | Purpose |
|---|---|
| `local [SCOPE=… TAGS=… FEATURE=…]` | Run against docker-local single-site stack (auto-fills env, pre-flights NATS+auth) |
| `run [SCOPE=… TAGS=… FEATURE=…]` | Run scenarios; caller provides env vars |
| `steps` | List registered step regexes |
| `lint` | Verify blindspot register consistency |
| `coverage` | Score documented-case coverage (report-only) |
| `audit SAMPLE=<n>` | Generate audit checklist |
| `audit-tally IN=<file>` | Tally a filled checklist |
| `help` | Print this list inline |

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `config: AUTH_SERVICE_URL_TW is required` | Env var unset for a listed site |
| `config: ..._SITE-LOCAL is required` even though `local` set it | `make` was invoked recursively between `env` and `go test`, or the recipe shell isn't bash. Both layers drop hyphenated env vars from the environment they pass to children. The suite's Makefile fixes this by `SHELL := /bin/bash` and calling `go test` directly from `local` — DO NOT reintroduce a recursive `$(MAKE)` between `env` and `go test`. |
| `POST /auth ... 400 {"error":"ssoToken and natsPublicKey are required"}` | `auth-service` is in OIDC mode — set `DEV_MODE=true` in `docker-local/.env` and recreate the container. |
| `auth: POST /auth: status 404` | `auth-service` not reachable, or not running in dev mode |
| `nats: connect <url>: ...` | NATS unreachable, or JWT was rejected (signer key mismatch with the cluster's account config) |
| `0 scenarios` | Feature files not in `tools/integration-suite/features/<scope>/` |
| Tests pass but `last-run.md` is empty | `reports/cucumber.json` failed to write — check `reports/` is writable |

## Status & maturity

This is Part 1 of the suite. Part 2 will add:

- Direct Mongo / Cassandra step primitives for state-observation
- JetStream consumer/peek primitives for stream-observation
- `pipeline/`, `federation/`, `resilience/`, `regional-resilience/` scopes
- Chaos integration (chaos-mesh, docker-compose pause/kill)
- `integration-suite-purge` data cleanup
