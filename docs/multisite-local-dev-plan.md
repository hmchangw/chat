# Multi-Site Local Dev — Implementation Plan

## Goal

Stand up two independent chat sites (`site-a`, `site-b`) on a single developer
machine to exercise the cross-site Outbox/Inbox federation pattern end-to-end.
Each site has its own NATS, its own Mongo DB, its own Cassandra keyspace, its
own ES indexes, and its own copies of every microservice. Shared infrastructure
(Mongo, Cassandra, ES, Valkey, Keycloak) runs as single containers with
per-site logical isolation.

The existing single-site `site-local` default is replaced. After this change,
`make deps-up && make up` brings up the full two-site federated stack as the
default local-dev experience.

## Scope

**In scope**

- Two NATS + JetStream instances (one per site), each with its own operator,
  account, and creds.
- One Mongo container hosting `chat_site_a` and `chat_site_b` databases.
- One Cassandra container hosting `chat_site_a` and `chat_site_b` keyspaces.
- One ES, one Valkey, one Keycloak (shared `chatapp` realm) — all
  site-discriminated via prefixed keys / indexes / DB names.
- Every existing backend service (12 of them) running twice — once per site.
- A `federation-init` one-shot container that wires `INBOX_site-a` ←
  `OUTBOX_site-b` and `INBOX_site-b` ← `OUTBOX_site-a` via JetStream External
  Sources, after both sites' `inbox-worker`s have created their INBOX streams.
- Updated `Makefile` and `docker-local/setup.sh`.
- Doc snippet in `docs/local-dev.md` showing how to run two `npm run dev`
  frontends pointed at each site.

**Out of scope (explicitly)**

- Keycloak realm federation — single shared realm only.
- TLS on NATS — plaintext on the `chat-local` docker network.
- Production / IaC topology — this is local-dev only. Production stream
  federation is still owned by ops.
- Cassandra DDL changes — bucket math and table schema are unchanged.
- Application code changes inside any service — every service already reads
  `SITE_ID` and connects to per-site infra via env vars. This work is
  entirely compose / config / Makefile / docs.

## Topology

```
┌─────────────────────────── chat-local network ───────────────────────────┐
│                                                                          │
│  Shared deps (single container each):                                    │
│    mongodb (27017)  cassandra (9042)  elasticsearch (9200)               │
│    valkey (6379)    keycloak (8180)                                      │
│                                                                          │
│  Per-site NATS:                                                          │
│    nats-a   host: 4222 / 8222 / 9222                                     │
│    nats-b   host: 4322 / 8322 / 9322                                     │
│                                                                          │
│  Site A services                       Site B services                   │
│    site-a-auth-service  :8080            site-b-auth-service  :8081      │
│    site-a-history-service               site-b-history-service           │
│    site-a-room-service                  site-b-room-service              │
│    site-a-search-service                site-b-search-service            │
│    site-a-message-gatekeeper            site-b-message-gatekeeper        │
│    site-a-message-worker                site-b-message-worker            │
│    site-a-broadcast-worker              site-b-broadcast-worker          │
│    site-a-notification-worker           site-b-notification-worker       │
│    site-a-room-worker                   site-b-room-worker               │
│    site-a-inbox-worker                  site-b-inbox-worker              │
│    site-a-search-sync-worker            site-b-search-sync-worker        │
│    site-a-mock-user-service             site-b-mock-user-service         │
│                                                                          │
│  One-shot:                                                               │
│    chat-local-federation-init (runs once after both sites are up,        │
│                                exits 0; wires INBOX sources)             │
└──────────────────────────────────────────────────────────────────────────┘
```

## Site IDs and naming

- `SITE_ID=site-a` and `SITE_ID=site-b`.
- Mongo DBs: `chat_site_a`, `chat_site_b`.
- Cassandra keyspaces: `chat_site_a`, `chat_site_b`.
- ES index prefixes: `messages-site-a-v1`, `messages-site-b-v1` (already
  driven by env var `MSG_INDEX_PREFIX` in `search-service` and
  `search-sync-worker`).
