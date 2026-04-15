# Room Member Management v2 — Cross-Site Local Demo

An end-to-end, cross-site local testbed for the feature on this branch
(`claude/setup-local-demo-qjHbt`): **room member management v2**. The stack
runs two independent sites — **`f12`** and **`f18`** — each with its own NATS
cluster and MongoDB database, sharing a single Docker Compose file. You drive
it from **three terminals** so you can see requests, events, and DB state
change at the same time.

Spec: [`docs/superpowers/specs/2026-04-10-room-member-management-v2-design.md`](../docs/superpowers/specs/2026-04-10-room-member-management-v2-design.md)

## Topology

```
                      ┌─────────────────────────┐
                      │       MongoDB :27017    │
                      │  chat_f12     chat_f18  │
                      └─────────▲───────▲───────┘
                                │       │
             ┌──────────────────┤       ├──────────────────┐
             │                  │       │                  │
   ┌─────────┴─────────┐        │       │        ┌─────────┴─────────┐
   │ room-service-f12  │────────┤       ├────────│ room-service-f18  │
   │ room-worker-f12   │        │       │        │ room-worker-f18   │
   └──────────▲────────┘        │       │        └────────▲──────────┘
              │                 │       │                 │
      ┌───────┴────────┐                          ┌───────┴────────┐
      │ nats-f12 :4222 │  (outbox events observable on origin)     │
      │ mon.     :8222 │                          │ nats-f18 :4223 │
      └────────────────┘                          │ mon.     :8223 │
                                                  └────────────────┘
```

- Two NATS clusters simulate two sites. There is **no NATS gateway** and
  **no `inbox-worker`** in the stack — outbox events are observable on the
  **origin** site (that's what we're testing) but do not physically
  propagate. In production gateways + inbox-worker would replicate them.
- MongoDB is shared but the databases are per-site (`chat_f12`, `chat_f18`),
  matching the real system where each site has its own operational DB.
- `message-worker` / Cassandra are intentionally absent — system messages
  are observed directly on `chat.msg.canonical.{site}.created`.

## What's in `demo/`

| Path | Purpose |
|---|---|
| `docker-compose.yml` | 2× NATS + Mongo + 2× `room-service` + 2× `room-worker` |
| `cli/` | Request driver — subcommands to create rooms, add/remove members, update roles, seed users, snapshot DB, reset |
| `watch/` | NATS watcher — subscribes to all event subjects on both sites, pretty-prints each event with a coloured site tag |
| `dbwatch/` | Mongo polling observer — prints a snapshot of rooms / subscriptions / room_members whenever they change |
| `scenario/` | One-shot scripted walkthrough that exercises add / promote / remove / org-add against f12 in a single terminal |

## Prerequisites

- Docker + Docker Compose
- Go 1.25 (for the CLI and observer binaries)

Host ports used: **4222, 4223, 8222, 8223, 27017**. Make sure none of them are
already bound before `docker compose up`.

## 3-terminal workflow

### Terminal 1 — **request driver**

```bash
# from the repo root
docker compose -f demo/docker-compose.yml up --build -d

# populate users in both sites (alice/carol/dave/eve.bot on f12, bob/frank on f18)
go run ./demo/cli seed
```

Leave this terminal free — you'll use it for `go run ./demo/cli …` commands.

### Terminal 2 — **NATS observer**

```bash
go run ./demo/watch
```

Watches all relevant subjects on both NATS clusters. Each printed line is
tagged `[f12]` (cyan) or `[f18]` (magenta). Keep it running.

### Terminal 3 — **MongoDB observer**

```bash
go run ./demo/dbwatch
```

Polls both `chat_f12` and `chat_f18` every 2 seconds and prints a compact
snapshot whenever anything changes. Keep it running.

> 💡 Alternative: open `http://localhost:8222` or `http://localhost:8223` in a
> browser for the NATS monitoring UI (streams, consumers, connections).

## Scenarios

All scenarios run from **Terminal 1**. Watch Terminals 2 and 3 react.

### A. Base room operations (create / list / get / add / remove)

