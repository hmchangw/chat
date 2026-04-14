# Room Member Management v2 — Local Demo

An end-to-end local walkthrough of the feature delivered on this branch
(`claude/setup-local-demo-qjHbt`): **room member management v2**. The demo
drives `room-service` and `room-worker` through add / promote / remove flows
and prints every NATS event the room's site emits, so you can see the full
pipeline — including cross-site outbox replication — without leaving your
laptop.

Spec: [`docs/superpowers/specs/2026-04-10-room-member-management-v2-design.md`](../docs/superpowers/specs/2026-04-10-room-member-management-v2-design.md)

## What the demo covers

| Step | Action | Expected events |
|-----:|--------|-----------------|
| 4 | `alice` adds `bob` (cross-site) + `carol` | `subscription.update` × 2 · `room.event.member` · `msg.canonical.site-local.created` (system message `members_added`) · `outbox.site-local.to.site-remote.member_added` (bob only) |
| 5 | `alice` promotes `bob` to owner | `subscription.update` · `outbox.site-local.to.site-remote.role_updated` |
| 6 | `alice` adds the `eng` org | `subscription.update` per resolved member · `room.event.member` · `msg.canonical.*.created` (org members resolved via `users.find({sectId:"eng"})`, `eve.bot` filtered) |
| 7 | `alice` removes `bob` | `subscription.update` · `room.event.member` · system message `member_removed` · `outbox.site-local.to.site-remote.member_removed` |

The scenario dumps the final state of the `rooms`, `subscriptions`, and
`room_members` collections so you can confirm what room-worker persisted.

## What's in the stack

| Service | Role |
|---------|------|
| `nats` | NATS 2.11 with JetStream enabled (ports `4222`, `8222`) |
| `mongodb` | MongoDB 8 (port `27017`) |
| `room-service` | Validates NATS requests, publishes to `ROOMS_site-local` |
| `room-worker` | Consumes `ROOMS_site-local`, writes subscriptions / room_members / userCount, publishes events + outbox |

`message-worker` / Cassandra are intentionally left out — the demo subscribes
directly to `chat.msg.canonical.site-local.created` so you can see the
`members_added` / `member_removed` system messages without having to
bootstrap the Cassandra schema.

## Prerequisites

- Docker + Docker Compose
- Go 1.25 (for running the scenario binary on the host)

Nothing else — no NATS CLI, no separate seeding step.

## Run it

From the repo root:

```bash
# 1. Bring up NATS, MongoDB, room-service, room-worker
docker compose -f demo/docker-compose.yml up --build -d

# 2. Wait a couple of seconds for MongoDB + NATS to accept connections, then:
docker compose -f demo/docker-compose.yml logs --tail=20 room-service room-worker
# You should see "room-service running" and "room-worker running" lines.

# 3. Run the scenario (seeds data, publishes requests, captures events)
go run ./demo/scenario
```

The scenario prints nine numbered sections. Each NATS request shows its
subject, the outbound payload, and the reply. Each captured event is
pretty-printed directly underneath the request that produced it.

## Inspecting the state afterwards

```bash
# NATS server stats (JetStream streams: MESSAGES_CANONICAL_site-local,
# ROOMS_site-local, OUTBOX_site-local)
open http://localhost:8222   # macOS — any browser works

# Look at Mongo directly
docker exec -it chat-demo-mongodb mongosh chat_demo --eval 'db.subscriptions.find().pretty()'
docker exec -it chat-demo-mongodb mongosh chat_demo --eval 'db.room_members.find().pretty()'
docker exec -it chat-demo-mongodb mongosh chat_demo --eval 'db.users.find().pretty()'
```

You can also re-run `go run ./demo/scenario` safely — it clears the
`users`, `rooms`, `subscriptions`, and `room_members` collections on every
run and re-seeds a fresh room.

## Tearing down

```bash
docker compose -f demo/docker-compose.yml down -v
```

## Configuration knobs

Environment variables override the scenario defaults:

| Var | Default | Purpose |
|-----|---------|---------|
| `NATS_URL` | `nats://localhost:4222` | NATS server the demo connects to |
| `MONGO_URI` | `mongodb://localhost:27017` | MongoDB server |
| `MONGO_DB` | `chat_demo` | Database name (must match `room-service` + `room-worker`) |
| `SITE_ID` | `site-local` | Local site identifier — must match the services' `SITE_ID` |

If you change `SITE_ID` or `MONGO_DB`, update the environment in
`demo/docker-compose.yml` and restart the services too.

## Troubleshooting

- **`requester not in room`** — the scenario creates an owner subscription
  for `alice` before publishing any requests. If you see this error, the
  seeding step failed; run `go run ./demo/scenario` again and check the
  MongoDB connection.
- **No events captured** — the scenario subscribes before it publishes, but
  if you brought the stack up against an old NATS server with leftover
  streams the subjects can get redirected. Tear down with `down -v` and
  start again.
- **Scenario hangs at step 4 with "no responders"** — `room-service` did not
  register its queue-group subscribers yet. Give it a couple of seconds
  after `docker compose up` and re-run the scenario.
