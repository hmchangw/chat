# mchat

A browser-based chat client and full-stack local development environment. Provides a web UI for login, room management, messaging, and real-time event streaming backed by Keycloak, auth-service, and NATS.

## Features

- **Login** via Keycloak password grant, with automatic NATS credential provisioning through auth-service
- **Room management** — create rooms, invite members
- **Messaging** — send messages through the MESSAGES JetStream stream with gatekeeper validation
- **Real-time events** via Server-Sent Events (SSE) — room updates, new messages, notifications
- **Full-stack Docker Compose** — spins up the entire chat system (Keycloak, NATS, MongoDB, Cassandra, and all backend services) in one command

## Quick Start

### Prerequisites

- Docker and Docker Compose

### 1. Generate signing key

Run the setup script once to create the `.env` file with a NATS account signing key:

```bash
./tools/mchat/deploy/setup.sh
```

### 2. Start the stack

```bash
docker compose -f tools/mchat/deploy/docker-compose.yml up -d
```

Wait for Keycloak to be ready (~30-60s):

```bash
docker compose -f tools/mchat/deploy/docker-compose.yml logs -f keycloak
```

### 3. Open the UI

| Service | URL |
|---------|-----|
| mchat UI | http://localhost:8091 |
| Keycloak Admin | http://localhost:9090 (admin / admin) |
| NATS Monitoring | http://localhost:8222 |

Test users: `testuser` / `testuser`, `alice` / `alice`

### Run the binary directly

Requires Keycloak, auth-service, and NATS already running.

```bash
# Build
make build SERVICE=tools/mchat

# Run with defaults
./bin/tools/mchat

# Custom config
PORT=8091 KEYCLOAK_URL=http://localhost:9090 NATS_URL=nats://localhost:4222 ./bin/tools/mchat
```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Health check |
| `POST` | `/api/login` | Authenticate via Keycloak and create session |
| `POST` | `/api/logout` | Destroy session |
| `GET` | `/api/rooms` | List rooms for the logged-in user |
| `POST` | `/api/rooms` | Create a new room |
| `POST` | `/api/rooms/{id}/invite` | Invite a member to a room |
| `POST` | `/api/rooms/{id}/messages` | Send a message to a room |
| `GET` | `/api/events` | SSE stream of real-time events |
| `GET` | `/api/status` | Connection and session status |
| `GET` | `/` | Serve the web UI |

### Login

```bash
curl -X POST http://localhost:8091/api/login \
  -H 'Content-Type: application/json' \
  -d '{"username": "testuser", "password": "testuser"}'
```

Returns a session cookie (`mchat-session`) and user info.

### Create Room

```bash
curl -X POST http://localhost:8091/api/rooms \
  -H 'Content-Type: application/json' \
  -b 'mchat-session=<session-id>' \
  -d '{"name": "general", "type": "group", "members": ["alice"]}'
```

### Send Message

```bash
curl -X POST http://localhost:8091/api/rooms/<room-id>/messages \
  -H 'Content-Type: application/json' \
  -b 'mchat-session=<session-id>' \
  -d '{"content": "hello world"}'
```

### SSE Events

```bash
curl -N http://localhost:8091/api/events \
  -b 'mchat-session=<session-id>'
```

Event types: `room_event`, `room_metadata_update`, `subscription_update`, `notification`, `response`, `message`.

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `PORT` | `8091` | HTTP port the server listens on |
| `KEYCLOAK_URL` | `http://localhost:9090` | Keycloak base URL |
| `KEYCLOAK_REALM` | `chatapp` | Keycloak realm name |
| `KEYCLOAK_CLIENT_ID` | `nats-chat` | Keycloak OAuth client ID |
| `AUTH_SERVICE_URL` | `http://localhost:8080` | auth-service base URL |
| `NATS_URL` | `nats://localhost:4222` | NATS server URL |
| `SITE_ID` | `site-local` | Site identifier for multi-site federation |

## Architecture

```
Browser ──HTTP/SSE──> mchat server ──NATS──> backend services
                          |
                     Keycloak (auth)
                          |
                     auth-service (NATS credentials)
```

The login flow:

1. mchat sends the user's credentials to Keycloak (password grant) and receives an access token
2. mchat generates an ephemeral NKey pair and sends the token + public key to auth-service
3. auth-service returns a NATS JWT and user info
4. mchat creates a session with a NATS subscription on `chat.user.<account>.>` to receive all events for that user

Messages are published to the MESSAGES JetStream stream. The message-gatekeeper validates and publishes to MESSAGES_CANONICAL, then replies on a per-request response subject. mchat waits for this reply before responding to the client.

Real-time events (new messages, room updates, notifications) are delivered via SSE. Each session subscribes to the user's NATS subject wildcard and fans out incoming messages to all connected browser tabs.

## Services Started by Docker Compose

| Service | Description |
|---------|-------------|
| keycloak | Identity provider (SSO) |
| nats | NATS server with JetStream |
| mongodb | Operational database |
| cassandra | Message history (time-series) |
| auth-service | SSO token validation and NATS credential issuing |
| message-gatekeeper | Message validation and canonical publishing |
| broadcast-worker | Fan-out delivery to room members |
| message-worker | Persist messages to Cassandra |
| room-service | Room CRUD and membership (request/reply) |
| room-worker | Room event processing (JetStream consumer) |
| mchat | This tool — web UI and API gateway |

## Stop

```bash
docker compose -f tools/mchat/deploy/docker-compose.yml down
```
