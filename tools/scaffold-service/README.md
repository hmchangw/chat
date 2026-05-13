# scaffold-service

Emits the minimal flat-package-main service layout described in `CLAUDE.md`
Section 6: an HTTP (Gin) service skeleton with `/healthz`, a Store interface
stub + mockgen directive, a handler smoke test, and `deploy/Dockerfile +
docker-compose.yml + azure-pipelines.yml`.

The output builds and tests pass out of the box. From there, see
[`docs/service-creation-guide.md`](../../docs/service-creation-guide.md) for
the decisions the scaffolder cannot make (persistence backend, NATS vs HTTP,
observability wiring, bootstrap stream ownership, etc.).

## Usage

```bash
go run ./tools/scaffold-service -name presence-service -root .
go mod tidy
go test ./presence-service/
go build ./presence-service/
```

Flags:

- `-name` (required): service directory name. Must match
  `^[a-z][a-z0-9-]{1,38}[a-z0-9]$` — lowercase, hyphenated, no underscores.
- `-root` (default `.`): repo root.
- `-force`: overwrite an existing service directory.

## What it produces

```
<name>/
├── main.go              # config + Gin server + graceful shutdown
├── handler.go           # *Handler with /healthz
├── routes.go            # registerRoutes
├── store.go             # Store interface + mockgen directive
├── handler_test.go      # /healthz smoke test
└── deploy/
    ├── Dockerfile           # multi-stage golang:1.25.8-alpine → alpine:3.21
    ├── docker-compose.yml   # single-service compose
    └── azure-pipelines.yml  # path-triggered CI skeleton
```

## What it intentionally does NOT do

See `docs/service-creation-guide.md` — the scaffolder is deliberately minimal
so domain-specific choices (persistence backend, NATS vs HTTP, observability,
JetStream bootstrap, port allocation) stay in the developer's hands.
