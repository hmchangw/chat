# NATS Debug Tool

A browser-based debug UI for inspecting and publishing NATS messages across two independent servers.

## Features

- Connect to a **source** NATS server (for publishing) and a **dest** NATS server (for subscribing) independently
- Subscribe to **multiple subjects simultaneously** using NATS wildcards (`chat.>`, `fanout.*`, etc.)
- Each subscription is **colour-coded** so you can tell messages apart at a glance
- **Publish** arbitrary JSON payloads to any subject on the source server
- **Real-time message feed** with JSON syntax highlighting, subject filtering, copy-to-clipboard, and auto-scroll
- Quick-sample buttons pre-fill common chat subjects

## Quick Start

### Option 1 — Docker Compose (recommended)

Spins up two NATS servers and the debug UI in one command.

```bash
docker compose -f tools/nats-debug/deploy/docker-compose.yml up
```

| Service | URL |
|---------|-----|
| Debug UI | http://localhost:8090 |
| Source NATS | nats://localhost:4222 |
| Dest NATS | nats://localhost:4223 |

> **Tip:** For simple local testing, point both Source and Dest at the same NATS URL (e.g. `nats://localhost:4222`). Subscribe on dest and publish on source — messages appear in the feed immediately.

### Option 2 — Run the binary directly

Requires a NATS server already running.

```bash
# Build
make build SERVICE=tools/nats-debug

# Run (default port 8090)
./bin/tools/nats-debug

# Custom port
PORT=9000 ./bin/tools/nats-debug
```

Open http://localhost:8090.

## Usage

### 1. Connect

Enter the URLs for both servers and click **Connect**.

| Field | Description |
|-------|-------------|
| Source NATS | Server you will **publish** to |
| Dest NATS   | Server you will **subscribe** to |

### 2. Subscribe

Type a subject pattern in the Subscriptions panel and click **+**.
NATS wildcards are supported:

| Pattern | Matches |
|---------|---------|
| `chat.>` | All subjects under `chat.` |
| `fanout.*` | Single token — e.g. `fanout.site-a` |
| `chat.room.123` | Exact subject |

Add as many subscriptions as you need. Remove any with **×**.

### 3. Publish

Fill in a subject and a JSON payload, then click **Publish** (or press `Ctrl+Enter`).
Messages are sent to the **source** server.

### 4. Message Feed

Incoming messages appear in real time on the right panel:

- **Filter** by subject using the search box
- **Click** a message header to collapse/expand the payload
- **⧉** copies the raw payload to the clipboard
- **↓ Auto** toggles auto-scroll (green = enabled)
- **Clear** removes all messages from the view

### Quick Samples

Use the sidebar buttons to pre-fill common patterns:

| Button | Action |
|--------|--------|
| Subscribe chat.> | Fills subject input with `chat.>` |
| Subscribe fanout.> | Fills subject input with `fanout.>` |
| Publish sample message | Fills publish form with a sample chat message |

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `PORT`  | `8090`  | HTTP port the UI listens on |

## Architecture

```
Browser ──SSE──▶ nats-debug server
        ◀──HTTP─┘     │           │
                  source NATS  dest NATS
                  (publish)   (subscribe)
```

The server holds two independent NATS connections. Subscriptions are registered on the dest connection; publishes go to the source connection. Incoming NATS messages are broadcast to all connected browser tabs via Server-Sent Events (SSE).
