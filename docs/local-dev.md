# Local Development

The local-dev stack runs two federated sites side-by-side on a single
machine: `site-a` and `site-b`. Each site has its own NATS + JetStream
instance and its own copies of every microservice; Mongo, Cassandra,
Elasticsearch, Valkey, and Keycloak are shared containers with per-site
logical isolation (databases / keyspaces / index prefixes).

## Quickstart

```sh
make deps-up   # generates per-site NATS creds on first run, then brings
               # up Mongo, Cassandra, ES, Valkey, Keycloak, and both NATSes
make up        # builds + starts every service for both sites, runs
               # federation-init to wire cross-site stream sources,
               # then tails logs from both sites
```

`make up` exits its log tail on Ctrl-C but leaves containers running. Use
`make down` to stop the service stacks (deps stay up) and `make deps-down`
to stop the deps.

| Target | What it does |
|---|---|
| `make deps-up` | shared deps + both NATSes, runs `cassandra-init` |
| `make deps-down` | stop everything started by `deps-up` |
| `make up` | build + start both sites detached, run federation-init, tail logs |
| `make down` | stop both site stacks |
| `make logs SITE=a` (or `=b`) | tail one site's logs only |
| `make federation-up` | re-run federation-init (idempotent) |

## Topology

```
┌──────────────────── chat-local docker network ────────────────────┐
│                                                                   │
│  Shared deps:                                                     │
│    mongodb (27017)  cassandra (9042)  elasticsearch (9200)        │
│    valkey (6379)    keycloak (8180)                               │
│                                                                   │
│  Per-site NATS:                                                   │
│    nats-a   host: 4222 (client) / 8222 (mon) / 9222 (ws)          │
│    nats-b   host: 4322 (client) / 8322 (mon) / 9322 (ws)          │
│                                                                   │
│  Site A services           Site B services                        │
│    site-a-auth-service      site-b-auth-service                   │
│    site-a-history-service   site-b-history-service                │
│    site-a-room-service      site-b-room-service                   │
│    site-a-search-service    site-b-search-service                 │
│    site-a-message-*         site-b-message-*                      │
│    site-a-room-worker       site-b-room-worker                    │
│    site-a-inbox-worker      site-b-inbox-worker                   │
│    …                        …                                     │
│                                                                   │
│  One-shot (init profile):                                         │
│    chat-local-federation-init                                     │
└───────────────────────────────────────────────────────────────────┘
```

## Per-site host ports

| Service | site-a | site-b |
|---|---|---|
| `auth-service` | 8080 | 8081 |
| `search-service` `/metrics` | 9090 | 9091 |
| `nats` (client / monitor / ws) | 4222 / 8222 / 9222 | 4322 / 8322 / 9322 |

All other services do not publish a host port — reach them through their
site's NATS, or via the auth-service that owns the room/history/search
HTTP surfaces.

## Per-site state

| Resource | site-a | site-b |
|---|---|---|
| Mongo DB | `chat_site_a` | `chat_site_b` |
| Cassandra keyspace | `chat_site_a` | `chat_site_b` |
| ES message index | `messages-site-a-v1` | `messages-site-b-v1` |
| Container names | `site-a-<service>` | `site-b-<service>` |

## Federation

Each site's NATS connects to its peer as a leafnode (port 7422, internal
to the docker network). The leafnode bridges the `chatapp` account between
the two NATSes so JetStream stream sourcing can pull across them.

`tools/federation-init` (run automatically at the tail of `make up` and
manually via `make federation-up`) layers a `StreamSource` onto each site's
`INBOX_<site>` stream that pulls `OUTBOX_<peer>` via JetStream Domain.
Subject transform: `outbox.<peer>.to.<site>.>` → `chat.inbox.<site>.aggregate.>`.

Inspect the wiring after `make up`:

```sh
docker run --rm --network chat-local -v $PWD/docker-local/site-a:/creds:ro \
  natsio/nats-box:latest \
  nats --server nats://nats-a:4222 --creds /creds/backend.creds \
       stream info INBOX_site-a
```

Look for `Sources:` listing `OUTBOX_site-b` with the expected
`Domain: site-b` and `FilterSubject: outbox.site-b.to.site-a.>`.

## Running the frontend against a site

The chat-frontend is not part of `make up`. Run it with `npm run dev`
pointing at the site you want to test:

```sh
# site-a frontend (port 5173, the Vite default)
cd chat-frontend
VITE_AUTH_URL=http://localhost:8080 \
VITE_NATS_URL=ws://localhost:9222 \
VITE_DEFAULT_SITE_ID=site-a \
npm run dev

# site-b frontend (port 5174) — run in a second terminal
cd chat-frontend
VITE_AUTH_URL=http://localhost:8081 \
VITE_NATS_URL=ws://localhost:9322 \
VITE_DEFAULT_SITE_ID=site-b \
npm run dev -- --port 5174
```

Log into each side with the same Keycloak user (single shared realm) to
see cross-site events flow.

## Iterating on a single service

`make up` rebuilds every service. To iterate quickly on one service, use
docker compose directly — defaults are wired so a bare invocation runs
the site-a copy of the service against site-a deps:

```sh
# site-a (defaults)
docker compose -f message-worker/deploy/docker-compose.yml up --build

# site-b — pass site-b's env file
docker compose --env-file docker-local/site-b.env \
               -f message-worker/deploy/docker-compose.yml up --build
```

## Regenerating creds

`docker-local/setup.sh` is idempotent on the directory level but
overwrites every file. If you want a fresh setup:

```sh
make down
make deps-down
docker volume rm chat-local-deps_nats-a-data chat-local-deps_nats-b-data
rm -rf docker-local/site-a docker-local/site-b
make deps-up
make up
```