- Container names: `site-a-<service>` / `site-b-<service>` to allow both to
  coexist on a single Docker daemon.
- Compose project names: `site-a-services`, `site-b-services`.

## Host port allocations

Only services that today expose host ports need a second port for site-b.
Site-b gets `+1` on auth-service (kept low for human use) and a clean `+1`
on anything else exposed.

| Service | site-a host port | site-b host port |
|---|---|---|
| `auth-service` | 8080 | 8081 |
| `history-service` (Gin) | 8082 | 8083 |
| `room-service` (Gin, if exposed) | 8084 | 8085 |
| `search-service` (Gin) | 8086 | 8087 |
| `mock-user-service` (Gin) | 8088 | 8089 |

*During implementation, audit each service's existing `deploy/docker-compose.yml`
for any port currently published; if a service does not publish a host port
today, leave it unpublished for both sites.*

NATS:

| | site-a | site-b |
|---|---|---|
| Client | 4222 | 4322 |
| Monitor | 8222 | 8322 |
| WebSocket | 9222 | 9322 |

Shared deps keep their current host ports unchanged.

## File layout (target end state)

```
docker-local/
├── compose.deps.yaml              # CHANGED — nats → nats-a, add nats-b
├── compose.services.site-a.yaml   # NEW — `include`s every service compose
├── compose.services.site-b.yaml   # NEW — same
├── compose.federation.yaml        # NEW — one-shot federation-init container
├── site-a.env                     # NEW — env-file passed to compose for site-a
├── site-b.env                     # NEW — env-file passed to compose for site-b
├── setup.sh                       # CHANGED — generates two sets of creds + confs
├── site-a/                        # NEW — generated by setup.sh; .gitignored
│   ├── nats.conf
│   ├── backend.creds
│   └── account.seed
├── site-b/                        # NEW — generated by setup.sh; .gitignored
│   ├── nats.conf
│   ├── backend.creds
│   └── account.seed
├── compose.services.yaml          # REMOVED — replaced by per-site aggregators
├── nats.conf                      # REMOVED — replaced by site-a/ and site-b/
└── backend.creds                  # REMOVED — replaced by site-a/ and site-b/

<service>/deploy/docker-compose.yml  # CHANGED for every service — see "Per-service compose"

Makefile                            # CHANGED — see "Makefile targets"
docs/local-dev.md                   # CHANGED — two-frontend run instructions
.gitignore                          # CHANGED — ignore docker-local/site-a/, site-b/
```

## Per-service compose parameterization

Every backend service's `deploy/docker-compose.yml` is rewritten to read these
variables from the compose env-file, with site-a as the default for standalone
runs:

```yaml
name: ${SITE_ID:-site-a}-<service>

services:
  <service>:
    build:
      context: ../..
      dockerfile: <service>/deploy/Dockerfile
    container_name: ${SITE_ID:-site-a}-<service>
    environment:
      - SITE_ID=${SITE_ID:-site-a}
      - NATS_URL=nats://${NATS_HOST:-nats-a}:4222
      - NATS_CREDS_FILE=/etc/nats/backend.creds
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat_${SITE_ID:-site-a}
      - CASSANDRA_HOSTS=cassandra
      - CASSANDRA_KEYSPACE=chat_${SITE_ID:-site-a}
      - ELASTICSEARCH_URL=http://elasticsearch:9200
      - MSG_INDEX_PREFIX=messages-${SITE_ID:-site-a}-v1
      - VALKEY_ADDRS=valkey:6379
      - BOOTSTRAP_STREAMS=true
      # ...service-specific env stays as-is
    ports:
      # Only on services that currently publish a host port:
      - "${<SERVICE>_HOST_PORT:-8080}:8080"
    volumes:
      - ../../docker-local/${SITE_ID:-site-a}/backend.creds:/etc/nats/backend.creds:ro
    networks:
      - chat-local

networks:
  chat-local:
    external: true
```

