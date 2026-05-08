# End-to-End Test Suite

**Date:** 2026-05-08
**Status:** Draft
**Owner:** platform / tooling

## Problem

The repo has unit tests (mocked stores) and per-service integration tests (single dependency via testcontainers), but nothing exercises the full request path across services, and **nothing exercises cross-site federation at all**. Bugs in the OUTBOX/INBOX wiring, the `outbox.{src}.to.{dst}.>` subject transforms, the `siteID`-scoped stream naming, and the multi-service event chains (gatekeeper → message-worker → broadcast-worker → notification-worker) are only caught manually or in production.

We need a test suite that drives the system end-to-end at its real boundaries — NATS subjects and HTTP routes — across two independent sites with full federation wiring, using only the same protocols a real client would use.

## Goal

Add an `e2e/` Go test package that:

- Boots a complete two-site stack (12 dep containers + 22 service containers + federation bootstrap) via `testcontainers-go`'s compose module against a new `docker-local/e2e/compose.e2e.yaml`.
- Exercises the system through real NATS request/reply, JetStream publishes, and HTTP routes — no service code is mocked, replaced, or modified.
- Achieves test isolation through per-test `siteID` namespacing (matches the production sharding model — every stream, index, and Mongo doc is already `siteID`-scoped).
- Runs locally today via `make e2e`, with a workflow shape that drops into CI later without restructuring.
- Stays compatible with both legacy `docker-compose` v1 and modern `docker compose` v2 (existing repo composes prove this subset works in both).

## Non-goals

- **Frontend**: `chat-frontend` is not in scope. e2e drives the backend at the same NATS/HTTP boundary the frontend would use, but no browser, no JS toolchain, no UI assertions.
- **Modifying existing files**: no edits to `<service>/deploy/Dockerfile`, `<service>/deploy/docker-compose.yml`, `docker-local/compose.deps.yaml`, or `docker-local/compose.services.yaml`. The e2e overlay is fully additive.
- **Modifying service code**: tests exercise services as deployed; if a test fails, the fix is in the service or the test, not in plumbing.
- **CI workflow**: `.github/workflows/e2e.yml` is intentionally out of scope. The harness is built so a future workflow is `make e2e` and nothing more.
- **Per-test container isolation**: a 34-container stack cannot start per-test. Stack starts once per `go test` invocation; tests share it; isolation is via `siteID`.
- **Replacing existing integration tests**: per-service integration tests stay. e2e is additive.

## Architecture

### Test driver

A new `e2e/` Go package at the repo root, build-tagged `//go:build e2e`. Same toolchain as the rest of the repo. Reuses `pkg/model` (typed structs over the wire), `pkg/idgen` (generates per-test IDs), `pkg/subject` (subject builders), `pkg/natsutil` (request/reply helpers), and the same `nats.go` / Mongo driver / Resty / Cassandra clients the services use.

### Stack orchestration

`testcontainers-go`'s `compose` module drives a single `docker-local/e2e/compose.e2e.yaml`. The compose module embeds compose v2 as a Go library, so the stack runs identically regardless of whether the host has `docker-compose` v1, `docker compose` v2, both, or neither.

Stack lifecycle:
- `TestMain` calls `harness.StartStack(ctx)` which `compose.Up()`s the file with `WithRecreate(api.RecreateNever)` and waits for all healthchecks.
- Federation bootstrap runs as a Go function in the harness (not a one-shot container) immediately after `compose.Up()` returns: connects to both NATS clusters, calls `js.CreateOrUpdateStream` with cross-site `Sources` + `SubjectTransforms` for `INBOX_siteA ← OUTBOX_siteB` and vice versa.
- `TestMain` defers `stack.Stop(ctx)`. testcontainers' Reaper handles cleanup if the test process dies hard.

