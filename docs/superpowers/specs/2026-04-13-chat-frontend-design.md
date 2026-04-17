# Chat Frontend Design Spec

## Purpose

A developer tool and POC demo frontend for the distributed chat system. Lets developers send and receive messages, create rooms, browse history, and verify the end-to-end event flow through the backend services.

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Stack | Vite + React | Team has React expertise; Vite gives zero-config dev server with hot reload |
| Connection | Direct NATS WebSocket | Architecture already supports it; auth-service issues scoped NATS JWTs; no gateway needed |
| Auth (dev) | Dev-mode bypass in auth-service | Skips OIDC dependency for local development; accepts account name directly |
| Location | `chat-frontend/` at repo root | Follows existing flat service directory convention |
| Styling | Plain CSS (no framework) | Dev tool — keep dependencies minimal |
| State management | React state + context | Sufficient for rooms list + message list; no Redux/Zustand needed |

## Scope

### In scope (v1)

1. **Dev-mode login** — Enter account name and site ID, authenticate via `/auth`, connect to NATS WebSocket
2. **Room list** — Fetch user's rooms via request/reply, display sorted by last message time, update in real-time
3. **Send and receive messages** — Text input, publish to NATS, see incoming messages via room event subscription
4. **Message history** — Load previous messages when entering a room via `msg.history` request/reply
5. **Create room** — Form to create a group chat or DM with a name and member list

### Out of scope (future iterations)

- Thread replies UI
- Room member management (invite, remove, member list)
- Subscription management (leave room, mark as read)
- File attachments, cards, reactions
- Message editing/deletion
- Search
- Notifications UI
- Production OIDC login flow

## Architecture

```
Browser (React SPA)
  |
  +-- POST /auth (auth-service:8080) --> NATS JWT + user info
  |
  +-- ws://localhost:4223 (NATS WebSocket)
        +-- Request/Reply: list rooms, get room, create room, load history
        +-- Publish: send messages
        +-- Subscribe: room events, subscription updates, metadata updates
```

The frontend has no backend of its own. It talks directly to the existing services through NATS, using the same subject patterns the backend services use.

## UI Layout

```
+------------------------------------------------------+
|  [Dev Login]  user: alice  |  site: site-A           |
+--------------+---------------------------------------+
|              |  # general                   2 members|
|  Rooms       |---------------------------------------|
|              |                                       |
|  # general   |  [alice] 10:30 AM                     |
|  # backend   |  Hello everyone!                      |
|  # frontend  |                                       |
|              |  [bob] 10:31 AM                        |
|  [+ Create]  |  Hey alice!                           |
|              |                                       |
|              |                                       |
|              |---------------------------------------|
|              |  [Type a message...        ] [Send]   |
+--------------+---------------------------------------+
```

- **Login screen** — Shown first. Text field for account name, site ID field (default from env), "Connect" button.
- **Left sidebar** — Room list sorted by `lastMsgAt` descending. Highlights selected room. "Create Room" button at bottom.
- **Main area** — Header with room name and member count. Scrollable message list (newest at bottom, auto-scroll). Message input bar at bottom.

## Components

| Component | File | Responsibility |
|-----------|------|----------------|
| `App` | `src/App.jsx` | Top-level: renders LoginPage or ChatPage based on connection state |
| `NatsProvider` | `src/context/NatsContext.jsx` | React context managing NATS connection lifecycle; exposes publish/request/subscribe helpers |
| `LoginPage` | `src/pages/LoginPage.jsx` | Dev-mode login form; calls auth-service, triggers NATS connection |
| `ChatPage` | `src/pages/ChatPage.jsx` | Main layout shell: sidebar + message area |
| `RoomList` | `src/components/RoomList.jsx` | Fetches room list, subscribes to real-time updates, handles room selection |
| `MessageArea` | `src/components/MessageArea.jsx` | Displays messages for selected room, loads history, subscribes to room events |
| `MessageInput` | `src/components/MessageInput.jsx` | Text input, publishes message to NATS on submit |
| `CreateRoomDialog` | `src/components/CreateRoomDialog.jsx` | Modal/dialog for creating a new room (name, type, members) |

