# TODO

Potential work items across the codebase, grouped by priority.

---

## Critical

### SSO Token Verifier is a stub
- **Service:** auth-service
- **Files:** `auth-service/main.go:57,82-88`
- **Context:** `SSOTokenVerifier.Verify()` always returns `"SSO token verification not implemented"`. The auth callout flow is fully wired — NATS delegates to auth-service, JWT issuance and permission scoping works — but no real identity provider is connected. Every client connection will be rejected until this is implemented.
- **Action:** Implement `TokenVerifier` against an OAuth2/OIDC provider (token introspection endpoint or local JWT validation with JWKS).

---

## High

### Missing MongoDB indexes
- **Services:** all services using MongoDB (message-worker, broadcast-worker, notification-worker, room-service, room-worker, history-service, inbox-worker)
- **Context:** No `EnsureIndexes` calls in any store constructor. The `subscriptions` collection is queried by `{userId, roomId}` (every message, every history request) and by `{roomId}` alone (broadcast, notification fan-out). The `rooms` collection is queried by `_id` (indexed by default) and `siteId`. Without compound indexes, these queries do collection scans.
- **Action:** Add `EnsureIndexes()` to each store constructor:
  - `subscriptions`: compound index on `{userId, roomId}` (unique), index on `{roomId}`
  - `rooms`: index on `{siteId}` if site-scoped queries exist

### No OpenTelemetry tracing in any service
- **Services:** all 8 services
- **Files:** `pkg/otelutil/otel.go` exists with `InitTracer()` and `InitMeter()` but is never imported
- **Context:** The tracing utility package is built and ready. No service calls it. Without traces, there is no visibility into cross-service request flow (e.g., message publish → message-worker → broadcast-worker → client delivery).
- **Action:** Call `otelutil.InitTracer()` in each service's `main.go`, create spans in handler methods, propagate trace context through NATS headers using `natsutil.HeaderCarrier`.

### No Prometheus metrics
- **Services:** all 8 services
- **Context:** No counters, histograms, or gauges anywhere. Cannot measure message throughput, processing latency, error rates, or queue depth.
- **Action:** Add metrics to handlers — at minimum: messages processed (counter), processing duration (histogram), errors by type (counter). Expose `/metrics` endpoint or use OpenTelemetry metrics exporter.

### Silent failures on critical write paths
- **Files:**
  - `room-service/handler.go:121` — owner subscription creation failure logged as warning; room exists but creator can't access it
  - `room-worker/handler.go:59` — `IncrementUserCount` failure logged as warning; room user count drifts from reality
  - `message-worker/handler.go:94` — `UpdateRoomLastMessage` failure logged as warning; room metadata goes stale
- **Context:** These are logged with `slog.Warn` but the handler continues as if nothing happened. For the room-service case, this creates an orphan room. For room-worker, the count becomes permanently wrong.
- **Action:** Decide per case: return error (and NAK the JetStream message for retry), or implement a compensating action. At minimum, the room-service owner subscription failure should fail the create-room request.

---

## Medium

### Double JSON unmarshal in message-worker
- **File:** `message-worker/handler.go:114-120`
- **Context:** `getRequestID()` unmarshals the full `SendMessageRequest` just to extract `RequestID`. The same payload was already unmarshaled in `processMessage()` at line 69. This runs on every message.
- **Action:** Refactor `HandleJetStreamMsg` to unmarshal once and pass the parsed request (or the extracted `RequestID`) into `processMessage`.

### Missing NATS integration tests
- **Services:** message-worker, broadcast-worker, notification-worker, room-worker, inbox-worker
- **Context:** Integration tests use testcontainers for MongoDB/Cassandra but not NATS. Handler tests mock the store but never test real JetStream consumption, acknowledgment, or redelivery behavior.
- **Action:** Add NATS testcontainer to integration tests. Verify: message consumption, ACK/NAK behavior, consumer durable state, and dedup via `Nats-Msg-Id`.