Stack reuse for fast iteration:
- `E2E_REUSE_STACK=1` env var makes `harness.StartStack` skip `compose.Up()` and connect to an already-running stack.
- Devs iterating on tests run `docker compose -f docker-local/e2e/compose.e2e.yaml up -d --wait` once, then `E2E_REUSE_STACK=1 go test ...` repeatedly. testcontainers-go's `WithReuse` gives this natively — no custom sentinel files.

### Test isolation

Every test mints a fresh `siteID` (UUIDv7 from `pkg/idgen`, prefixed with the test name for log readability). All resources the test creates — JetStream streams the harness bootstraps for that test, Mongo `siteID`-keyed docs, ES indices `*-{siteID}-*`, room IDs — are scoped to that ID. Since the production sharding model is already `siteID`-keyed, this isolation is exactly what prod data multi-tenancy looks like.

Multi-site tests use two pre-allocated `siteID`s per case (`siteA-<test>`, `siteB-<test>`) that bind to the two sites running in the stack. Federation tests assert the cross-site flow between them.

No global teardown between tests. Stack stays warm; `siteID` namespacing is the cleanup mechanism.

## Topology

Two parallel sites, every dependency duplicated per site (faithful to prod):

```
siteA:                              siteB:
  nats-a       (4222)                 nats-b       (4322)
  mongo-a      (27017)                mongo-b      (27018)
  cass-a       (9042)                 cass-b       (9142)
  es-a         (9200)                 es-b         (9300)
  valkey-a     (6379)                 valkey-b     (6479)
  keycloak-a   (8180)                 keycloak-b   (8181)
  auth-a, room-a, msg-gk-a,           auth-b, room-b, msg-gk-b,
  msg-worker-a, broadcast-a,          msg-worker-b, broadcast-b,
  notif-a, history-a,                 notif-b, history-b,
  search-a, search-sync-a,            search-b, search-sync-b,
  inbox-a, room-worker-a              inbox-b, room-worker-b
```

Resource budget (uncapped, default heap sizes everywhere): ~8–12 GB RAM, ~6–8 cores under load. Designed for an 8-core / 32GB local dev box. Future CI will need either a larger runner or a cap-applied profile (env-driven heap overrides leave the door open without restructuring).

### Federation wiring

Owned by the harness, not by any service's `bootstrap.go` (per CLAUDE.md: federation config is ops/IaC, never service-owned). The harness is the ops layer for the test environment.

After both NATS clusters are healthy, the harness creates:

```go
// INBOX_siteA sources from OUTBOX_siteB
js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
    Name:     "INBOX_siteA",
    Subjects: []string{"inbox.siteB.>"},
    Sources: []*jetstream.StreamSource{{
        Name:          "OUTBOX_siteB",
        FilterSubject: "outbox.siteB.to.siteA.>",
        SubjectTransforms: []jetstream.SubjectTransformConfig{{
            Source:      "outbox.siteB.to.siteA.>",
            Destination: "inbox.siteB.>",
        }},
        External: &jetstream.ExternalStream{APIPrefix: "$JS.siteB.API"},
    }},
})
// ... and the symmetric siteB ← siteA stream
```

Cross-NATS access uses leaf nodes or account exports configured in the per-site `nats-server.conf` mounted into each NATS container. Specifics are pinned during implementation; the architectural commitment is that federation wiring is harness-owned and lives next to the e2e compose file.

## Compose file

`docker-local/e2e/compose.e2e.yaml`. Style matches existing repo composes:
- No `version:` header (modern compose-spec, accepted by both v1.29+ and v2)
- `depends_on:` with `condition: service_healthy` for ordering (proven to work in this repo by `docker-local/compose.deps.yaml`)
- `name: chat-e2e` top-level project name
- Service blocks `build:` from existing `<service>/deploy/Dockerfile` with repo root as context — no Dockerfile changes anywhere

Image refs use whole-name parameterization with sensible defaults so a fresh `git clone && make e2e` works with zero setup:

```yaml
nats-a:
  image: ${E2E_NATS_IMAGE:-nats:2.11-alpine}
  command: ["--jetstream", "--http_port", "8222", "--server_name", "nats-a"]
  healthcheck: ...
mongo-a:
  image: ${E2E_MONGO_IMAGE:-mongo:8}
  ...
```

