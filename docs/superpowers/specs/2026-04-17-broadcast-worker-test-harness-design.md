# Broadcast Worker Local Test Harness — Design

## Purpose

Give operators a zero-code way to exercise the `broadcast-worker` against a
locally running docker-compose stack, covering:

- Group message with no mentions
- Group message with an individual mention (`@bob`)
- Group message with `@all`
- DM message that crosses a NATS supercluster gateway to a subscriber on a
  different site

The harness must work without installing Go tools, `jq`, or any NATS client on
the host — only `docker`, `docker compose`, `make`, and `bash`.

## Scope

In scope:

- A `docker-compose.test.yml` that brings up a two-site NATS supercluster,
  one MongoDB, and one broadcast-worker on site1.
- Static seed data (users, rooms, subscriptions) loaded into MongoDB.
- Static per-scenario `MessageEvent` payloads published to the
  `MESSAGES_CANONICAL_site1` stream.
- Per-scenario verification scripts that query MongoDB and print/assert the
  expected state change.
- A scoped `Makefile` under `broadcast-worker/deploy/` that wraps all of the
  above.

Out of scope:

- Automated Go tests (the existing integration tests already cover the
  handler logic).
- Editing the root `Makefile`.
- Dynamic ID / timestamp injection into scenario payloads.
- A second broadcast-worker instance on site2.
- A second MongoDB.

## Topology

Docker compose file: `broadcast-worker/deploy/docker-compose.test.yml`.

- **`nats_site1`** — NATS 2.11-alpine, JetStream enabled, `server_name=site1`,
  cluster `site1`, gateway listening on `0.0.0.0:7222` with `nats_site2`
  declared as a remote gateway. Host-exposed client port `4222`, monitoring
  `8222`. Config mounted from `deploy/test/nats/nats-site1.conf`.
- **`nats_site2`** — same image, `server_name=site2`, cluster `site2`,
  gateway on `0.0.0.0:7222` with `nats_site1` as remote. Host-exposed client
  port `4223` (container `4222`), monitoring `8223` (container `8222`).
  Config mounted from `deploy/test/nats/nats-site2.conf`.
- **`mongodb`** — `mongo:8`, host port `27017`. One shared instance; site
  scoping is handled by the `siteId` field on documents.
- **`broadcast-worker`** — built from `broadcast-worker/deploy/Dockerfile`.
  Env: `SITE_ID=site1`, `NATS_URL=nats://nats_site1:4222`,
  `MONGO_URI=mongodb://mongodb:27017`, `MONGO_DB=chat`. Depends on
  `nats_site1` and `mongodb`.
- **`tools`** — `natsio/nats-box:latest`, started with
  `command: ["sleep", "infinity"]`. This is a long-lived helper container
  attached to the compose network; `publish.sh` uses
  `docker compose exec tools nats pub ...` instead of spinning up a new
  container per publish. Seed + verify scripts use
  `docker compose exec mongodb mongosh ...` for the same reason.

This leaves the existing `broadcast-worker/deploy/docker-compose.yml` untouched
so the simple single-node flow still works.

### Why a supercluster for a local test?

The cross-site DM scenario depends on the broadcast-worker publishing an event
on site1's core NATS and the subscriber receiving it on site2 via gateway
interest propagation. Without true gateway routing, this scenario collapses
into the single-site DM case and provides no signal.

JetStream stays local to each cluster, which matches production. Only core
NATS (room/user event subjects) crosses the gateway.

## File layout

```
broadcast-worker/deploy/
├── Makefile                         # scoped targets
├── docker-compose.test.yml          # supercluster + worker + mongo
└── test/
    ├── README.md                    # operator walkthrough
    ├── nats/
    │   ├── nats-site1.conf
    │   └── nats-site2.conf
    ├── seed/
    │   ├── users.json
    │   ├── rooms.json
    │   └── subscriptions.json
    ├── scenarios/
    │   ├── group-plain.json
    │   ├── group-mention-bob.json
    │   ├── group-mention-all.json
    │   └── dm-cross-site.json
    ├── verify/
    │   ├── group-plain.sh
    │   ├── group-mention-bob.sh
    │   └── group-mention-all.sh
    ├── seed.sh
    └── publish.sh
```

## Seed data

Users (`seed/users.json`):

| id       | account | siteId | engName    | chineseName |
|----------|---------|--------|------------|-------------|
| u-alice  | alice   | site1  | Alice Wang | 愛麗絲      |
| u-bob    | bob     | site1  | Bob Chen   | 鮑勃        |
| u-carol  | carol   | site2  | Carol Lee  | 卡蘿        |

Rooms (`seed/rooms.json`):

| _id     | name     | type  | siteId | userCount |
|---------|----------|-------|--------|-----------|
| group-1 | general  | group | site1  | 2         |
| dm-1    | (empty)  | dm    | site1  | 2         |

Subscriptions (`seed/subscriptions.json`):

