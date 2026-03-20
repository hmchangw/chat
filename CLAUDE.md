# Project Guidelines

## Section 7: API & HTTP Standards

### HTTP Server

- **Framework**: Use the [Gin](https://github.com/gin-gonic/gin) library for all HTTP servers. Do not use `net/http` mux directly.
- **Router**: Register all routes in a dedicated `routes.go` file — never in `main.go`. The `routes.go` file should expose a function (e.g., `SetupRoutes(r *gin.Engine)`) that `main.go` calls.
- **Request/Response format**: All APIs use JSON. Define request and response structs explicitly — no `map[string]interface{}`.
- **Validation**: Validate all incoming request bodies at the handler level using Gin's binding/validation before passing to the service layer. Return `400 Bad Request` with a descriptive error message for invalid input.
- **Error responses**: Use a consistent error response format across all services: `{"error": "human readable message"}`. Map domain errors to appropriate HTTP status codes in the handler — don't leak internal details.
- **Middleware**: Chain middleware where the code is cleanest and most maintainable. For global middleware (logging, panic recovery, CORS), attach in `routes.go` alongside route registration. For route-group-specific middleware (auth, rate limiting), attach on the relevant `gin.RouterGroup`. Keep `main.go` focused on server lifecycle, not middleware wiring.
- **Health checks**: Every HTTP service exposes `GET /healthz` that returns `200 OK` when the service is ready to accept traffic.
- **Timeouts**: Set read/write timeouts on `http.Server` wrapping the Gin engine. Use `context.WithTimeout` for downstream calls (DB, NATS, external APIs) within handlers.
- **Context propagation**: Extract request-scoped values (user ID, trace ID) into `context.Context` in middleware and pass through to service and store layers.

### Outbound HTTP Calls

- **Library**: Use [Resty](https://github.com/go-resty/resty) for all outbound HTTP calls to third-party dependencies. Do not use `net/http` client directly for external calls.
- **Timeouts**: Always set a request timeout on the Resty client to protect against long waits. Example:
  ```go
  client := resty.New().SetTimeout(10 * time.Second)
  ```
- **Testing**: Use Resty's recommended testing framework (`httpmock` via `jarcoal/httpmock` or Resty's built-in test utilities) for unit testing outbound HTTP calls. Mock external dependencies — never make real HTTP calls in unit tests.
- **Error handling**: Always check both the error return and the HTTP status code from Resty responses. Wrap errors with context before returning.

## Section 8: Logging & Observability

### Logging

- **Library**: Use `log/slog` (Go standard library structured logging). No third-party logging libraries.
- **Initialization**: Initialize the default logger in `main.go` using `slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))`. Other files use `slog` directly (e.g., `slog.Info(...)`) — no need to pass the logger via dependency injection.
- **Format**: Always use JSON output. Never use text format, even in development.
- **Log levels**: Use `slog.Info` for normal operations, `slog.Error` for failures that need attention, `slog.Debug` for development diagnostics. Never log at `Warn` unless there's a degraded-but-recoverable situation.
- **Structured fields**: Always attach context as key-value pairs, not interpolated strings. Example: `slog.Error("failed to send message", "room_id", roomID, "err", err)` — not `slog.Error(fmt.Sprintf("failed to send message to room %s: %v", roomID, err))`.
- **Sensitive data**: Never log tokens, passwords, or full message bodies. Log IDs and metadata only.

### Request Logging & Tracing

- **Request logging**: For HTTP services (Gin), use a middleware that logs method, path, status code, latency, and request ID on every request.
- **Trace/Request ID**: Generate or extract a unique request/correlation ID at the entry point (HTTP middleware or NATS message handler) and propagate it through `context.Context`. Include it in all log lines.

### Metrics

- **Library**: If metrics are needed, use Prometheus client library (`prometheus/client_golang`). Expose metrics on a separate port via `/metrics` endpoint.

## Section 9: Configuration & Environment

- **Source**: All configuration comes from environment variables. No config files (YAML, TOML, JSON) unless there's a strong reason.
- **Loading**: Read environment variables in `main.go` at startup. Parse them into a typed `Config` struct. Pass the struct (or its fields) to constructors — services and handlers never read `os.Getenv` directly.
- **Validation**: Validate all required config at startup. If a required variable is missing or invalid, log the error and exit immediately with a non-zero exit code. Fail fast — don't let the service start in a broken state.
- **Naming**: Use `SCREAMING_SNAKE_CASE` for environment variable names. Prefix with the service name for service-specific vars (e.g., `AUTH_SERVICE_PORT`, `HISTORY_SERVICE_CASSANDRA_HOSTS`). Use unprefixed names for shared vars (e.g., `NATS_URL`, `MONGO_URI`).
- **Defaults**: Provide sensible defaults for non-critical config (e.g., port, log level). Never default secrets or connection strings — require them explicitly.
- **Secrets**: Secrets (tokens, keys, passwords) come from environment variables. Never hardcode them. Never log them. In production, inject via the orchestrator's secret management (e.g., Kubernetes Secrets).
- **`.env` files**: Use `.env` files for local development only. Add `.env` to `.gitignore`. Never commit `.env` files.
