# loadgen

Capacity-baseline load generator for the chat platform.

## Quick start

    ./tools/loadgen/scripts/up.sh
    ./tools/loadgen/scripts/quickstart.sh
    ./tools/loadgen/scripts/down.sh

`make up` brings up the shared `docker-local` stack (NATS, MongoDB,
Cassandra, Valkey, Elasticsearch, every microservice) and then the
load-test-only overlay (loadgen, Prometheus, Grafana). The overlay joins
the `chat-local` network so it can reach the same services any developer
sees with `make up` at the repo root.

## Discoverability

    loadgen scenarios     # list all registered scenarios
    loadgen presets       # list all built-in presets
    loadgen recommend --target-rps=N --duration=5m
    loadgen doctor        # check host readiness

## Encryption

`broadcast-worker` runs with `ENCRYPTION_ENABLED=true` by default in this
stack. `loadgen seed` provisions one P-256 keypair per fixture room into
Valkey (the same Valkey `broadcast-worker` reads from), derived from the
RNG seed so runs stay reproducible. To run an apples-to-apples plaintext
comparison:

```
ENCRYPTION_ENABLED=false make -C tools/loadgen/deploy up
```

Loadgen's end-to-end broadcast correlation reads `RoomEvent.LastMsgID`,
which sits in the cleartext envelope regardless of encryption mode, so
the run binary itself never touches ciphertext.

## Where to find more

| For X                                    | See Y                                    |
|------------------------------------------|------------------------------------------|
| Recipes & worked examples                | [USAGE.md → Recipes](USAGE.md#recipes)   |
| Concepts (HDR, omission, verdicts)       | [USAGE.md → Concepts](USAGE.md#concepts) |
| Per-scenario reference                   | [USAGE.md → Scenarios](USAGE.md#scenarios) |
| Tuning knobs (every flag)                | [USAGE.md → Tuning](USAGE.md#tuning-knobs) |
| Migrating from v1                        | [USAGE.md → Migrating](USAGE.md#migrating-from-v1) |
| Changelog                                | [CHANGES.md](CHANGES.md)                 |
| UNTRUSTED run triage                     | [docs/runbooks/loadgen-untrusted.md](docs/runbooks/loadgen-untrusted.md) |
| Per-scenario specifics                   | [docs/scenarios/](docs/scenarios/)       |
| Design rationale (the v2 spec)           | [../../docs/superpowers/specs/2026-05-12-loadgen-v2-design.md](../../docs/superpowers/specs/2026-05-12-loadgen-v2-design.md) |

## Non-goals

- Not a CI regression gate; invoked manually.
- Not an auth benchmark. Uses shared `backend.creds`.
- Not a cross-site benchmark. Single-site only.
- Not a cross-machine numerical benchmark; compare within one machine across changes.
- Not a production replay tool.

## Members workload (add-member benchmark)

Benchmarks the add-member pipeline:
`room-service.handleAddMembers` → `chat.room.canonical.{siteID}.member.add`
(ROOMS stream) → `room-worker` → `chat.room.{roomID}.event.member` broadcast.

### Quick start

```
make -C tools/loadgen/deploy up
make -C tools/loadgen/deploy seed-members PRESET=members-medium
make -C tools/loadgen/deploy run-sustained PRESET=members-medium RATE=100 DURATION=60s
```

For capacity-mode growth curves:

```
make -C tools/loadgen/deploy seed-members PRESET=members-capacity
make -C tools/loadgen/deploy run-capacity  PRESET=members-capacity TARGET_SIZE=500
```

Between sustained runs, reset state so candidate pools refill:

```
make -C tools/loadgen/deploy reset-members PRESET=members-medium
```

### Presets

| preset             | rooms | baseline | candidate pool | use case                                |
|--------------------|-------|----------|----------------|-----------------------------------------|
| `members-small`    | 5     | 10       | 50             | smoke / dev                             |
| `members-medium`   | 100   | 100      | 500            | sustained-throughput default            |
| `members-capacity` | 5     | 1        | 990            | capacity-growth, fills up to ~MAX_ROOM_SIZE |

### Subcommands

- `loadgen seed --workload=members --preset=<name>` — populate Mongo
  + Valkey for the members workload.
- `loadgen teardown --workload=members --preset=<name>` — drop the seeded data.
- `loadgen members-sustained --preset=<name> [flags]` — open-loop publish
  at `--rate` req/sec for `--duration`. Flags: `--users-per-add` (default 10),
  `--inject=frontdoor|canonical` (default frontdoor),
  `--shape=users` (v1; orgs/channels/mixed reserved for v2), `--warmup`,
  `--csv`.
- `loadgen members-capacity --preset=<name> --target-size=N [flags]` —
  per-room sequential growth until rooms reach `--target-size`. Flags:
  `--users-per-add`, `--inject`, `--shape`, `--max-rate` (per-room rate
  cap, default 0 = sequential pacing only), `--e2-timeout`, `--csv`.

### v1 scope

Only `--shape=users` is implemented. The flag accepts `orgs`, `channels`,
`mixed` for forward compat but rejects them at parse time. See
`docs/superpowers/specs/2026-05-19-load-test-room-members-design.md`
for the rationale and the v2 plan.

### Reading the summary

- **Sustained mode**: `final_pending == 0` on room-worker + zero errors →
  pipeline is sustaining the target rate. Climbing `final_pending` or
  non-zero errors → over capacity. If you see `aborted early — pools
  exhausted` in the logs, your `rate × duration × users-per-add` exceeded
  the preset's `CandidatePool` budget; pick a bigger preset or shorter
  duration.
- **Capacity mode**: the size-bucket table shows latency at four
  size ranges; the `final sizes` block confirms each room hit
  `--target-size`. A row with `count > 0` whose `e2_p99` is much larger
  than smaller-size buckets indicates a per-room-size degradation.
