# Local Jaeger Setup for Tracing

**Date:** 2026-04-09
**Status:** Approved
**Approach:** Jaeger all-in-one via docker-compose at `tools/jaeger/`

## Summary

Add a standalone docker-compose file to run Jaeger locally for testing OpenTelemetry traces. All services already export traces via OTLP gRPC to `localhost:4317` (the default for `otlptracegrpc.New(ctx)`), so no code changes are needed — just start Jaeger and run any service.

## Decision

| Decision | Choice |
|----------|--------|
| Location | `tools/jaeger/` (follows `tools/nats-debug/` pattern) |
| Approach | Jaeger all-in-one via docker-compose (Approach 1) |
| Image | `jaegertracing/all-in-one:latest` |
| Storage | In-memory (default for all-in-one, traces lost on restart) |

## Files Created

| File | Purpose |
|------|---------|
| `tools/jaeger/docker-compose.yml` | Jaeger all-in-one container definition |
| `tools/jaeger/README.md` | Usage instructions for developers |

## Docker Compose Service

Single service `jaeger` using `jaegertracing/all-in-one:latest`:

| Port | Protocol | Purpose |
|------|----------|---------|
| `4317` | OTLP gRPC | Trace ingestion (services send here by default) |
| `4318` | OTLP HTTP | Alternative trace ingestion |
| `16686` | HTTP | Jaeger UI for viewing traces |

Environment variable `COLLECTOR_OTLP_ENABLED=true` enables the OTLP receiver.

## Usage Flow

1. `cd tools/jaeger && docker compose up -d`
2. Run any service locally (e.g., `go run ./broadcast-worker/`)
3. Exercise the service (send messages, etc.)
4. Open `http://localhost:16686` to view traces
5. Select the service name from the dropdown, click "Find Traces"

No `OTEL_EXPORTER_OTLP_ENDPOINT` configuration needed — services default to `localhost:4317`. To override, set `OTEL_EXPORTER_OTLP_ENDPOINT=host:port` before starting the service.

## No Code Changes

- `pkg/otelutil/otel.go` — `otlptracegrpc.New(ctx)` already reads `OTEL_EXPORTER_OTLP_ENDPOINT` env var and defaults to `localhost:4317`
- All 8 services already call `otelutil.InitTracer` at startup
- All services use `otelnats.Connect` / `oteljetstream.New` for traced NATS messaging
