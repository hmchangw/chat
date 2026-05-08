# Error Format Standard

> This document defines the **target** error response format for the chat backend. It supersedes the envelope described in [`client-api.md` §5](./client-api.md#5-error-envelope-reference) once the migration lands. For the current shipping shape (`{ "error": "..." }` only), see that section.

## Overview

The chat backend has three transports that can surface errors to a caller:

1. **HTTP** — `auth-service` (`POST /auth`).
2. **NATS request/reply** — `room-service`, `history-service`, `search-service`, and the `message-gatekeeper` reply path.
3. **NATS async-job-result** — `room-worker` publishes `model.AsyncJobResult` to `chat.user.{account}.response.{requestID}`.

JetStream worker services that consume events without a reply path (`message-worker`, `broadcast-worker`, `notification-worker`, `inbox-worker`, `search-sync-worker`) have no client-visible error surface — failures are logged and decided by Ack/Nak. They are out of scope for the wire format below; the same `code` strings apply when those errors are logged or recorded for operator inspection.

## Goals

- One JSON envelope across HTTP, NATS request/reply, and `AsyncJobResult.Error`.
- Programmatic discrimination via a stable, machine-readable `code` — clients never parse human text.
- A category vocabulary borrowed from gRPC's canonical status codes, trimmed to what this codebase actually produces. Familiar to engineers, with documented HTTP mappings, and aligned with OpenTelemetry span statuses.
- Backward-compatible field name (`error`) so the existing client parser keeps working during rollout.
- Structured `details` for context that today is bolted on as one-off fields (e.g. the `roomId` field on `dm already exists`).

## Non-goals

The following are surfaced by the survey that produced this spec but are **not** part of this standard. They are tracked separately:

- Internal Go error model (`pkg/errs` package, typed errors carrying `(category, reason, details)`).
- JetStream Ack/Nak policy unification (today: three different `errPermanent` mechanisms across `room-worker`, `inbox-worker`, `message-gatekeeper`).
- Structured logging field-key conventions (today: `roomID`/`roomId`, `siteID`/`site_id`, `requestID`/`request_id` drift).
- Dead-letter queue stream design.
- `message-gatekeeper` reply-on-infrastructure-error fix (clients currently see a silent timeout on transient errors).

These either build on this spec or are orthogonal; addressing them is sequenced after this format is agreed.

## The envelope

```json
{
  "code": "permission_denied.not_room_member",
  "error": "you are not a member of this room",
  "requestId": "01970a4f-8c2d-7c9a-abcd-e0123456789f",
  "details": { "roomId": "abc123xyz0000def4" }
}
```

| Field       | Type     | Required | Notes |
|-------------|----------|----------|-------|
| `code`      | string   | yes      | Two-level dot-separated identifier: `<category>.<reason>`. Stable contract. |
| `error`     | string   | yes      | Human-readable message, sanitized at the service boundary. **Never** parsed by clients. Field name preserved from today's envelope for compatibility. |
| `requestId` | string   | yes      | 36-char hyphenated UUID. Echoed from the inbound `X-Request-ID` header, or generated server-side via `idgen.GenerateRequestID()`. Allows clients and support to correlate to server logs and traces. |
| `details`   | object   | no       | Service-specific structured context. Schema documented per `code`. Omitted when no structured context applies. |

The envelope is the same JSON for all three transports. HTTP additionally carries a status code on the response line (see [§HTTP status mapping](#http-status-mapping)).

## Code naming: `category.reason`

A `code` is two segments joined by a single dot. Both segments are `snake_case`.

- **Category** — one of the values in [§Categories](#categories). Picks the broad class of failure and, transitively, the HTTP status and retryability hint.
- **Reason** — a service- or domain-specific identifier for the specific failure within that category. Stable once shipped.

Examples grounded in real triggers from existing services:

| Today's behavior                                                                  | New `code`                                  |
|-----------------------------------------------------------------------------------|---------------------------------------------|
| `room-service` `errOnlyOwners` raw string                                         | `permission_denied.not_room_owner`          |
| `room-service` `errCannotDemoteLast` raw string                                   | `failed_precondition.last_owner_demote`     |
| `room-service` `dmExistsError` (with one-off `roomId` field on `ErrorResponse`)   | `already_exists.dm` + `details.roomId`      |
| `room-service` `errChannelNameTooLong` raw string                                 | `invalid_argument.channel_name_too_long`    |
| `history-service` `"not subscribed to room"`                                      | `permission_denied.not_room_member`         |
| `history-service` `"thread is outside access window"`                             | `failed_precondition.access_window_expired` |
| `history-service` `"message not found"`                                           | `not_found.message`                         |
| `search-service` `"search backend unavailable"`                                   | `unavailable.search_backend`                |
| `auth-service` `"SSO token has expired, please re-login"`                         | `unauthenticated.sso_token_expired`         |
| `auth-service` `"failed to generate NATS token"`                                  | `internal.jwt_signing`                      |
| `message-gatekeeper` `"user %s is not subscribed to room %s"`                     | `permission_denied.not_room_member`         |
| `message-gatekeeper` `"content exceeds maximum size of %d bytes"`                 | `invalid_argument.content_too_large`        |

Reasons are owned by the service that produces them. Two services may legitimately emit the same `code` when they detect the same failure (e.g. both `history-service` and `message-gatekeeper` emit `permission_denied.not_room_member`).

Clients **may** match on the full `code` for exact handling, on the category prefix (`code.startsWith("permission_denied.")`) for class-level handling, or on neither and fall back to displaying `error`. All three are supported uses.

## Categories

The categories below are the subset of gRPC canonical status codes that map to failures actually produced by this codebase. Codes outside this list (`OUT_OF_RANGE`, `DATA_LOSS`, `CANCELLED`, `UNIMPLEMENTED`, `ABORTED`) are reserved — add them only when a real producer appears.

| Category              | Meaning                                                                                       | HTTP | Retryable? |
|-----------------------|-----------------------------------------------------------------------------------------------|------|------------|
| `invalid_argument`    | Caller-supplied input is malformed, missing, or violates a documented bound.                  | 400  | No         |
| `unauthenticated`     | Caller has no valid credentials, or credentials have expired.                                 | 401  | No         |
| `permission_denied`   | Caller is authenticated but not authorized for this operation or resource.                    | 403  | No         |
| `not_found`           | Named resource does not exist, or is not visible to the caller.                               | 404  | No         |
| `already_exists`      | Resource the caller wanted to create is already present.                                      | 409  | No         |
| `failed_precondition` | System state precludes the operation (e.g. last owner demote, access window expired).         | 400  | No         |
| `resource_exhausted`  | Quota, rate limit, or capacity reached. Reserved; no current producer.                        | 429  | After backoff |
| `deadline_exceeded`   | Operation took too long. Distinct from `unavailable` because the client request likely succeeded server-side or is in flight. | 504  | Yes, with care |
| `unavailable`         | Transient backend dependency failure (DB unreachable, downstream service down, NATS no-responders). | 503 | Yes        |
| `internal`            | Server bug or unhandled error. Catch-all when none of the above apply.                        | 500  | No         |

The "Retryable?" column is a **hint** for clients — it does not bind worker Ack/Nak behavior, which is governed separately (see [§Non-goals](#non-goals)).

### HTTP status mapping

The mapping above is canonical. HTTP services translate at the boundary:

```go
// pseudocode — actual helper lands with the migration
func httpStatusFor(code string) int {
    switch category(code) {
    case "invalid_argument", "failed_precondition": return 400
    case "unauthenticated":                          return 401
    case "permission_denied":                        return 403
    case "not_found":                                return 404
    case "already_exists":                           return 409
    case "resource_exhausted":                       return 429
    case "internal":                                 return 500
    case "unavailable":                              return 503
    case "deadline_exceeded":                        return 504
    default:                                         return 500
    }
}
```

NATS replies carry the envelope only; there is no separate status field on the wire — the `code` category is the source of truth.

## Per-transport application

### HTTP (auth-service)

Replace `c.JSON(status, gin.H{"error": "..."})` with a helper that writes the envelope and picks the status from the category:

```json
HTTP/1.1 401 Unauthorized
Content-Type: application/json

{
  "code": "unauthenticated.sso_token_expired",
  "error": "SSO token has expired, please re-login",
  "requestId": "01970a4f-8c2d-7c9a-abcd-e0123456789f"
}
```

### NATS request/reply (room-service, history-service, search-service, gatekeeper reply)

Replace `natsutil.ReplyError(msg, "...")` and `natsrouter.RouteError{...}` with a helper that writes the same envelope. `model.ErrorResponse` is extended with `Code`, `RequestID`, and `Details map[string]any`; the existing one-off `RoomID` field is removed and its data moves under `details.roomId`.

```json
{
  "code": "already_exists.dm",
  "error": "dm already exists",
  "requestId": "01970a4f-8c2d-7c9a-abcd-e0123456789f",
  "details": { "roomId": "abc123xyz0000def4" }
}
```

### NATS async-job-result (room-worker)

`model.AsyncJobResult.Error` carries the same envelope when `Status == "error"`. The string-typed `Error` field becomes a typed object:

```json
{
  "status": "error",
  "requestId": "01970a4f-8c2d-7c9a-abcd-e0123456789f",
  "error": {
    "code": "already_exists.channel_name_taken",
    "error": "channel with that name already exists",
    "details": { "name": "engineering" }
  }
}
```

`requestId` lives at the `AsyncJobResult` level (it already does today); the inner envelope omits it to avoid duplication.

## Backward compatibility

- The `error` field is preserved with its current name and meaning. Clients that today do `JSON.parse(reply).error` continue to work without changes.
- Adding `code`, `requestId`, and `details` is purely additive on the wire. Old clients ignore unknown fields.
- The `roomId` field currently appended to `model.ErrorResponse` for `dm already exists` moves under `details.roomId` in the new envelope. Clients reading the top-level field must migrate before that field is removed; the rollout sequence is part of the implementation plan, not this spec.

## Open questions

These do not block agreeing on the format, but should be resolved before implementation starts:

1. **Reason registry.** Do reasons live in a single registry (e.g. constants in `pkg/model/errcodes`) or per-service? Single registry catches collisions but pulls every service into one file; per-service avoids the bottleneck but allows accidental duplicates.
2. **`details` schemas.** Per-`code` schemas can be documented inline in `client-api.md`, or as JSON Schema files under `docs/error-details/`. The latter is verifiable; the former is closer to the rest of the API doc.
3. **Worker error logging.** Workers don't reply, but we want their structured logs to use the same `code` strings so the same dashboard query covers RPC and async failures. Confirm before locking the format.
