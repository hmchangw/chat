# NATS Debug Tool — Per-Session Connections & Status

**Date:** 2026-06-15
**Scope:** `tools/nats-debug/`

## Problem

The `nats-debug` tool builds a single global `natsHub` in `main.go` and shares
it across every browser that opens the UI. All sessions therefore share the same
source/dest/request NATS connections, subscription set, connection status, and
SSE message feed. One user connecting, disconnecting, subscribing, or publishing
affects everyone else viewing the tool.

## Goal

Give each web user (browser) its own isolated hub: its own connections, status,
subscriptions, and message feed. One user's actions must not affect another's.

## Decisions

- **Session identity:** server-set `ndsid` cookie (`HttpOnly; SameSite=Strict;
  Path=/`). Both `fetch` and `EventSource` send it automatically on same-origin
  requests, so the frontend needs no ID-handling code. Identity is per-browser;
  multiple tabs in the same browser intentionally share one session.
- **Lifecycle:** an idle-timeout janitor closes a session's hub after no activity
  for `SESSION_IDLE_TIMEOUT` (default `30m`). No per-SSE-disconnect teardown — a
  page refresh briefly drops SSE and must not kill an in-use session.
- **Wiring:** explicit `resolve` in each handler (approach A) rather than context
  middleware — smallest, most testable change for a 12-route std-lib mux tool.

## Design

### Components

**`session.go` (new)**

```go
type session struct {
    id       string
    hub      Hub
    mu       sync.Mutex
    lastSeen time.Time
}

type sessionManager struct {
    mu          sync.Mutex
    sessions    map[string]*session
    factory     func() Hub       // builds a fresh hub; main injects newNATSHub
    idleTimeout time.Duration
    now         func() time.Time // injectable for sweep tests
    done        chan struct{}
}
```

Methods:

- `resolve(w, r) *session` — reads the `ndsid` cookie. If present **and** the
  session exists, touch `lastSeen` and return it. Otherwise mint a new id via
  `idgen.GenerateID()`, create a session via `factory()`, set the cookie, and
  return it. Always called first in a handler so the cookie is written before any
  body/headers.
- `start()` — launches the janitor goroutine. Ticks every minute (or
  `idleTimeout/2` when smaller). Terminates when `done` is closed.
- `sweep()` — closes and deletes sessions where `now() - lastSeen > idleTimeout`,
  calling `hub.Disconnect()` on each.
- `shutdown()` — closes `done` to stop the janitor, then `Disconnect()`s every
  remaining hub.

`session.touch()` updates `lastSeen` under its mutex.

**`handler.go`** — `handler` holds `sessions *sessionManager` instead of
`hub Hub`. Each method begins with `sess := h.sessions.resolve(w, r)` and uses
`sess.hub`. `serveUI` also calls `resolve` so the cookie is established at page
load, before the first API/SSE call.

**`main.go`** — builds `factory := func() Hub { return newNATSHub(cfg.CredsFile) }`,
constructs the manager with `cfg.IdleTimeout`, calls `start()`, and in shutdown
calls `sessions.shutdown()` (replacing the single `hub.Disconnect()`).

### SSE keep-alive

A user watching the feed may have no other HTTP activity and would otherwise be
swept mid-stream. The `events` handler adds a keep-alive ticker (~25s) that writes
a `: ping\n\n` comment and calls `sess.touch()`. This prevents proxy idle-timeouts
and keeps an actively-watched session alive. SSE clients are registered per-hub,
so each browser's feed is already isolated.

### Teardown is a single call

`Disconnect()` already closes all three connections (source, dest, request) and
clears subscriptions (`hub_nats.go` `disconnectLocked`). Session teardown is
therefore just `hub.Disconnect()` — no new `Hub` interface method is required.

### Config

Add to `config` in `main.go`:

```go
IdleTimeout time.Duration `env:"SESSION_IDLE_TIMEOUT" envDefault:"30m"`
```

### Error handling

No new client-facing error categories — this is an internal debug tool using
plain `http.Error`/`writeJSON`. Teardown errors are logged via `slog`
(`Disconnect` already logs internally).

## Testing (TDD)

- **`session_test.go` (new):**
  - `resolve` sets a cookie when none is present.
  - `resolve` reuses the same hub for the same cookie (factory called once).
  - Two different cookies get two different hubs (isolation).
  - `sweep` tears down only idle sessions (injected `now`) and calls `Disconnect`.
  - `sweep` keeps a recently-touched session.
  - `shutdown` disconnects all sessions and stops the janitor.
- **`handler_test.go` (updated):** construct handlers via a `sessionManager` whose
  factory returns the mock hub; assert `Set-Cookie` on responses; assert distinct
  cookies route to distinct hubs.
- **`hub_nats_test.go`:** unchanged — hub internals are untouched.

## Out of scope

- Per-tab isolation (the cookie is per-browser by design).
- Authentication / per-user access control.
- Changes to the `Hub` interface or `natsHub` internals.
- Frontend changes (cookie handling is automatic for same-origin requests).