### Bootstrap ergonomics

A fresh clone runs `make e2e` with **zero manual setup**. The mechanism:

- `docker-local/e2e/.env.example` is checked in. It enumerates every `E2E_*_IMAGE` var with the upstream-default value, so the file is both documentation and a working config.
- `docker-local/e2e/.env` is gitignored.
- The `e2e-up` Makefile target copies `.env.example` → `.env` if (and only if) `.env` is missing. Idempotent — re-running never overwrites a customized file.
- Compose v2 and v1 both auto-load `.env` from the compose file's directory, so no `--env-file` flag is needed.

**Internal port flow (one time, per dev box):** edit the auto-created `docker-local/e2e/.env` — change the image refs to point at the internal registry mirror — and `make e2e` keeps using those overrides forever after. No script to run, no manual file creation, no documentation step beyond "edit .env if your registry is different".

```
# docker-local/e2e/.env (auto-created from .env.example, then edited internally)
E2E_NATS_IMAGE=internal-registry.acme.com/mirror/nats:2.11-alpine
E2E_MONGO_IMAGE=internal-registry.acme.com/mirror/mongo:8.0.4
```

For a fully scripted setup (e.g. an internal onboarding tool), `scripts/e2e-bootstrap.sh` is provided as a thin wrapper that copies `.env.example` and applies an internal-registry prefix via `sed`. Optional — most devs just edit the file once.

Service env variables (`NATS_URL`, `SITE_ID`, `MONGO_URL`, `BOOTSTRAP_STREAMS`, etc.) are explicit in each compose block — site A services point at `nats-a`, site B services at `nats-b`. `BOOTSTRAP_STREAMS=true` per-service so each service stands up its own streams against the fresh NATS.

The compose file is long (22 service blocks + 12 dep blocks). It's mechanical and readable. It is the single source of truth for the e2e topology. Adding a new service to e2e coverage = adding two service blocks (one per site).

## File layout

```
e2e/
  doc.go                     # //go:build e2e build tag
  main_test.go               # TestMain: stack lifecycle
  harness/
    stack.go                 # Compose up/down, health waits
    federation.go            # Cross-site stream wiring
    images.go                # E2E_*_IMAGE env defaults
    auth.go                  # Wraps pkg/testutil/keycloak.Client with per-site
                             #   Keycloak base URLs and the e2e realm; mints
                             #   JWTs for test users on demand.
    clients.go               # Per-site NATS/Mongo/Cassandra/HTTP client factories
    ids.go                   # Per-test siteID/roomID/userID minting
    logs.go                  # Per-test service log capture
  scenarios/
    auth_test.go             # Login → JWT → authenticated NATS connection
    rooms_test.go            # Channel/DM creation, invite, member list
    message_send_test.go     # End-to-end happy path
    history_test.go          # LoadHistory readback
    search_test.go           # Search index visibility
    federation_basic_test.go # Cross-site message delivery
    federation_invite_test.go# Cross-site room invite
    federation_catchup_test.go # OUTBOX accumulation while site down
    errors_test.go           # Permission denial, malformed payload, bad JWT
    request_id_test.go       # Request-ID propagation across the chain
  testdata/
    keycloak-realm-e2e.json  # Pre-provisioned test users
docker-local/e2e/
  compose.e2e.yaml           # Full two-site topology
  README.md                  # How to run, image overrides, debugging
  nats-a/nats-server.conf
  nats-b/nats-server.conf
  cassandra/init.cql         # Symlink or copy of existing schema init
```

## Test scenarios

Roughly 25–30 tests across 9 files. Each test asserts observable behavior at the same boundary a real client would use — never reads from a service's internal state directly.