Defaults are chosen so a developer running `docker compose -f
<service>/deploy/docker-compose.yml up` standalone still works against site-a
deps (current single-site behavior, just renamed).

`auth-service` additionally needs `AUTH_SIGNING_KEY` pulled from its site's
account seed; setup.sh writes it into the per-site env file.

## Aggregator composes

`docker-local/compose.services.site-a.yaml`:

```yaml
name: site-a-services

include:
  - ../auth-service/deploy/docker-compose.yml
  - ../broadcast-worker/deploy/docker-compose.yml
  - ../history-service/deploy/docker-compose.yml
  - ../inbox-worker/deploy/docker-compose.yml
  - ../message-gatekeeper/deploy/docker-compose.yml
  - ../message-worker/deploy/docker-compose.yml
  - ../mock-user-service/deploy/docker-compose.yml
  - ../notification-worker/deploy/docker-compose.yml
  - ../room-service/deploy/docker-compose.yml
  - ../room-worker/deploy/docker-compose.yml
  - ../search-service/deploy/docker-compose.yml
  - ../search-sync-worker/deploy/docker-compose.yml
```

`docker-local/compose.services.site-b.yaml` is identical. Per-site variation
comes entirely from the env-file (`--env-file docker-local/site-a.env` vs
`--env-file docker-local/site-b.env`) interpolated into the included files.

## Env files

`docker-local/site-a.env`:

```
SITE_ID=site-a
NATS_HOST=nats-a
AUTH_HOST_PORT=8080
HISTORY_HOST_PORT=8082
ROOM_HOST_PORT=8084
SEARCH_HOST_PORT=8086
MOCK_USER_HOST_PORT=8088
AUTH_SIGNING_KEY=<written by setup.sh from site-a/account.seed>
DEV_MODE=true
```

`docker-local/site-b.env` mirrors with `site-b` / `nats-b` / +1 ports / its
own signing key.

## NATS configs and federation

`setup.sh` is rewritten to run two passes of the existing nsc flow, writing
into `docker-local/site-a/` and `docker-local/site-b/`. Each per-site
`nats.conf` then resolver-preloads **both** operator/account JWTs so that
services authenticated to the other side's account can be granted source
permission. Concretely:

```
# docker-local/site-a/nats.conf
port: 4222
http_port: 8222
operator: ${SITE_A_OPERATOR_JWT}
resolver: MEMORY
resolver_preload {
  ${SITE_A_ACCOUNT_PUB}: ${SITE_A_ACCOUNT_JWT}
  ${SITE_A_SYS_PUB}:     ${SITE_A_SYS_JWT}
  ${SITE_B_ACCOUNT_PUB}: ${SITE_B_ACCOUNT_JWT}   # for cross-site source
}
jetstream { store_dir: /data/jetstream; max_mem: 1G; max_file: 10G }
websocket { port: 9222; no_tls: true }
```

site-b's `nats.conf` is symmetric. Cross-account subject exports on the
`OUTBOX_<siteID>` subjects (and matching imports) are added to the account
JWTs during `setup.sh` via `nsc edit account ... --rm/--ai-export
outbox.site-a.>`.

### Federation init container

`docker-local/compose.federation.yaml` declares one service:

```yaml
name: chat-local-federation

services:
  federation-init:
    image: natsio/nats-box:latest
    container_name: chat-local-federation-init
    profiles: ["init"]
    depends_on:
      site-a-inbox-worker:
        condition: service_started
      site-b-inbox-worker:
        condition: service_started
    volumes:
      - ./site-a/backend.creds:/creds/site-a.creds:ro
      - ./site-b/backend.creds:/creds/site-b.creds:ro
      - ./federation-init.sh:/init.sh:ro
    entrypoint: ["/bin/sh", "/init.sh"]
    networks:
      - chat-local

networks:
  chat-local:
    external: true
```

