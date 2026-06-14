# Debug Payload Capture (dev-only full-payload logging)

**Date:** 2026-06-14
**Status:** Approved — ready to build
**Relationship:** A *separate* capability from the `X-Debug` flow/debug/trace ladder. That ladder is **metadata-only and always-available** (rate-capped). This is **content**, and is **off in production by construction**.

## Problem

During development, a client (e.g. an independent app calling `LoadHistory`) makes a real request that is hard to replicate in a debug tool. The developer wants to see the **full request and reply payloads** for that call. The `X-Debug` ladder is deliberately metadata-only and will never log bodies; the operator-side NATS tap is awkward for sync request/reply (random `_INBOX`, correlation) and can't easily reproduce the client's exact call.

## Decision

A header is the right trigger for this case — but the safety must not depend on perfectly stripping a client-reachable header in prod. So:

| Concern | Choice |
|---------|--------|
| Trigger | `X-Debug-Payload: 1` header on the request (truthy `1`/`true`/`on`; anything else off). Distinct from `X-Debug` — content, not the metadata ladder. |
| **Safety gate** | A per-service config flag `DEBUG_LOG_PAYLOADS` (env `DEBUG_LOG_PAYLOADS`, **default `false`**). A service logs a body ONLY when its own config has the flag on. In prod the flag is unset → the header is **inert**, no client can cause a body to be logged. |
| Emission | `slog` at INFO, message `"debug payload"`, fields: `direction` (request/reply/consumed), `subject`, `request_id`, `bytes`, `payload` (the raw bytes as a string). Keyed by `request_id` so request+reply pair up. |
| Propagation | Rides the existing `X-Debug` machinery: `HeaderForContext` also emits `X-Debug-Payload`, so capture intent flows cross-service like the rung (useful for the async pipeline; not needed for single-service RPCs). |
| Scope (capture sites) | **natsrouter Register** captures request + reply centrally (covers `LoadHistory` and every RPC at once). JetStream consumer entries capture `msg.Data()` under the same gate (follow-up / opt-in per service). |

**Why the env gate, not ingress stripping:** safety becomes "prod services are configured to ignore it" — a single, greppable, auditable flag (`DEBUG_LOG_PAYLOADS=false` in prod) — instead of "an ingress must strip a client header correctly, forever." Far smaller, more visible surface.

## Design

### `pkg/natsutil` — payload-capture intent (mirrors X-Debug propagation)
- `const DebugPayloadHeader = "X-Debug-Payload"`
- `WithPayloadCapture(ctx) ctx` / `PayloadCaptureFromContext(ctx) bool`
- `PayloadCaptureFromHeader(nats.Header) bool` (truthy parse)
- `HeaderForContext` also emits `X-Debug-Payload: 1` when set (so it propagates onto `NewMsg`).

### `pkg/logctx` — the gate + the emit
- `Config` gains `Payloads bool \`env:"PAYLOADS" envDefault:"false"\`` (so `DEBUG_LOG_PAYLOADS` rides the existing `DEBUG_LOG_` prefix). `Configure` stores it in a package var `capturePayloads`.
- `Admit` (already the boundary) additionally stamps `natsutil.WithPayloadCapture` when the inbound header carries `X-Debug-Payload`. So every natsrouter handler + JetStream consumer that already calls `Admit` propagates the intent for free.
- `CapturePayload(ctx, direction, subject string, data []byte)`:
  1. `if !capturePayloads { return }`  ← prod-safe gate (the load-bearing line)
  2. `if !natsutil.PayloadCaptureFromContext(ctx) { return }`  ← per-request trigger
  3. `slog.InfoContext(ctx, "debug payload", "direction", direction, "subject", subject, "request_id", …, "bytes", len(data), "payload", string(data))`

  Logging at INFO (not a sub-INFO rung) keeps payload capture **independent of the metadata-admission gate** — `CapturePayload` does all its own gating.

### `pkg/natsrouter` — central request/reply capture
In `Register`'s wrapper: `logctx.CapturePayload(ctx, "request", subject, c.Msg.Data)` before the handler; `logctx.CapturePayload(ctx, "reply", subject, replyBytes)` after the reply is marshaled (the wrapper already has the bytes). One place → covers `LoadHistory` (history-service) and all RPC services.

### Service wiring
Services that already parse `DebugLog logctx.Config` + call `logctx.Configure` get the flag for free. **history-service** must add the `DebugLog` config field + `logctx.Configure(cfg.DebugLog)` call so `LoadHistory` capture works.

## Content-safety invariant (must stay true + tested)

> With `DEBUG_LOG_PAYLOADS` **off** (default), no body is ever logged — **even when `X-Debug-Payload` is set.**

This is the production guarantee. A test asserts: flag off + header set → no `payload` field emitted; flag on + header set → body emitted; flag on + header absent → nothing. This keeps the existing metadata content-safety guarantee intact and makes "is the prod door shut?" a CI check.

## Guardrails

- **Off in prod by default**; flipping `DEBUG_LOG_PAYLOADS` is a deliberate, auditable, reviewable config change.
- If ever enabled in a **shared** env with real PII (staging), route to a restricted sink or scrub — out of scope here; for local/dev, the normal log stream is fine.
- Encryption unaffected: encrypted-room payloads on the bus are ciphertext; this logs whatever the service holds (plaintext at the handler boundary in dev).

## Out of scope
- Per-field redaction; restricted/short-TTL sinks (dev uses normal logs).
- Rate-capping the payload lines (env-gated to dev already; add later if dev logs get noisy).
- A client-facing contract: `X-Debug-Payload` is a dev/transport affordance, not a documented client API (no `client-api.md` change).

## Testing (TDD)
- `natsutil`: payload header round-trips ctx↔header; `HeaderForContext`/`NewMsg` emit it; absent → not emitted.
- `logctx.CapturePayload`: the three-case gate (flag off → silent even with header; flag on + requested → body; flag on + not requested → silent); `Admit` stamps payload-requested from the header.
- `natsrouter`: Register captures request + reply when gated on; nothing when flag off.
- history-service: `Configure(cfg.DebugLog)` wired.