### Notification dedup on JetStream redelivery
- **Service:** notification-worker
- **File:** `notification-worker/handler.go`
- **Context:** If a JetStream message is redelivered (NAK, timeout, crash recovery), notification-worker will send duplicate notifications to all room members. There is no dedup mechanism — no message ID tracking, no idempotency check.
- **Action:** Either track processed message IDs (in-memory with TTL or in MongoDB), or accept at-least-once delivery and handle dedup client-side.

### Broadcast/notification publish failures are silent
- **Files:**
  - `broadcast-worker/handler.go:88` — DM room per-user publish failures only logged
  - `notification-worker/handler.go:65` — per-user notification failures only logged
  - `room-worker/handler.go:98` — per-member metadata update failures only logged
- **Context:** When publishing to individual users in a loop, errors are logged but the handler ACKs the message as successfully processed. If NATS is under pressure, some users silently miss messages/notifications with no retry.
- **Action:** Collect errors; if any publish fails, NAK the JetStream message for retry (with backoff). Or implement a dead-letter mechanism for failed deliveries.

### Room-service input validation gaps
- **File:** `room-service/handler.go:88-125`
- **Context:** `handleCreateRoom` does not validate room name (empty string allowed), room type (any string accepted), or member list. Invalid rooms can be persisted to MongoDB.
- **Action:** Validate: name non-empty and length-bounded, type is `"group"` or `"dm"`, members list is non-empty for DM rooms.

### Room-worker test coverage is minimal
- **Service:** room-worker
- **File:** `room-worker/handler_test.go`
- **Context:** Only 1 unit test. Handler has multiple code paths: local invite, cross-site invite (OUTBOX publish), subscription creation, user count increment, metadata broadcast to all members. Most paths are untested.
- **Action:** Add table-driven tests covering: successful local invite, cross-site invite (verify OUTBOX publish), duplicate invite, store errors, empty room edge case.

### Message-worker test coverage is thin
- **Service:** message-worker
- **File:** `message-worker/handler_test.go`
- **Context:** 2 unit tests (happy path + not subscribed). Missing: invalid JSON input, `SaveMessage` error, `UpdateRoomLastMessage` error, fanout publish error, empty content, missing `RequestID` (no reply sent).
- **Action:** Add table-driven tests for all error paths and edge cases.

---

## Low

### History service hardcoded default limit
- **File:** `history-service/handler.go:71`
- **Context:** Default pagination limit is hardcoded to `50`. Not configurable per deployment.
- **Action:** Add `DEFAULT_HISTORY_LIMIT` to the service config struct with `envDefault:"50"`.

### Auth-service incomplete shutdown
- **File:** `auth-service/main.go:70-76`
- **Context:** Shutdown calls `svc.Stop()` but does not explicitly drain or close the NATS connection. Other services call `nc.Drain()` during shutdown. The `defer nc.Close()` on line 55 will eventually fire, but ordering relative to `svc.Stop()` is not guaranteed.
- **Action:** Add `nc.Drain()` to the shutdown sequence after `svc.Stop()`, matching the pattern used by worker services.

### No rate limiting on broadcast fan-out
- **Service:** broadcast-worker
- **File:** `broadcast-worker/handler.go`
- **Context:** For DM rooms, publishes to each member sequentially. For a theoretical high-member room (if type check is bypassed), this could create a publish storm. Currently mitigated by the semaphore in `main.go` limiting concurrent handler invocations.
- **Action:** Consider batching publishes or adding per-room rate limiting if room sizes grow significantly. Low priority given the `MaxWorkers` semaphore already bounds concurrency.

### Kubernetes deployment manifests missing
- **Context:** Each service has `deploy/Dockerfile`, `deploy/docker-compose.yml`, and `deploy/azure-pipelines.yml`. No Helm charts or Kubernetes manifests exist. Docker Compose is local-dev only.
- **Action:** Create Helm chart or Kustomize manifests when moving to production Kubernetes deployment. Include: resource limits, health probes (`/healthz`), configmaps for env vars, secrets for signing keys and connection strings.
