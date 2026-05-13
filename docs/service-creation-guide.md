# Creating a new service

This guide covers everything `tools/scaffold-service` cannot decide for you. The
scaffolder emits a minimal Gin HTTP service skeleton with `/healthz`, a Store
interface stub, a mockgen directive, a handler smoke test, and `deploy/`
artifacts. From there, the choices below are domain-specific.

## 1. Run the scaffolder

```bash
go run ./tools/scaffold-service -name presence-service -root .
go mod tidy
go test ./presence-service/
go build ./presence-service/
```

The name must match `^[a-z][a-z0-9-]{1,38}[a-z0-9]$` (lowercase, hyphenated, no
underscores) per the repo conventions in `CLAUDE.md`.

## 2. Pick a shape

The scaffolder produces a Gin HTTP service because that's the simplest of the
three shapes in this repo. If your service is one of the other shapes, plan to
swap before adding logic:

| Shape | When to use | Reference services |
|---|---|---|
| **HTTP (Gin)** | Client-facing endpoints, healthchecks, OIDC token exchange. | `auth-service`, `search-service` |
| **NATS request/reply** | Synchronous RPC over NATS subjects (`chat.user.{account}.…` or `chat.server.…`). | `room-service`, `history-service`, `search-service` (registers via `natsrouter`) |
| **JetStream consumer (worker)** | Async event processing with at-least-once semantics; ACK/NAK/TERM. | `broadcast-worker`, `inbox-worker`, `message-worker`, `search-sync-worker` |

For workers, port `main.go` over by copying `pkg/shutdown.Wait` plus
`iter.Stop()` → `wg.Wait()` → `nc.Drain()` ordering from a reference. See
`CLAUDE.md` "JetStream Consumer Pattern" and "Graceful Shutdown".

## 3. Wire up persistence

The scaffolder gives you `store.go` with a `Ping(ctx) error` placeholder. Choose:

- **MongoDB** (rooms, subscriptions, messages, profiles, etc.): use the
  `pkg/mongoutil` helper. See `room-service/store_mongo.go` for the canonical
  pattern (collection per domain entity, app-generated IDs via `pkg/idgen`,
  `bson:"_id"` mapped to the model's `ID` field, indexes created in the store
  constructor).
- **Cassandra** (message history only): `pkg/cassutil`. See
  `message-worker/store_cassandra.go`. Schema lives in
  `docker-local/cassandra/init/` and is mirrored in
  `docs/cassandra_message_model.md`; any change touches all three together.
- **Elasticsearch** (full-text search): use `pkg/searchengine`. See
  `search-service/store_es.go` and `search-sync-worker/messages.go`.
- **Valkey** (Redis-compatible cache, room keystore): `pkg/valkeyutil`.
- **No persistence**: delete `store.go` and drop the `store` field on `Handler`.

Always regenerate the mock after changing the interface:

```bash
make generate SERVICE=presence-service
```

## 4. Stream bootstrap (JetStream consumers only)

If your service reads from or writes to a JetStream stream, follow the opt-in
bootstrap convention in `CLAUDE.md` "JetStream Streams":

- Add a `Bootstrap bootstrapConfig` block to your `config` struct with prefix
  `BOOTSTRAP_` and an `Enabled bool env:"STREAMS" envDefault:"false"` field.
- Add a `bootstrap.go` with `bootstrapStreams(ctx, js, siteID, enabled) error`.
- Use `js.CreateOrUpdateStream` and **preserve** any existing `Sources` from a
  live `StreamInfo`; do not author federation config. See
  `inbox-worker/bootstrap.go` for the reference implementation.
- Set `BOOTSTRAP_STREAMS=true` in your local `deploy/docker-compose.yml` only;
  production gates it off and lets ops/IaC own provisioning.

## 5. Observability

The scaffolder ships zero telemetry. Add only what you need:

- **Logs**: `slog` with the default JSON handler is already configured in
  `main.go`. Use structured key-value pairs; never interpolate. Never log
  tokens, passwords, or full message bodies.
- **Tracing**: import `pkg/otelutil` and call `otelutil.InitTracer(ctx,
  serviceName)` early in `run()`. Defer the returned shutdown func.
- **Metrics**: see `search-service/metrics.go` for the
  `:9090/metrics` listener pattern; bind synchronously so a port conflict fails
  startup loudly.
- **Request IDs**: HTTP — install `natsrouter.RequestID()` middleware (NATS
  router only) or the Gin middleware in `pkg/httpmw` (HTTP services). For NATS
  workers, `pkg/natsutil.WithRequestID(ctx, ...)` carries the inbound header.

## 6. Update the Makefile and CI

- The root `Makefile` discovers services by directory; no edit needed if your
  service follows the flat layout.
- The scaffolder writes a `deploy/azure-pipelines.yml` with a trigger on
  `<service>/**` plus `pkg/**`. Edit the YAML if your service has additional
  trigger paths (e.g., a shared template repo).

## 7. Client-facing handlers

If your service exposes any handler on `chat.user.{account}.request.…` or
similar client-facing subjects, **update `docs/client-api.md` in the same PR**
with the new request/response schema, error cases, and any triggered events.
This is enforced by `CLAUDE.md` ("Before Committing").

## 8. Testing

Default everything to the TDD cycle from `CLAUDE.md` Section 4:

1. Write tests first (`*_test.go`), confirm they fail.
2. Write the minimum implementation to make them pass.
3. Refactor with tests green.
4. Run with the race detector: `make test SERVICE=presence-service`.

Coverage target is 80% minimum / 90% for core business logic (handlers, stores,
shared `pkg/`). Use `go tool cover -func=coverage.out` to verify.

Integration tests get a `//go:build integration` tag and `testcontainers-go`
real-dependency containers — see `message-worker/integration_test.go`.

## 9. What the scaffolder intentionally does NOT do

It does NOT:

- Add the service to the root `docker-local/docker-compose.yml`. Do that
  manually if your service is part of the local dev stack.
- Wire up Keycloak / OIDC / auth — that's `auth-service` territory.
- Pick a port that doesn't conflict with the rest of the stack. The default
  template ships `:8080`; pick a free one before adding to compose.
- Add OTel propagation middleware. Add it explicitly when the service
  participates in cross-service traces.