**Auth + room lifecycle (single-site)**
- Login via Keycloak password grant → JWT → NATS callout accepts the connection
- Create channel room → owner subscribed automatically; `room-worker` writes the subscription
- Create DM room → `idgen.BuildDMRoomID(a, b)` matches both lookups; second create with reversed args is idempotent
- Invite user → `room.invited` event → user's subscriptions list now includes the room
- Permission denial: non-member sends to a private room → `message-gatekeeper` rejects with `model.ErrorResponse`

**Message flow (single-site)**
- Send message → MESSAGES → gatekeeper validates → MESSAGES_CANONICAL → `message-worker` writes Cassandra → `broadcast-worker` delivers to subscribers' NATS subjects → `notification-worker` publishes notification
- `history-service.LoadHistory` returns the message
- `search-service` query returns it after `search-sync-worker` indexes it (with a polling wait, not a fixed sleep)
- Idempotency: duplicate client-supplied message ID is rejected

**Federation (multi-site)**
- siteA user sends in a room with siteB members → siteB members receive on their site's broadcast subject
- Cross-site invite: siteB user invited to a siteA room → subscription created on siteB → siteB user can read room history (which lives on siteA) via outbox-mediated history fetch
- Outbox catch-up: stop siteB inbox-worker, send 50 messages on siteA, restart siteB → siteB drains and delivers all 50 in publish order
- Negative: siteA-only message must NOT appear on siteB when no siteB members are subscribed

**Error paths**
- Send to non-existent room → `model.ErrorResponse` with sanitized message
- Malformed JSON payload → reply error, no service crash
- Unauthorized JWT → connection rejected at NATS callout

**Observability sanity**
- Every test asserts the request ID set on the inbound call appears in the broadcast-worker delivery payload — confirms propagation across the full chain

## How it runs

### Local (default)

```
$ make e2e
go test -tags e2e -race -count=1 ./e2e/...
```

`TestMain` boots the stack via testcontainers compose module, waits for all healthchecks, runs federation bootstrap, runs all tests, tears down. First run ~5–6 min (image builds dominate). Subsequent runs ~2 min (cached images, ~90s startup + ~30s tests).

### Local iteration loop (fast)

```
$ docker compose -f docker-local/e2e/compose.e2e.yaml up -d --wait
$ make e2e-only
E2E_REUSE_STACK=1 go test -tags e2e -race -count=1 ./e2e/...
PASS  ~30s
```

Run repeatedly while editing tests. Stack stays warm. Tear down with `docker compose -f docker-local/e2e/compose.e2e.yaml down -v` when finished.

### Standalone debug

```
$ docker compose -f docker-local/e2e/compose.e2e.yaml up
# tail logs, exec into containers, hit endpoints, repro a colleague's bug,
# entirely outside `go test`
```

This is impossible if the topology lives in Go (the rejected Option A). A YAML-described stack runs anywhere docker runs.

### Failure debugging

On test failure the harness prints, for each service touched by the failing test, the path to a captured log file at `e2e/logs/<test-name>/<service>.log`. Logs include the request ID as a structured field so following one e2e flow across 22 services is `grep <reqID>`.

### Future CI

A `.github/workflows/e2e.yml` (out of scope for this spec; designed-in) is roughly:

```yaml
- uses: actions/checkout@v4
- uses: actions/setup-go@v5
  with: { go-version: "1.25" }
- run: make e2e
```

testcontainers-go embeds compose v2; no separate CLI install. The `make e2e` target is the single contract — no CI-specific code paths in the harness.

## Makefile targets

```makefile
E2E_DIR := docker-local/e2e
E2E_COMPOSE := $(E2E_DIR)/compose.e2e.yaml
E2E_ENV := $(E2E_DIR)/.env

# Auto-create .env from .env.example on first run; idempotent.
$(E2E_ENV):
	@cp $(E2E_DIR)/.env.example $@
	@echo "Created $@ from .env.example. Edit it to override image refs (e.g. internal registry)."

e2e: $(E2E_ENV)
	go test -tags e2e -race -count=1 ./e2e/...

e2e-only:
	E2E_REUSE_STACK=1 go test -tags e2e -race -count=1 ./e2e/...

e2e-up: $(E2E_ENV)
	docker compose -f $(E2E_COMPOSE) up -d --wait

e2e-down:
	docker compose -f $(E2E_COMPOSE) down -v

e2e-logs:
	docker compose -f $(E2E_COMPOSE) logs -f $(SERVICE)
```

