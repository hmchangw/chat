# Optional Stream Bootstrap

## Problem

Seven services unconditionally call `js.CreateOrUpdateStream` at startup:

| Service | Stream(s) created |
|---|---|
| `message-gatekeeper` | `MESSAGES`, `MESSAGES_CANONICAL` |
| `broadcast-worker` | `MESSAGES_CANONICAL` |
| `message-worker` | `MESSAGES_CANONICAL` |
| `notification-worker` | `MESSAGES_CANONICAL` |
| `room-worker` | `ROOMS` |
| `inbox-worker` | `INBOX` |
| `room-service` | `ROOMS` |

In production, JetStream streams are pre-provisioned by ops/IaC. Services should not attempt to create or modify them — only the consumers they own. Today there is no way to disable stream creation per service.

`search-sync-worker` already established a convention for this: a nested `bootstrapConfig` struct gated by `BOOTSTRAP_STREAMS` (default `false`). Extend that convention to the seven services above.

## Goals

- Local dev (`docker-compose` against the local NATS container) continues to create streams automatically so a developer can stand up any service in isolation.
- Production deployments do not call `CreateOrUpdateStream` at all. Services only create their own consumers.
- Default behavior is "do not bootstrap" — safe for prod, opt-in for dev.
- Pattern matches `search-sync-worker` exactly so the codebase has a single convention.

## Non-Goals

- Removing the redundant `MESSAGES_CANONICAL` creations in `broadcast-worker`, `message-worker`, `notification-worker` (and similarly the duplicate `ROOMS` between `room-service` and `room-worker`). Gating preserves current "any service starts standalone in dev" behavior. A future cleanup can collapse to owner-only bootstrap, matching `search-sync-worker`'s stated philosophy.
- Changing how `search-sync-worker` works — it already has the pattern.
- Pre-flight checks that warn if a stream is missing before consumer creation. If `BOOTSTRAP_STREAMS=false` and the stream doesn't exist, `CreateOrUpdateConsumer` will fail at startup with the underlying NATS error. That fail-fast behavior is desired — it surfaces a missing IaC provision step.

## Design

### Config struct

Each of the seven services adds the following to its `config` (in `main.go`):

```go
// bootstrapConfig groups every field that is ONLY meaningful when the
// service is being stood up in dev or integration tests against a NATS
// instance where the streams it consumes do not yet exist. In
// production streams are pre-provisioned by ops/IaC and Bootstrap.Enabled
// must remain false; the service only creates its own durable consumer.
type bootstrapConfig struct {
    // Enabled (BOOTSTRAP_STREAMS) toggles whether the service calls
    // CreateOrUpdateStream at startup for the streams it consumes.
    // Leave false in production.
    Enabled bool `env:"STREAMS" envDefault:"false"`
}

type config struct {
    // ...existing fields...
    Bootstrap bootstrapConfig `envPrefix:"BOOTSTRAP_"`
}
```

Env var: `BOOTSTRAP_STREAMS`. Default: `false`.

### Gated stream creation

Each existing `js.CreateOrUpdateStream` call is wrapped in a check on `cfg.Bootstrap.Enabled`. To make the gate independently unit-testable, extract a small helper per service:

```go
// bootstrapStreams creates the streams this service consumes from.
// In production this is a no-op (cfg.Bootstrap.Enabled is false) — streams
// are owned by ops/IaC. In dev/integration the local docker-compose sets
// BOOTSTRAP_STREAMS=true so a developer can stand the service up in
// isolation against a fresh NATS instance.
func bootstrapStreams(ctx context.Context, js streamCreator, siteID string, enabled bool) error {
    if !enabled {
        return nil
    }
    canonicalCfg := stream.MessagesCanonical(siteID)
    if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
        Name:     canonicalCfg.Name,
        Subjects: canonicalCfg.Subjects,
    }); err != nil {
        return fmt.Errorf("create MESSAGES_CANONICAL stream: %w", err)
    }
    return nil
}

// streamCreator is the minimal interface the helper depends on, kept
// service-local so we don't pollute pkg/ with a one-method type.
type streamCreator interface {
    CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error)
}
```