## NATS Integration Layer (NatsContext)

The `NatsProvider` component wraps the entire app and provides:

```
NatsProvider
  +-- connect(account, siteId)
  |     1. Generate NKey pair (ed25519)
  |     2. POST /auth with { account, natsPublicKey } (dev mode)
  |     3. Store user info from response
  |     4. Connect to ws://localhost:4223 with NATS JWT authenticator
  |     5. Set connected state
  |
  +-- request(subject, data) --> Promise<response>
  |     JSON-encode data, send NATS request, parse JSON response
  |
  +-- publish(subject, data)
  |     JSON-encode data, publish to NATS
  |
  +-- subscribe(subject, callback) --> subscription handle
  |     Subscribe, parse incoming JSON, invoke callback
  |
  +-- disconnect()
        Drain connection, clear state
```

Exposed via `useNats()` hook. Components call `request()` for queries, `publish()` for fire-and-forget, and `subscribe()` for real-time events.

## NATS Subjects

| Action | Subject | Method |
|--------|---------|--------|
| List rooms | `chat.user.{account}.request.rooms.list` | request/reply |
| Get room | `chat.user.{account}.request.rooms.get.{roomID}` | request/reply |
| Create room | `chat.user.{account}.request.rooms.create` | request/reply |
| Send message | `chat.user.{account}.room.{roomID}.{siteID}.msg.send` | publish (ack on `chat.user.{account}.response.{requestID}`) |
| Load history | `chat.user.{account}.request.room.{roomID}.{siteID}.msg.history` | request/reply |
| Room events (new messages) | `chat.room.{roomID}.event` | subscribe |
| Subscription updates | `chat.user.{account}.event.subscription.update` | subscribe |
| Room metadata updates | `chat.user.{account}.event.room.metadata.update` | subscribe |

## Data Flow

### Login

1. User enters account name (e.g., "alice") and site ID (e.g., "site-A")
2. Frontend generates an NKey pair
3. POST `/auth` with `{ "account": "alice", "natsPublicKey": "<public key>" }`
4. Auth-service (dev mode) returns `{ "natsJwt": "...", "user": { "account": "alice", ... } }`
5. Frontend connects to NATS WebSocket at `ws://localhost:4223` using the JWT
6. On success, transition to ChatPage

### Room List

1. On mount, send NATS request to `chat.user.alice.request.rooms.list` with `{}`
2. Receive `{ "rooms": [...] }`, render sorted by `lastMsgAt` descending
3. Subscribe to `chat.user.alice.event.subscription.update` for room added/removed events
4. Subscribe to `chat.user.alice.event.room.metadata.update` for name/count/lastMsg changes
5. Update room list in-place when events arrive

### Viewing Messages

1. User clicks a room in the sidebar
2. Subscribe to `chat.room.{roomID}.event` for real-time messages
3. Send NATS request to `chat.user.alice.request.room.{roomID}.{siteID}.msg.history` with `{ "limit": 50 }`
4. Receive `{ "messages": [...] }` (descending order), reverse for display (oldest at top)
5. Render messages, auto-scroll to bottom
6. When a `RoomEvent` arrives with `type: "new_message"`, append the `message` (a `ClientMessage` with sender info) to the list
7. Unsubscribe from previous room's events when switching rooms

### Sending Messages

1. User types a message and presses Enter or clicks Send
2. Generate a UUID for message ID and request ID
3. Publish to `chat.user.alice.room.{roomID}.{siteID}.msg.send` with:
   ```json
   {
     "id": "<uuid>",
     "content": "Hello!",
     "requestId": "<uuid>"
   }
   ```
4. The message flows through the backend pipeline: message-gatekeeper validates, message-worker persists, broadcast-worker fans out
5. The message arrives back on `chat.room.{roomID}.event` subscription as a `RoomEvent`
6. No optimistic rendering — wait for the event to confirm delivery