`make e2e` is the single user-facing command. The `$(E2E_ENV)` prerequisite is what makes "fresh clone, zero setup" work — `.env` is auto-created from the checked-in template before anything else runs.

`make e2e` is the user-facing target. `e2e-up` / `e2e-down` / `e2e-only` exist for the iteration loop. `e2e-logs SERVICE=msg-gk-a` for debugging.

## Dependency additions

Adding to `go.mod`:
- `github.com/testcontainers/testcontainers-go/modules/compose` — already on `testcontainers-go` indirectly via `pkg/testutil`; this adds the compose subpackage explicitly.

No other new dependencies. Keycloak JWT acquisition uses Resty (already a project dep). NATS/Mongo/Cassandra/Resty all reuse existing pinned versions.

## Pre-work: extracting the Keycloak helper

A small precursor change lands in the same PR as the spec, ahead of the harness scaffolding:

- New package `pkg/testutil/keycloak/` with a `Client` exposing `PasswordGrant`, `AccessToken`, and `Refresh` over Keycloak's `/realms/{realm}/protocol/openid-connect/token` endpoint.
- Sentinel errors (`ErrInvalidCredentials`, `ErrUnknownClient`, `ErrRealmNotFound`) plus a structured `*Error` for everything else, with `errors.Is` integration so callers can branch on category without parsing strings.
- Built on `pkg/restyutil` so it inherits OTel + slog instrumentation + the project's standard timeout.
- Unit tests via `httptest` cover request shape, success parsing, every sentinel category, generic errors, refresh, context cancellation, base-URL normalization, and the option override (`WithHTTPClient`). 97% statement coverage.

This package is consumed by `e2e/harness/auth.go`, but is independent enough that any future per-service integration test that wants Keycloak-backed auth uses the same client. Keeps the e2e harness thin and avoids a one-off JWT-acquisition implementation buried inside `e2e/`.

## Open questions

- **Cassandra schema bootstrap in e2e**: each `cass-a` / `cass-b` container needs the schema from `docker-local/cassandra/init/*.cql` applied at startup. Resolution: mount that directory into both Cassandra containers via the compose file; the existing init container pattern in `docker-local/compose.deps.yaml` should port over directly.
- **Keycloak realm sharing**: each Keycloak gets its own realm import. Pre-provisioned `e2e-bot-a` / `e2e-bot-b` users in `e2e/testdata/keycloak-realm-e2e.json`. Cross-site auth tests verify a user authenticated against keycloak-a can be referenced (by subject) on siteB room operations — same model as prod.
- **Image build performance on first run**: 11 distinct service Dockerfiles × 2 sites = 22 image builds. Same binary, same image — testcontainers-go's compose module caches by Dockerfile path so site B reuses site A's image build. Net: 11 builds on first run, ~3–4 min. Acceptable for an e2e suite.

## Rollout

Three commits, each independently mergeable:

1. **Compose + harness skeleton**: `docker-local/e2e/compose.e2e.yaml`, `e2e/harness/*.go`, `e2e/main_test.go` with one trivial smoke test that boots the stack and asserts every NATS, Mongo, ES, and Cassandra is reachable. Verifies the topology stands up before any scenario logic exists.

2. **Single-site scenarios**: auth, rooms, message_send, history, search, errors, request_id. Federation tests skipped via build constraint.

3. **Federation scenarios**: federation_basic, federation_invite, federation_catchup. Federation bootstrap function in `harness/federation.go` lands in this commit alongside the tests that exercise it.

Each commit follows TDD: tests written first against the topology, fail, then minimal implementation to pass.
