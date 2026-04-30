# History-Service Production-Readiness Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply a focused set of code-quality, performance, and operational improvements to `history-service` identified during the production-readiness review, without changing externally-observable behavior.

**Architecture:** All work is scoped to `history-service/` and a small touch in `pkg/cassutil` and `pkg/model/cassandra`. No NATS subject changes, no Cassandra/Mongo schema changes, no API/response shape changes. Improvements are independent tasks committed individually so each lands as a reviewable, revertable change.

**Tech Stack:** Go 1.25, gocql, mongo-driver/v2, NATS, `caarlos0/env`, testcontainers-go, `go.uber.org/mock`, `stretchr/testify`.

**Out of scope (tracked separately):**
- `pkg/natsrouter` worker pool / handler-timeout middleware (separate spec).
- EditMessage LWT (deferred — not implementing per user decision).
- Observability (metrics, request-ID propagation) — deferred.
- HTTP `/healthz` endpoint — deferred.
- Cross-site federation of edits/deletes — out of architectural scope.

---