`docker-local/federation-init.sh` polls each site's NATS until the INBOX
stream exists (created by inbox-worker on startup with `BOOTSTRAP_STREAMS=true`),
then patches it with cross-site sources:

```sh
#!/bin/sh
set -eu

# Wait for inbox-worker on each side to create INBOX_<site>.
wait_for_stream() {
  local url="$1" creds="$2" stream="$3"
  for _ in $(seq 1 60); do
    if nats --server "$url" --creds "$creds" stream info "$stream" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for $stream on $url" >&2
  exit 1
}

wait_for_stream nats://nats-a:4222 /creds/site-a.creds INBOX_site-a
wait_for_stream nats://nats-b:4222 /creds/site-b.creds INBOX_site-b

# Layer cross-site sources onto the existing INBOX schema (services own
# Name+Subjects; ops/IaC owns Sources+SubjectTransforms — see CLAUDE.md).
nats --server nats://nats-a:4222 --creds /creds/site-a.creds stream update INBOX_site-a \
  --source 'OUTBOX_site-b' \
  --source-external-deliver 'nats://nats-b:4222' \
  --source-subject-transform-src 'outbox.site-b.to.site-a.>' \
  --source-subject-transform-dest 'chat.inbox.site-a.aggregate.>' \
  --defaults

nats --server nats://nats-b:4222 --creds /creds/site-b.creds stream update INBOX_site-b \
  --source 'OUTBOX_site-a' \
  --source-external-deliver 'nats://nats-a:4222' \
  --source-subject-transform-src 'outbox.site-a.to.site-b.>' \
  --source-subject-transform-dest 'chat.inbox.site-b.aggregate.>' \
  --defaults
```

Idempotent: running twice is a no-op because `nats stream update` with the
same source config is a no-op on JetStream.

*Note: the exact `nats` CLI flag names need verification against the version
shipped in `natsio/nats-box:latest` during implementation. If the CLI cannot
express the cross-NATS source in one command, fall back to a small Go binary
shipped in `tools/federation-init/` using `nats.go/jetstream` to PUT the
`StreamSource` directly. Plan author's bias: try the CLI first.*

## Makefile targets

```make
DEPS_COMPOSE        := docker-local/compose.deps.yaml
SITE_A_COMPOSE      := docker-local/compose.services.site-a.yaml
SITE_B_COMPOSE      := docker-local/compose.services.site-b.yaml
FED_COMPOSE         := docker-local/compose.federation.yaml
SITE_A_ENV          := docker-local/site-a.env
SITE_B_ENV          := docker-local/site-b.env

deps-up:
	@if [ ! -d docker-local/site-a ] || [ ! -d docker-local/site-b ]; then \
	  echo "First-time setup: generating per-site NATS creds + configs..."; \
	  ./docker-local/setup.sh; \
	fi
	docker compose -f $(DEPS_COMPOSE) up -d --wait
	docker compose -f $(DEPS_COMPOSE) --profile init run --rm cassandra-init

deps-down:
	docker compose -f $(DEPS_COMPOSE) down

up:
	@docker container inspect -f '{{.State.Running}}' chat-local-nats-a 2>/dev/null | grep -q true || { \
	  echo "Deps are not running. Run 'make deps-up' first."; exit 1; \
	}
	docker compose --env-file $(SITE_A_ENV) -f $(SITE_A_COMPOSE) up -d --build
	docker compose --env-file $(SITE_B_ENV) -f $(SITE_B_COMPOSE) up -d --build
	docker compose -f $(FED_COMPOSE) --profile init run --rm federation-init
	@echo "Both sites up. Following logs (Ctrl-C detaches; containers keep running)."
	docker compose --env-file $(SITE_A_ENV) -f $(SITE_A_COMPOSE) \
	               --env-file $(SITE_B_ENV) -f $(SITE_B_COMPOSE) logs -f

down:
	docker compose --env-file $(SITE_B_ENV) -f $(SITE_B_COMPOSE) down
	docker compose --env-file $(SITE_A_ENV) -f $(SITE_A_COMPOSE) down

logs:
ifndef SITE
	$(error SITE is required. Usage: make logs SITE=a|b)
endif
	docker compose --env-file docker-local/site-$(SITE).env \
	               -f docker-local/compose.services.site-$(SITE).yaml logs -f

federation-up:
	docker compose -f $(FED_COMPOSE) --profile init run --rm federation-init
```

