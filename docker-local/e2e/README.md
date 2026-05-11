# E2E Test Suite

Two-site backend end-to-end tests. Drives the full multi-service stack
(NATS supercluster + per-site Mongo / Cassandra / Elasticsearch / Valkey
/ Keycloak + all 11 services per site) through the same NATS subjects
and HTTP routes a real client would use.

## Quick start

```bash
make e2e            # full lifecycle: bring stack up, run tests, tear down
```

First run: 5-8 minutes on a 32GB / 8-core box (image pulls + builds
dominate). Subsequent runs against warm caches: 1-3 minutes.

## Iteration loop

```bash
make e2e-up         # bring stack up (auto-creates .env + secrets/ on first run)
make e2e-only       # run tests against the running stack
make e2e-only       # ...repeat as you iterate on tests
make e2e-down       # stop stack (keeps Keycloak realm + Cassandra schema)
make e2e-down-clean # stop + drop volumes (next up re-imports everything)
```

`e2e-only` skips `compose up` so a typed-correction → re-run loop is
fast.

## Debugging a failure

`make e2e-logs SERVICE=msg-gk-a` tails a single container's logs.

When a federation test fails, the harness writes the last 1000 lines of
each relevant service log to `e2e/logs/<TestName>/<service>.log`.
Passing tests leave the directory absent. The path is gitignored.

## Customizing image registries

`docker-local/e2e/.env` is auto-created from `.env.example` on first
`make e2e-up`. Edit it to point at an internal Docker registry mirror;
the auto-create rule preserves your edits across `make` invocations.

When `.env.example` gains new vars (image upgrades, new deps), refresh
your local `.env` once:

```bash
rm docker-local/e2e/.env && make e2e-up
```

## Layout

```
docker-local/e2e/
├── .env.example        # E2E_*_IMAGE refs, defaulted to upstream Docker Hub
├── compose.e2e.yaml    # 34-container stack (6 deps + 11 services per site)
├── nats/               # nats-{a,b}.conf  (NATS supercluster + gateway config)
├── setup-e2e.sh        # generates secrets/ on first run (nsc via nats-box)
├── secrets/            # generated; gitignored
│   ├── auth.conf       # operator + chatapp account JWTs (included by nats-{a,b}.conf)
│   ├── backend.creds   # all-services NATS creds
│   ├── e2e-secrets.env # AUTH_SIGNING_KEY for auth-service-{a,b}
│   └── operator.jwt / account.jwt / sys.jwt
└── README.md           # this file

e2e/                    # Go test package, gated //go:build e2e
├── doc.go              # package documentation
├── main_test.go        # TestMain: harness.Start + BootstrapFederation + var stack
├── smoke_test.go               # every dep reachable
├── helpers_test.go             # requestReply, sendAndAwaitReply, awaitDurableReady
├── message_send_test.go        # single-site happy path (channel + DM)
├── rooms_test.go               # DM idempotency, channel CRUD, MessageRead
├── search_test.go              # send + index + search roundtrip (skipped; user-room auth)
├── errors_test.go              # negative paths
├── request_id_test.go          # X-Request-ID propagation
├── federation_invite_test.go   # cross-site invite + negative-isolation
├── federation_catchup_test.go  # inbox-worker outage + drain
└── harness/            # stack lifecycle + per-site clients + per-test IDs
    ├── stack.go        # Start, Stop, ServiceContainer, preflight
    ├── auth.go         # Authenticate -> Keycloak -> auth-service -> NATS user JWT
    ├── clients.go      # SystemConn, MongoDB, CassandraSession, ESClient, SeedRemoteUser
    ├── federation.go   # BootstrapFederation: cross-site INBOX Sources
    ├── ids.go          # NewTestIDs (test-scoped resource identifiers)
    └── logs.go         # CaptureLogs (failure-only log dumps)
```

The scenarios + TestMain share one package so the singleton `stack` variable
is visible to both. Earlier iterations had a `scenarios/` subpackage; that
produced two separate test binaries with disjoint process state and was
collapsed during live debug.

## Host port allocation

Site-A on +10000 band, site-B on +20000. Mongo on +100/+200 to stay
below Linux's 32768 ephemeral range. `make e2e-up` refuses to start
when `make deps-up` is running -- dual-stack on one box risks OOM.

| Service     | Internal | Site-A | Site-B |
|-------------|----------|--------|--------|
| NATS client | 4222     | 14222  | 24222  |
| NATS HTTP   | 8222     | 18222  | 28222  |
| Mongo       | 27017    | 27117  | 27217  |
| Cassandra   | 9042     | 19042  | 29042  |
| ES          | 9200     | 19200  | 29200  |
| Valkey      | 6379     | 16379  | 26379  |
| Keycloak    | 8080     | 18180  | 28180  |
| auth-service| 8080     | 18080  | 28080  |

## Notes

- **Trust model deviation**: both sites share one operator + one chatapp
  account. A token minted on site-A is bit-identical and accepted by
  site-B's NATS. Acceptable for the test environment; not how
  production trust isolation works. Tests that need per-site token
  isolation realism must mock that boundary or wait for a follow-up
  plan with per-site operators.
- **host.docker.internal must resolve from the host**. Docker Desktop
  sets it up automatically. On plain Linux Docker Engine:
  `echo '127.0.0.1 host.docker.internal' | sudo tee -a /etc/hosts`.
  The harness preflight fails fast with a remediation hint if missing.
- **Federation wiring** is harness-owned at startup (see
  `e2e/harness/federation.go`). The chatapp account's INBOX_{site}
  Source is configured by `BootstrapFederation` after `harness.Start`
  succeeds; idempotent so re-running is safe.

For the full design, plan, and amendments:
- `docs/superpowers/plans/2026-05-08-e2e-tests.md`
- `docs/superpowers/plans/2026-05-08-e2e-tests-amendments.md`
