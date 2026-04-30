# natsrouter Concurrency Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `pkg/natsrouter`'s implicit per-subscription serialization (one in-flight handler per route per pod) with a Gin-style admission-controlled concurrency model: spawn-per-message guarded by a semaphore, with a 503-busy reply when the semaphore is saturated.

**Architecture:** Each `*nats.Subscription` still has one dispatcher goroutine inside nats.go. The router's callback no longer runs the handler chain inline — it acquires a router-level semaphore (non-blocking), and on success spawns a goroutine that runs the chain. On semaphore-full, the callback publishes a `{"error":"service busy","code":"unavailable"}` reply and returns. Per-route override (`WithConcurrency`) gives a route its own semaphore so it can be bulkheaded from the shared pool. A new `HandlerTimeout(d)` middleware enforces a per-handler context deadline.

**Tech Stack:** Go 1.25, `nats-io/nats.go`, `Marz32onE/instrumentation-go/otel-nats`, `stretchr/testify`.

**Out of scope:**
- Replacing the router's NATS transport. Subjects, queue groups, and subscription semantics are unchanged.
- Any service-level wiring (history-service, message-worker, etc.) — this plan only changes `pkg/natsrouter`. Service-level adoption is tracked in each service's own plan (e.g. history-service Task 11 wires `WithMaxConcurrency`).
- Per-route ordering guarantees. The new model loses per-subject FIFO; documented in Task 7. Routes that need ordering opt in via `WithConcurrency(1)`.

---