The `make up SERVICE=<name>` standalone form goes away — running a single
service in two sites simultaneously is rarely useful, and a developer who
wants to iterate on one service can `docker compose -f <service>/deploy/...`
directly (defaults to site-a thanks to the parameterized compose).

## Compose deps changes

`docker-local/compose.deps.yaml`:

- Rename existing `nats` service block to `nats-a`, container name
  `chat-local-nats-a`, volume mount `./site-a/nats.conf`.
- Add a second `nats-b` service block, container name `chat-local-nats-b`,
  ports `4322:4222 / 8322:8222 / 9322:9222`, volume mount
  `./site-b/nats.conf`, separate JetStream volume `nats-b-data`.
- Cassandra init: extend `docker-local/cassandra/init/*.cql` to create both
  `chat_site_a` and `chat_site_b` keyspaces (or duplicate the existing
  `00-keyspace.cql` parametrically). Tables in both keyspaces.

## Stages

Each stage stops at a green checkpoint and a commit. Pre-commit hook (lint +
tests) runs on every commit.

**Stage 1 — setup.sh + NATS configs + deps compose**

1. Rewrite `docker-local/setup.sh` to produce `site-a/` and `site-b/` directories
   with `nats.conf`, `backend.creds`, `account.seed`. Embed both account JWTs
   in each `nats.conf`'s `resolver_preload`. Write `site-a.env` and
   `site-b.env` with the corresponding `AUTH_SIGNING_KEY`.
2. Update `docker-local/compose.deps.yaml`: split `nats` into `nats-a` +
   `nats-b`, extend cassandra-init DDL for both keyspaces.
3. Add `docker-local/site-a/` and `docker-local/site-b/` to `.gitignore`.
4. Delete `docker-local/nats.conf`, `docker-local/backend.creds`,
   `docker-local/.env` (regeneratable).
5. Verify: `./docker-local/setup.sh && docker compose -f
   docker-local/compose.deps.yaml up -d --wait` — both NATSes pass
   healthchecks, both keyspaces exist via `cqlsh -e 'describe keyspaces'`.
6. Commit: "Split local dev into per-site NATS (site-a + site-b)".

**Stage 2 — Parameterize every service compose**

7. For each of the 12 backend services in
   `auth-service/, broadcast-worker/, history-service/, inbox-worker/,
   message-gatekeeper/, message-worker/, mock-user-service/,
   notification-worker/, room-service/, room-worker/, search-service/,
   search-sync-worker/`: rewrite `deploy/docker-compose.yml` per the
   "Per-service compose parameterization" template above. Preserve existing
   service-specific env (consumer names, log levels, anything else not
   covered by the template).