| _id | u.id    | u.account | roomId  | siteId |
|-----|---------|-----------|---------|--------|
| s1  | u-alice | alice     | group-1 | site1  |
| s2  | u-bob   | bob       | group-1 | site1  |
| s3  | u-alice | alice     | dm-1    | site1  |
| s4  | u-carol | carol     | dm-1    | site1  |

Note: carol's subscription lives in site1's MongoDB because the room is owned
by site1. What makes the DM scenario "cross-site" is that carol's *NATS
client* (simulated via nats-debug) connects to `nats_site2`, so the event
must traverse the gateway.

### `seed.sh`

- `set -euo pipefail`.
- Waits for MongoDB readiness via `docker compose exec mongodb mongosh
  --quiet --eval "db.adminCommand('ping')"` with a 10 × 1s retry loop.
- Copies each seed JSON file into the `mongodb` container with
  `docker compose cp`, then drops and repopulates the collection with
  `docker compose exec mongodb mongosh mongodb://localhost:27017/chat
  --quiet --eval "db.<coll>.drop(); db.<coll>.insertMany(<parsed json>)"`.
- Idempotent: re-running fully resets the data.

## Scenario payloads

Each `scenarios/*.json` file is a complete, static `model.MessageEvent` — no
substitution, no external tools required at publish time. Hard-coded values:

| scenario             | roomId   | message.id            | content                     | createdAt            | timestamp       |
|----------------------|----------|-----------------------|-----------------------------|----------------------|-----------------|
| group-plain          | group-1  | m-group-plain         | `hello team`                | 2026-04-17T12:00:00Z | 1744891200000   |
| group-mention-bob    | group-1  | m-group-mention-bob   | `hey @bob can you review?`  | 2026-04-17T12:01:00Z | 1744891260000   |
| group-mention-all    | group-1  | m-group-mention-all   | `standup now @all`          | 2026-04-17T12:02:00Z | 1744891320000   |
| dm-cross-site        | dm-1     | m-dm-cross-site       | `ping from site1`           | 2026-04-17T12:03:00Z | 1744891380000   |

All messages are from `alice` (`userId=u-alice`, `userAccount=alice`).

Re-running a scenario republishes the same event and deterministically
overwrites MongoDB state with the same values. For observation in nats-debug
this is fine — each publish still produces a fresh NATS broadcast event.

### `publish.sh`

- Validates `SCENARIO` against the files in `scenarios/`.
- Reads the file into a shell variable with `"$(cat scenarios/$SCENARIO.json)"`.
- Publishes via the already-running `tools` service:

```
docker compose -f docker-compose.test.yml exec -T tools \
    nats --server nats://nats_site1:4222 \
    pub chat.msg.canonical.site1.created "$PAYLOAD"
```

`exec -T` (no TTY) keeps the script safe for non-interactive use. No `jq`,
no `envsubst`, no host-side NATS CLI, and no new container per publish.

## Verification scripts

Each script in `test/verify/` runs a MongoDB query via
`docker compose exec mongodb mongosh ...` (reusing the already-running
MongoDB container), prints the queried document, and exits 0 on success /
1 on failure.

- **`verify/group-plain.sh`**
  - Query: `db.rooms.findOne({_id:"group-1"}, {lastMsgAt:1, lastMsgId:1})`.
  - Pass: `lastMsgId === "m-group-plain"` **and** `lastMsgAt` ISO string
    starts with `2026-04-17T12:00:00`.
- **`verify/group-mention-bob.sh`**
  - Query: `db.subscriptions.findOne({roomId:"group-1", "u.account":"bob"},
    {hasMention:1})`.
  - Pass: `hasMention === true`.
- **`verify/group-mention-all.sh`**
  - Query: `db.rooms.findOne({_id:"group-1"}, {lastMentionAllAt:1})`.
  - Pass: `lastMentionAllAt` ISO string starts with `2026-04-17T12:02:00`.

No verify script for `dm-cross-site` — that scenario is validated by seeing
the event arrive in nats-debug on site2.

Scripts print a final `OK: <summary>` or `FAIL: <reason>` line.

## Makefile (scoped)

`broadcast-worker/deploy/Makefile`:

```make
COMPOSE ?= docker compose -f docker-compose.test.yml

.PHONY: up seed send verify down logs

up:
	$(COMPOSE) up -d --build

seed:
	./test/seed.sh

send:
	@test -n "$(SCENARIO)" || (echo "SCENARIO=<name> required" && exit 1)
	./test/publish.sh $(SCENARIO)

verify:
	@test -n "$(SCENARIO)" || (echo "SCENARIO=<name> required" && exit 1)
	./test/verify/$(SCENARIO).sh

down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f broadcast-worker
```

Invoked from the repo root as `make -C broadcast-worker/deploy <target>`.

## Manager demo script — step by step

Goal: demonstrate in ~5 minutes that the broadcast-worker correctly handles
three group scenarios and one cross-site DM scenario.

Prerequisites (one-time on the demo machine):

- `docker` + `docker compose` installed and running
- `make` and `bash` available
- Repo cloned; current working directory is the repo root
- Ports `4222`, `4223`, `8222`, `8223`, `27017`, `8090` free

