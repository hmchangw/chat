# integration-suite

A scenario-driven black-box integration test suite for the chat
backend. See `docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`
for the full design.

This tool does NOT own infrastructure. It connects to whatever NATS /
MongoDB / Cassandra / service URLs are provided via env vars, and
acts as an authenticated NATS user — minting a NATS JWT via
auth-service and using it to talk to the same NATS the services use.

## Quick start

### 1. Bring up infra (kind) and services (docker-compose) yourself

The suite does not manage either. Bring them up the way you already do.

### 2. Export config

```bash
export SITES=tw
export PRIMARY_SITE=tw
export AUTH_SERVICE_URL_TW=http://localhost:8081
export ROOM_SERVICE_URL_TW=http://localhost:8082   # placeholder; v1 doesn't call HTTP on room-service
export NATS_URL_TW=nats://localhost:14222
```

For multi-site (Part 2), add `_US` variants and update `SITES`.

### 3. Run the suite

```bash
make integration-suite                  # everything
make integration-suite SCOPE=service    # one scope folder
make integration-suite TAGS=@smoke      # filter by tag
make integration-suite FEATURE=tools/integration-suite/features/service/room.feature
```

### 4. Read the report

```bash
cat docs/integration-suite/last-run.md
```

The report shows two scores side by side:

- **APPROVED** — `@status:approved` scenarios; this gates CI
- **DRAFT** — everything else; informational

### 5. Periodically audit (calibration)

```bash
make integration-suite-audit SAMPLE=30
# Review the generated markdown file, fill in Reviewer says / Class
make integration-suite-audit-tally IN=docs/integration-suite/audit-<runID>.md
```

The tally is appended to `docs/integration-suite/audit-log.md`.

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

## Authoring new scenarios

See `AUTHORING.md`. Both humans and AI agents follow the same rules.

## Available targets

| Target | Purpose |
|---|---|
| `make integration-suite [SCOPE=… TAGS=… FEATURE=…]` | Run scenarios |
| `make integration-suite-steps` | List registered step regexes |
| `make integration-suite-lint` | Verify blindspot register consistency |
| `make integration-suite-audit SAMPLE=<n>` | Generate audit checklist |
| `make integration-suite-audit-tally IN=<file>` | Tally a filled checklist |

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `config: AUTH_SERVICE_URL_TW is required` | Env var unset for a listed site |
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