### Creating a Room

1. User clicks "Create Room" button
2. Dialog appears: room name, type (group/DM), comma-separated member accounts
3. On submit, send NATS request to `chat.user.alice.request.rooms.create` with:
   ```json
   {
     "name": "frontend-team",
     "type": "group",
     "createdBy": "<userId>",
     "createdByAccount": "alice",
     "siteId": "site-A",
     "members": ["bob", "charlie"]
   }
   ```
4. Receive created room in response
5. The new room also arrives via `subscription.update` event — add to room list

## Dev-Mode Auth Bypass

Changes to `auth-service`:

- New config field: `DEV_MODE` (bool, env var, default `false`)
- When `DEV_MODE=true`:
  - `OIDC_ISSUER_URL` and `OIDC_AUDIENCE` become optional (not required)
  - OIDC validator is not initialized
  - `HandleAuth` accepts `{ "account": "alice", "natsPublicKey": "..." }` instead of `{ "ssoToken": "...", "natsPublicKey": "..." }`
  - Skips OIDC token validation entirely
  - Synthesizes user info from the account name (e.g., `engName` = account, `email` = `{account}@dev.local`)
  - Signs and returns a valid NATS JWT with the same scoped permissions as production
- When `DEV_MODE=false`: behavior is identical to current production flow; no code paths change

The `authRequest` struct gains an optional `Account` field used only in dev mode. The `SSOToken` field becomes optional when `DEV_MODE=true`.

## Project Structure

```
chat-frontend/
  index.html
  package.json
  vite.config.js
  src/
    main.jsx              -- React entry point, renders App
    App.jsx               -- Top-level: LoginPage or ChatPage
    context/
      NatsContext.jsx      -- NATS connection provider + useNats hook
    pages/
      LoginPage.jsx        -- Dev-mode login form
      ChatPage.jsx         -- Main chat layout (sidebar + messages)
    components/
      RoomList.jsx         -- Room sidebar list
      MessageArea.jsx      -- Message display + history
      MessageInput.jsx     -- Text input + send
      CreateRoomDialog.jsx -- Room creation form
    styles/
      index.css            -- All styles
  deploy/
    Dockerfile             -- Multi-stage: node build + nginx serve
    docker-compose.yml     -- Frontend + NATS (WebSocket) + auth-service
```

## Dependencies

| Package | Purpose |
|---------|---------|
| `react` | UI framework |
| `react-dom` | DOM rendering |
| `nats.ws` | NATS WebSocket client for browsers |
| `nkeys.js` | NKey pair generation for NATS auth |
| `uuid` | Generate message IDs and request IDs |

No CSS framework, no state management library, no client-side router.

## Configuration

Frontend environment variables (injected at build time via Vite's `import.meta.env`):

| Variable | Default | Description |
|----------|---------|-------------|
| `VITE_AUTH_URL` | `http://localhost:8080` | Auth-service base URL |
| `VITE_NATS_URL` | `ws://localhost:4223` | NATS WebSocket endpoint |
| `VITE_DEFAULT_SITE_ID` | `site-A` | Pre-filled site ID on login page |

## Docker Setup

**Dockerfile** (`chat-frontend/deploy/Dockerfile`):
- Builder stage: `node:22-alpine`, `npm ci`, `npm run build`
- Runtime stage: `nginx:alpine`, copy build output to `/usr/share/nginx/html`

**docker-compose.yml** (`chat-frontend/deploy/docker-compose.yml`):
- `frontend`: the built image, port 3000 -> nginx 80
- `nats`: `nats:latest` with `--jetstream --http_port 8222` and WebSocket on port 4223
- `auth-service`: with `DEV_MODE=true`, linked to NATS

## Error Handling

- NATS connection failures: show error on login page, allow retry
- Request/reply timeouts (default 5s): show inline error in the relevant component
- Backend error responses (`{ "error": "...", "code": "..." }`): display the error message to the user
- Disconnection during use: show a banner "Disconnected — Reconnecting..." (NATS client handles auto-reconnect)