### Step 1 — Bring up the stack (30 seconds)

```
make -C broadcast-worker/deploy up
```

Wait for the output to settle. Confirm all containers are healthy:

```
docker compose -f broadcast-worker/deploy/docker-compose.test.yml ps
```

Expected services: `nats_site1`, `nats_site2`, `mongodb`, `broadcast-worker`,
`tools` — all `running`.

### Step 2 — Seed MongoDB (5 seconds)

```
make -C broadcast-worker/deploy seed
```

Console shows "inserted 3 users, 2 rooms, 4 subscriptions".

### Step 3 — Start the nats-debug UI (separate terminal)

```
docker compose -f tools/nats-debug/deploy/docker-compose.yml up
```

Open http://localhost:8090 in a browser.

### Step 4 — Demo scenario 1: group message, no mention

In nats-debug:

- Source NATS: `nats://localhost:4222`
- Dest NATS: `nats://localhost:4222`
- Subscribe to `chat.room.group-1.event`

Publish:

```
make -C broadcast-worker/deploy send SCENARIO=group-plain
```

**Point out in the UI:** one event appears with `type: "new_message"`,
`roomType: "group"`, sender enriched with `engName: "Alice Wang"`.

Verify MongoDB side:

```
make -C broadcast-worker/deploy verify SCENARIO=group-plain
```

**Point out:** `lastMsgAt` on `group-1` is updated, `lastMsgId` is
`m-group-plain`, script prints `OK:`.

### Step 5 — Demo scenario 2: group message with @bob

Keep the same subscription open. Publish:

```
make -C broadcast-worker/deploy send SCENARIO=group-mention-bob
```

**Point out in the UI:** event payload now has a `mentions` array containing
bob's enriched record (engName `Bob Chen`, chineseName `鮑勃`).

Verify:

```
make -C broadcast-worker/deploy verify SCENARIO=group-mention-bob
```

**Point out:** bob's subscription row now has `hasMention: true`.

### Step 6 — Demo scenario 3: group message with @all

Publish:

```
make -C broadcast-worker/deploy send SCENARIO=group-mention-all
```

**Point out in the UI:** event payload has `mentionAll: true`.

Verify:

```
make -C broadcast-worker/deploy verify SCENARIO=group-mention-all
```

**Point out:** `lastMentionAllAt` on `group-1` is set.

### Step 7 — Demo scenario 4: cross-site DM over a NATS supercluster

This is the highlight scenario. In nats-debug, change the **Dest NATS** to
`nats://localhost:4223` (site2) and subscribe to
`chat.user.carol.event.room`.

**Explain to the audience:** the message is being published to site1's
JetStream. The broadcast-worker is running on site1. Carol's client is
listening on a completely separate NATS server on site2. The two servers
are connected via a NATS supercluster gateway.

Publish:

```
make -C broadcast-worker/deploy send SCENARIO=dm-cross-site
```

**Point out in the UI:** the event appears on the site2 subscription, proving
the broadcast crossed the gateway.

### Step 8 — Tear down

```
make -C broadcast-worker/deploy down
```

All data and containers removed.

## Operator workflow

1. `make -C broadcast-worker/deploy up`
2. `make -C broadcast-worker/deploy seed`
3. Launch `tools/nats-debug` UI. Configure:
   - Source NATS: `nats://localhost:4222` (site1)
   - Dest NATS: `nats://localhost:4222` or `nats://localhost:4223` depending
     on scenario.
4. Subscribe to relevant subjects:
   - Group scenarios: `chat.room.group-1.event` (dest = site1).
   - DM cross-site scenario: `chat.user.carol.event.room` (dest = site2).
5. Run each scenario:
   ```
   make -C broadcast-worker/deploy send SCENARIO=group-plain
   make -C broadcast-worker/deploy verify SCENARIO=group-plain

   make -C broadcast-worker/deploy send SCENARIO=group-mention-bob
   make -C broadcast-worker/deploy verify SCENARIO=group-mention-bob

   make -C broadcast-worker/deploy send SCENARIO=group-mention-all
   make -C broadcast-worker/deploy verify SCENARIO=group-mention-all

   make -C broadcast-worker/deploy send SCENARIO=dm-cross-site
   # verify visually in nats-debug on site2
   ```
6. `make -C broadcast-worker/deploy down` to tear everything down.

## Error handling

- All shell scripts use `set -euo pipefail`.
- `seed.sh` retries MongoDB ping 10 × 1s before giving up with a clear
  message.
- `publish.sh` rejects unknown `SCENARIO` values before invoking docker.
- Verify scripts exit 1 with a readable `FAIL:` line if the MongoDB document
  is missing or the assertion does not hold.

## Dependencies

Host-side:

- `docker` and `docker compose`
- `make`
- `bash` (scripts shebang `#!/usr/bin/env bash`)

No `jq`, `nats`, `mongosh`, `uuidgen`, Go, or Python required on the host.
All tool invocations happen inside throwaway containers launched by
`docker compose run --rm`.