8. Verify per-service standalone: pick 2-3 services, `docker compose -f
   <service>/deploy/docker-compose.yml up --build` — defaults to site-a and
   connects to deps. (Skip the full system here; that's stage 3.)
9. Commit: "Parameterize per-service compose files with SITE_ID + NATS_HOST".

**Stage 3 — Aggregator composes + federation-init**

10. Write `docker-local/compose.services.site-a.yaml` and `.site-b.yaml`
    (identical `include` lists).
11. Write `docker-local/compose.federation.yaml` and
    `docker-local/federation-init.sh`. During implementation, verify the
    `nats` CLI in `natsio/nats-box:latest` supports the `--source-external-*`
    flags; if not, replace with a Go binary in `tools/federation-init/`.
12. Verify end-to-end: stop everything, `make deps-up`, then
    `docker compose --env-file docker-local/site-a.env -f
    docker-local/compose.services.site-a.yaml up -d --build`, same for
    site-b, then `docker compose -f docker-local/compose.federation.yaml
    --profile init run --rm federation-init`. Inspect
    `nats --server nats://localhost:4222 --creds docker-local/site-a/backend.creds
    stream info INBOX_site-a` → confirm `Sources: [OUTBOX_site-b @
    nats://nats-b:4222]` and subject transform present. Same for site-b.
13. Smoke: send a message in site-a (via existing loadgen or
    auth-service+history-service), confirm `messages_by_room` row appears
    in `chat_site_b` keyspace, confirm a search query against site-b's
    `messages-site-b-v1` index finds it.
14. Commit: "Add aggregator composes + federation-init for two-site local dev".

**Stage 4 — Makefile + docs**

15. Rewrite the `deps-up`, `up`, `down` Makefile targets per the spec above.
    Add `logs SITE=` and `federation-up`. Remove or deprecate
    `up SERVICE=<name>`.
16. Add `docs/local-dev.md` (or update the existing one) describing:
    - the two-site topology
    - `make deps-up && make up`
    - how to run two `npm run dev` frontends, one per site, with
      `VITE_DEFAULT_SITE_ID=site-a` (port 5173) and `VITE_DEFAULT_SITE_ID=site-b`
      (port 5174) and the corresponding `VITE_AUTH_URL` / `VITE_NATS_URL`
    - how to inspect cross-site federation via the `nats` CLI
17. Verify: clean clone → `make deps-up` → `make up` → both sites healthy.
18. Commit: "Update Makefile and docs for two-site local dev workflow".

## Verification checklist (final stage gate)

- [ ] `make deps-up` succeeds from a clean state (no pre-existing creds dirs).
- [ ] Both `chat-local-nats-a` and `chat-local-nats-b` healthchecks pass.
- [ ] `mongosh` shows `chat_site_a` and `chat_site_b` databases after first
      service writes.
- [ ] `cqlsh -e 'describe keyspaces'` shows both `chat_site_a` and `chat_site_b`.
- [ ] `make up` brings up all 24 service containers (12 × 2) plus the
      federation-init runs once and exits 0.
- [ ] `docker ps --format '{{.Names}}'` shows `site-a-*` and `site-b-*`
      pairs and no name collisions.
- [ ] `nats stream info INBOX_site-a` (against nats-a) shows
      `OUTBOX_site-b` as an external source.
- [ ] End-to-end smoke: send a message in site-a → row appears in site-b's
      Cassandra keyspace → site-b search query returns it.
- [ ] `make down` cleanly stops both site stacks and federation-init,
      leaving deps running.
- [ ] `make deps-down` removes everything.

## Risks and open items

1. **`nats` CLI cross-NATS source flags.** The exact CLI invocation in
   `federation-init.sh` needs to be verified against the `nats-box:latest`
   shipped version. Fallback is a small Go binary using
   `jetstream.Stream.UpdateConfiguration` directly. Decision point during
   stage 3, step 11.

2. **Cassandra single-node memory footprint with two keyspaces.** Existing
   container is capped at 1.5 GB. Two keyspaces increase memtable pressure
   marginally but shouldn't push past the cap; if Cassandra OOMs under
   two-site load, bump `mem_limit` to 2 GB.

3. **Port assignments.** The host port table above is provisional. Audit each
   service's existing `ports:` block in stage 2 and adjust if any service
   already binds outside the 8080-8089 range.

4. **Frontend.** Stays out of compose. Doc-only treatment: two `npm run dev`
   instances, each with its own `.env.local`. If users want the frontend
   containerized per-site too, that's a follow-up.

5. **Test isolation.** None of the integration tests run against this stack —
   they use testcontainers per-package. No changes needed there.

6. **`docs/client-api.md`.** No client-facing handler signatures change, so
   no update required by the CLAUDE.md guardrail.