`main.go` calls the helper before consumer creation:

```go
if err := bootstrapStreams(ctx, js, cfg.SiteID, cfg.Bootstrap.Enabled); err != nil {
    slog.Error("bootstrap streams failed", "error", err)
    os.Exit(1)
}

cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{ ... })
```

The shape of `bootstrapStreams` varies per service:

- `message-gatekeeper` creates two streams (`MESSAGES`, `MESSAGES_CANONICAL`) — both gated together.
- `broadcast-worker`, `message-worker`, `notification-worker` each create `MESSAGES_CANONICAL`.
- `room-worker`, `room-service` each create `ROOMS`.
- `inbox-worker` creates `INBOX`.

Each helper lives in the same `package main` as the service and uses the service-local `streamCreator` interface. This keeps the test seam minimal and avoids any new shared package.

### Consumer creation is unaffected

`CreateOrUpdateConsumer` calls remain unconditional. A service always owns its consumer regardless of who owns the stream.

### Local dev — docker-compose

Each of the seven services has `deploy/docker-compose.yml`. Add `BOOTSTRAP_STREAMS=true` to the service's `environment:` block:

```yaml
services:
  <service>:
    environment:
      # ...existing env vars...
      BOOTSTRAP_STREAMS: "true"
```

Production manifests stay unchanged — the absent env var means default `false`.

### Tests (TDD)

Per CLAUDE.md, every change follows Red-Green-Refactor.

For each of the seven services, add a unit test in `main_test.go` (creating the file if it does not exist) that table-tests the new helper:

```go
func TestBootstrapStreams(t *testing.T) {
    tests := []struct {
        name        string
        enabled     bool
        wantCreated []string  // stream names expected to be created
    }{
        {"disabled - skips creation", false, nil},
        {"enabled - creates expected streams", true, []string{"MESSAGES_CANONICAL_test"}},
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            fake := &fakeStreamCreator{}
            err := bootstrapStreams(t.Context(), fake, "test", tc.enabled)
            require.NoError(t, err)
            require.Equal(t, tc.wantCreated, fake.created)
        })
    }
}
```

The fake `streamCreator` records which stream names it was asked to create. No mockgen-generated mock is needed — the interface has one method and the helper is service-local.

For `bootstrapStreams` calls that fail, add an additional table case where the fake returns an error and assert the helper wraps it with `fmt.Errorf("create <STREAM> stream: %w", err)`.

Coverage target per CLAUDE.md: ≥80% for the helper. The helper has only two paths (enabled false, enabled true with each stream) plus error wrapping, so reaching the threshold is straightforward.

### Doc updates

Add a short subsection to `CLAUDE.md` under "JetStream Streams":

> **Stream bootstrap is opt-in.** Services that consume from a stream do NOT create it in production — streams are owned by ops/IaC. Each service's `config` includes `Bootstrap bootstrapConfig` with `BOOTSTRAP_STREAMS` (default `false`). Local docker-compose files set it to `true` so any service can stand up against a fresh NATS in dev. New services that consume from JetStream MUST follow this convention.

## Migration

No data migration. The change is config-only. Rollout per service:

1. Land code change with default `false`.
2. Verify prod manifests do not set `BOOTSTRAP_STREAMS=true` (they shouldn't, since they don't reference it today). Streams are already provisioned in prod, so the effective behavior shift is "service no longer touches the stream config" — strictly safer.
3. Local dev compose files are updated in the same PR so `make` workflows continue to function.

## Open Questions

None. Decisions confirmed:

- `room-service` is in scope (publisher-only, but same prod concern).
- Test strategy is unit-only via the extracted helper; integration tests already cover the `Enabled=true` end-to-end path indirectly.
- Existing redundant stream creations (`MESSAGES_CANONICAL` in three workers, `ROOMS` in two services) are preserved and gated; collapsing to owner-only bootstrap is a future cleanup.