```bash
# 1. alice (f12) creates a group room
go run ./demo/cli room create --site f12 --owner alice --name general

# copy the roomId from the reply into $R
R=<paste-room-id>

# 2. list & get
go run ./demo/cli room list --site f12 --requester alice
go run ./demo/cli room get  --site f12 --requester alice --room $R

# 3. alice adds carol + dave (both on f12)
go run ./demo/cli member add --site f12 --requester alice --room $R --users carol,dave

# 4. alice removes dave
go run ./demo/cli member remove --site f12 --requester alice --room $R --account dave
```

**What you should see**

| Terminal | Signal |
|---|---|
| 2 (watch) | `chat.room.canonical.f12.member.add` (ROOMS stream), then `chat.user.carol.event.subscription.update`, `chat.user.dave.event.subscription.update`, `chat.room.{R}.event.member`, `chat.msg.canonical.f12.created` (system message `members_added`). Remove fires the mirror events plus a `member_removed` system message. |
| 3 (dbwatch) | `chat_f12` subscriptions count goes from 1 → 3 → 2 and `rooms.userCount` updates in lockstep. |

> Note: this codebase does not expose a **delete room** operation — only
> create / list / get. Members can be removed individually or by org.

### B. Cross-site federation (flipped outbox)

Bob lives on `f18`. Alice owns a room on `f12`. Watch the outbox event fire
from f12.

```bash
# create a new room for this scenario
go run ./demo/cli room create --site f12 --owner alice --name fed-test
R=<paste-room-id>

# alice adds bob (home site f18) → cross-site
go run ./demo/cli member add --site f12 --requester alice --room $R --users bob
```

**Expected:**

- Terminal 2 shows `outbox.f12.to.f18.member_added` alongside the usual
  `subscription.update`, `room.event.member`, and `msg.canonical.f12.created`.
