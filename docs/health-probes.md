# Health Probes

Every service exposes Kubernetes-style liveness and readiness probes over HTTP,
served by `pkg/health`.

## Endpoints

| Path       | Meaning   | Behavior |
|------------|-----------|----------|
| `/healthz` | Liveness  | Always `200 {"status":"ok"}` while the process runs. It never probes dependencies — a dependency outage must not restart the pod. |
| `/readyz`  | Readiness | Reports whether this pod is connected to NATS. `200` when `CONNECTED` or `RECONNECTING`; `503` once the connection is `DISCONNECTED`/`CLOSED`. |

## Where they listen

| Service | Probe port | Notes |
|---------|-----------|-------|
| `auth-service` | `PORT` (default `8080`) | On the main Gin server. |
| `search-service` | `SEARCH_METRICS_ADDR` (default `:9090`) | Mounted on the existing metrics listener — no extra port. |
| all other (NATS) services | `HEALTH_ADDR` (default `:8081`) | A dedicated health-only listener. One port per pod, so the shared default does not collide. |

## What readiness checks — and why only NATS

Readiness probes **only the pod's own NATS connection**, not the shared
datastores (Mongo, Cassandra, Elasticsearch). This is deliberate:

- **Readiness should reflect per-pod serve-ability, not backend health.** A
  shared database is the same for every replica, so probing it in readiness means
  a brief DB blip flips *every* pod `NotReady` at once. For an HTTP service that
  removes all endpoints (clients get connection-refused instead of a clean
  `503`); for the NATS workers it gates nothing useful and risks correlated
  rollout/PDB churn. The application returns proper `errcode` errors when a
  datastore is down — that's the right failure mode, not yanking pods.
- **The NATS connection is genuinely per-pod.** If *this* pod loses its NATS
  connection while siblings are fine, `NotReady` correctly reflects that it can't
  do work. A brief reconnect is tolerated (`RECONNECTING` stays ready) so
  readiness doesn't flap; a sustained disconnect reports `NotReady`.

Most services receive work over NATS, not an HTTP Service, so readiness here is
primarily a rollout-gating and operator signal — and a safe one, since nothing is
routed off it.

The NATS readiness check is `natsutil.HealthCheck(nc)`.

## Liveness

Liveness is process-up only. (A consume-loop heartbeat — failing liveness when a
worker's pull loop wedges while the process stays alive — is the natural next
addition, since that is the failure neither current probe catches.)

`HEALTH_ADDR` is a standard `caarlos0/env` var; override per deployment if
`:8081` clashes with another container port.
