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