- Terminal 3 shows a new subscription for `bob` in **`chat_f12`** with
  `siteId=f12` (the room's site). It does **not** show up in `chat_f18`
  because nothing is consuming the outbox in this demo — in production
  `inbox-worker` on f18 would pick it up.

Now promote Bob cross-site (this is the "federation guard removed" change):

```bash
go run ./demo/cli role --site f12 --requester alice --room $R --account bob --to owner
```

Expected: `subscription.update` with `action: role_updated` and
`outbox.f12.to.f18.role_updated`.

Remove Bob to see the flipped outbox in the other direction:

```bash
go run ./demo/cli member remove --site f12 --requester alice --room $R --account bob
```

Expected: `outbox.f12.to.f18.member_removed` plus the matching system
message.

### C. Role management — promote / demote / last-owner guard

```bash
# start from a room with two members (alice owner, carol member)
go run ./demo/cli room create  --site f12 --owner alice --name roles-test
R=<paste-room-id>
go run ./demo/cli member add  --site f12 --requester alice --room $R --users carol

# promote carol to owner
go run ./demo/cli role --site f12 --requester alice --room $R --account carol --to owner

# demote carol back to member
go run ./demo/cli role --site f12 --requester alice --room $R --account carol --to member

# last-owner guard: alice tries to demote herself while she is the last owner
go run ./demo/cli member remove --site f12 --requester alice --room $R --account carol
go run ./demo/cli role          --site f12 --requester alice --room $R --account alice --to member
# → room-service rejects with "cannot demote the last owner"
```

The guard runs in `room-service` before anything touches the ROOMS stream,
so you will see no events in Terminal 2 for the last rejected request —
only the error reply in Terminal 1.

### D. Organisation management — org resolution + bot filtering

Seed puts `alice`, `carol`, `dave`, `eve.bot`, `p_system` in the `eng` org,
and `mallory` in `sales`. When alice adds the `eng` org, room-service uses
`users.find({sectId: "eng"})` to expand it and drops `.bot` and `p_*`
accounts via the bot filter.

```bash
go run ./demo/cli room create --site f12 --owner alice --name org-test
R=<paste-room-id>

# add the engineering org
go run ./demo/cli member add --site f12 --requester alice --room $R --orgs eng
```

**Expected in Terminal 2:**

- `chat.room.canonical.f12.member.add` — the **normalised** payload
  published to the ROOMS stream shows `eve.bot` and `p_system` **already
  filtered out**, with only `alice, carol, dave` in `users`.
- `subscription.update` fires for every newly-added member (not alice, she
  already had a subscription).
- `room.event.member` lists the added accounts.
- `msg.canonical.f12.created` — system message with
  `type: "members_added"` and `sysMsgData.orgs = ["eng"]`.

**Expected in Terminal 3:** `chat_f12.room_members` gains an `org` entry
for `eng` **and** individual entries for each expanded account (the
branch's change: individuals are written alongside the org when orgs are
present). `subscriptions` gains rows for `carol` and `dave` only.

Now remove the whole org:

```bash
go run ./demo/cli member remove --site f12 --requester alice --room $R --org eng
```

`subscriptions` for carol/dave go away, `room_members` for the `eng` org
goes away.

Mixing orgs and cross-site users in one call demonstrates another branch
change — org resolution and outbox both fire:

```bash
go run ./demo/cli room create --site f12 --owner alice --name mix
R=<paste-room-id>
go run ./demo/cli member add --site f12 --requester alice --room $R --orgs eng --users bob
# → outbox.f12.to.f18.member_added (bob)
# → carol, dave, alice already processed; bot filtered
```

## Snapshot / reset at any time

```bash
# print all DB state for both sites
go run ./demo/cli snapshot --all

# just one site
go run ./demo/cli snapshot --site f12

# wipe all collections in both sites (users, rooms, subscriptions, room_members)
go run ./demo/cli reset --all
go run ./demo/cli seed          # re-seed users afterwards
```

## One-shot smoke test

If you just want to see everything run without orchestrating three
terminals, there is still a scripted walkthrough:

```bash
go run ./demo/scenario
```

It targets site `f12`, seeds both databases with the fixture, creates a
room, and runs add → promote → org-add → remove, capturing events inline.

## Configuration

All binaries default to the host ports exposed by `docker-compose.yml`.
Override with environment variables or flags:

| Var / flag | Default | Used by |
|---|---|---|
| `NATS_F12` / `--nats-f12` | `nats://localhost:4222` | `cli`, `watch` |
| `NATS_F18` / `--nats-f18` | `nats://localhost:4223` | `cli`, `watch` |
| `MONGO_URI` / `--mongo-uri` | `mongodb://localhost:27017` | `cli`, `dbwatch` |
| `NATS_URL` | `nats://localhost:4222` | `scenario` (f12 only) |
| `MONGO_DB` | `chat_f12` | `scenario` |
| `SITE_ID` | `f12` | `scenario` |

## Tearing down

```bash
docker compose -f demo/docker-compose.yml down -v
```

## Troubleshooting

- **`error: room-service rejected: requester not in room`** — the
  requester account isn't an owner of the room on the targeted site. Check
  with `cli snapshot --site <site>` that the subscription exists, and
  remember that `--site` has to be the site of the **room**, not the
  requester's home site.
- **`error: room-service rejected: only owners can …`** — the requester
  is in the room but has `roles: [member]`. Promote them first via
  `cli role … --to owner`.
- **`error: ambiguous request: specify either orgId or account, not both`**
  — `member remove` takes exactly one of `--account` or `--org`.
- **No events in Terminal 2** — confirm `watch` printed the "watching
  nats://…" banner for both sites. If it only shows one, check that
  `nats-f18` came up: `docker compose -f demo/docker-compose.yml ps`.
- **Rooms created via `room create` don't show up in `dbwatch`** —
  `room create` does not include a `siteID` in its subject (the CRUD
  endpoint is site-agnostic in this codebase), so it goes to whichever
  `room-service` queue-group member on that NATS cluster grabs it first.
  Since each NATS cluster only has one `room-service` instance in this
  demo, it will always be that site's. If you override `--nats-f12` to
  point at `f18`'s NATS, the room will end up in `chat_f18`.
