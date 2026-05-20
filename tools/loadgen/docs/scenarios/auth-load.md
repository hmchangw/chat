# auth-load scenario

The auth-load scenario benchmarks `auth-service` under two distinct workloads:

1. **Normal mode** — sustained HTTP traffic against the auth REST surface.
2. **Reconnect-storm mode** — bulk NATS connection drops + immediate
   re-handshake, intended to characterise auth-service behaviour during a
   client-fleet recovery event (e.g., a NATS rolling restart or a
   network blip that dropped every client at once).

The two modes are mutually exclusive — `auth-load` runs in normal mode by
default and switches into reconnect-storm mode when the active preset is
`auth-reconnect-storm`.

## Normal mode

The tick loop runs at `--rate` rps (default 10 when unset). Each tick
alternates between two calls:

| Tick parity | Call                              | Recorded as                                    |
|-------------|-----------------------------------|------------------------------------------------|
| even (`n%2==0`) | `POST /auth` (dev-mode body)  | `loadgen_requests_total{kind="login"}`         |
| odd (`n%2==1`)  | `GET /healthz`                | `loadgen_requests_total{kind="validate"}`      |

Both calls are also observed into `loadgen_request_latency_seconds` and
on error into `loadgen_request_errors_total` with `reason="request"` (for
HTTP ≥ 400 or transport-layer failure).

**Concurrency.** Each tick spawns a single goroutine for its HTTP call so a
slow auth-service response does not back the ticker up; `wg.Wait()` on
shutdown guarantees no goroutine leaks past `ctx.Done`.

**Account selection.** Fixture users (`Fixtures.Users`) are round-robined
across ticks. When fixtures are empty the scenario falls back to the
literal account `"loadgen-anon"` so dev runs without a seed step still
produce traffic.

**NATS NKey.** One ed25519 keypair (`nkeys.CreateUser`) is generated per
generator instance and reused across every `/auth` call. Auth-service
signs whatever public key the client presents, so per-tick keygen would
add ed25519 cost to the measured loop without changing what's tested.

### Configuration

| Flag | Env | Default | Effect |
|------|-----|---------|--------|
| `--auth-url` | `AUTH_SERVICE_URL` | `http://auth-service:8080` | auth-service base URL |
| `--rate` | — | `10` | total auth+validate rps (split evenly) |
| `--auth-storm-period` | — | `0` | when `0`, normal mode; when `>0`, switches to storm mode periodic |

`--auth-url` takes precedence over the env. The default targets the
service name inside the loadgen Compose stack.

### SUT prerequisites

The scenario uses the auth-service **dev-mode** body shape
(`{"account": "...", "natsPublicKey": "..."}`) — auth-service must be
configured with `DEV_MODE=true` so it accepts the request without OIDC
validation. Running against prod-mode auth-service (with OIDC) will
produce 401s on every `/auth` call; this is a config issue, not a
scenario bug.

## Reconnect-storm mode

Triggered by `--preset=auth-reconnect-storm` (which sets
`AuthIdleConnections=1000` and `AuthStormPeriod=30s` by default).

A single "storm event" is:

1. Open `M` NATS connections (`AuthIdleConnections`).
2. Close all `M` at once.
3. Re-open `M` connections; observe wall-clock elapsed (`dropAt →
   last-recovered`) into `loadgen_auth_reconnect_seconds`.
4. Increment `loadgen_auth_reconnects_completed_total`.
5. Drop the rejoined cohort so the next event starts from zero.

The first event fires at `T + stormDelay` (default 30 s). When
`--auth-storm-period > 0` the loop continues with that interval; when
`0` (the default) the scenario exits after the first event.

### Endpoints touched

Reconnect-storm mode does **not** call any HTTP endpoint on
auth-service. The connection cost it measures is auth-service's NATS
callout latency — auth-service is the validator that signs/verifies the
NATS user JWT, so re-dialing M connections forces M JWT validations
through the callout subject.

The NATS URL is read from the running site's connection
(`Sites()[0].NC.NatsConn().ConnectedUrl()`); the scenario does not
require a separate `--auth-nats-url`.

## Endpoint audit (auth-service)

Audited 2026-05-18 against commit `dbde60b1` of `auth-service/`.

| Method | Path      | Body                                                              | Purpose                    |
|--------|-----------|-------------------------------------------------------------------|----------------------------|
| POST   | /auth     | `{"ssoToken":"...","natsPublicKey":"..."}`                        | Issue NATS JWT (prod mode) |
| POST   | /auth     | `{"account":"...","natsPublicKey":"..."}` (DEV_MODE=true)        | Issue NATS JWT (dev mode)  |
| GET    | /healthz  | (none)                                                            | Health check               |

Notes on the spec vs reality:

- The original spec listed `POST /login`, `POST /refresh`, `GET /validate`
  as the expected endpoint set. auth-service does **not** have any of
  these. The single auth endpoint is `POST /auth`, which both issues and
  implicitly validates.
- Auth-service issues NATS JWTs (not HTTP Bearer JWTs); the "refresh"
  concept does not exist — clients hold a signed NATS credential and
  re-authenticate via `POST /auth` when it nears expiry.
- `/healthz` is the closest available stand-in for a "validate" probe.
  Full NATS-JWT validation is SUT-internal (NATS server enforces the
  signed claims directly), not a REST endpoint.

`doAuthLogin` → `POST /auth` (dev mode).
`doAuthValidate` → `GET /healthz`.
`doAuthRefresh` → `POST /auth` (re-auth — same wire path as login).
