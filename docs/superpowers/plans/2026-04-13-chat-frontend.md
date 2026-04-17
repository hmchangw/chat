# Chat Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Vite + React chat frontend that connects directly to NATS via WebSocket for sending/receiving messages, listing rooms, loading history, and creating rooms.

**Architecture:** The SPA authenticates via the auth-service's dev-mode bypass (`POST /auth`), receives a NATS JWT, then connects to NATS over WebSocket. All queries use NATS request/reply; real-time events arrive via NATS subscriptions. No backend-for-frontend — the browser talks directly to NATS.

**Tech Stack:** Vite, React 19, nats.ws, nkeys.js, uuid

**Spec:** `docs/superpowers/specs/2026-04-13-chat-frontend-design.md`

---

## File Map

### New files (chat-frontend/)

| File | Responsibility |
|------|----------------|
| `chat-frontend/package.json` | npm project config, dependencies, scripts |
| `chat-frontend/vite.config.js` | Vite dev server config (proxy, port) |
| `chat-frontend/index.html` | HTML entry point |
| `chat-frontend/src/main.jsx` | React DOM entry point |
| `chat-frontend/src/App.jsx` | Top-level: LoginPage or ChatPage based on connection state |
| `chat-frontend/src/context/NatsContext.jsx` | NATS connection provider, useNats hook |
| `chat-frontend/src/pages/LoginPage.jsx` | Dev-mode login form |
| `chat-frontend/src/pages/ChatPage.jsx` | Main layout: sidebar + message area |
| `chat-frontend/src/components/RoomList.jsx` | Room sidebar with real-time updates |
| `chat-frontend/src/components/MessageArea.jsx` | Message display + history loading |
| `chat-frontend/src/components/MessageInput.jsx` | Text input + send |
| `chat-frontend/src/components/CreateRoomDialog.jsx` | Room creation form |
| `chat-frontend/src/styles/index.css` | All styles |
| `chat-frontend/deploy/Dockerfile` | Multi-stage: node build + nginx serve |
| `chat-frontend/deploy/docker-compose.yml` | Frontend + NATS (WebSocket) + auth-service |
| `chat-frontend/deploy/nats.conf` | NATS config with WebSocket listener on 4223 |

### Modified files (auth-service/)

| File | Change |
|------|--------|
| `auth-service/main.go` | Add `DEV_MODE` config field; conditionally skip OIDC init |
| `auth-service/handler.go` | Add `devMode` field to AuthHandler; bypass OIDC validation when true |
| `auth-service/handler_test.go` | Add tests for dev-mode auth bypass |

### Modified files (root)

| File | Change |
|------|--------|
| `.gitignore` | Add `node_modules/` |

---

## Task 1: Scaffold Vite + React project

**Files:**
- Create: `chat-frontend/package.json`
- Create: `chat-frontend/vite.config.js`
- Create: `chat-frontend/index.html`
- Create: `chat-frontend/src/main.jsx`
- Create: `chat-frontend/src/App.jsx`
- Create: `chat-frontend/src/styles/index.css`
- Modify: `.gitignore`

- [ ] **Step 1: Create package.json**

```json
{
  "name": "chat-frontend",
  "private": true,
  "version": "0.0.1",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "nats.ws": "^1.30.0",
    "nkeys.js": "^1.1.0",
    "react": "^19.1.0",
    "react-dom": "^19.1.0",
    "uuid": "^11.1.0"
  },
  "devDependencies": {
    "@types/react": "^19.1.2",
    "@types/react-dom": "^19.1.2",
    "@vitejs/plugin-react": "^4.4.1",
    "vite": "^6.3.2"
  }
}
```

- [ ] **Step 2: Create vite.config.js**

```js
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
  },
})
```

- [ ] **Step 3: Create index.html**

```html
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Chat</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.jsx"></script>
  </body>
</html>
```

- [ ] **Step 4: Create src/main.jsx**

```jsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import './styles/index.css'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
```

- [ ] **Step 5: Create src/App.jsx (placeholder)**

```jsx
export default function App() {
  return <div className="app"><h1>Chat</h1></div>
}
```

- [ ] **Step 6: Create src/styles/index.css (base reset)**

```css
*,
*::before,
*::after {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

html, body, #root {
  height: 100%;
}

body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  font-size: 14px;
  color: #1a1a1a;
  background: #f5f5f5;
}
```

- [ ] **Step 7: Add node_modules/ to .gitignore**

Append to `.gitignore`:
```
# Frontend
node_modules/
chat-frontend/dist/
```

- [ ] **Step 8: Install dependencies and verify**

Run:
```bash
cd chat-frontend && npm install
```
Expected: `node_modules/` created, no errors.

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds, output in `chat-frontend/dist/`.

- [ ] **Step 9: Commit**

```bash
git add chat-frontend/package.json chat-frontend/vite.config.js chat-frontend/index.html \
  chat-frontend/src/main.jsx chat-frontend/src/App.jsx chat-frontend/src/styles/index.css \
  .gitignore
git commit -m "feat(chat-frontend): scaffold Vite + React project"
```

---

## Task 2: Dev-mode auth bypass in auth-service (TDD)

**Files:**
- Modify: `auth-service/handler.go`
- Modify: `auth-service/handler_test.go`
- Modify: `auth-service/main.go`

The auth-service currently requires an OIDC SSO token. We add a `DEV_MODE` that accepts just an account name and skips OIDC validation. The `authRequest` struct gets an optional `Account` field; when `devMode=true`, the handler uses that instead of validating `SSOToken`.

- [ ] **Step 1: Write failing tests for dev-mode auth**

Add to `auth-service/handler_test.go`:

```go
func TestHandleAuth_DevMode_ValidRequest(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	handler := NewAuthHandler(nil, signingKP, 2*time.Hour)
	handler.devMode = true
	router := setupRouter(t, handler)

	body := `{"account":"alice","natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp authResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, "alice", resp.UserInfo.Account)
	assert.Equal(t, "alice", resp.UserInfo.EngName)
	assert.Equal(t, "alice@dev.local", resp.UserInfo.Email)

	// Verify NATS JWT is valid and scoped to alice.
	claims, err := jwt.DecodeUserClaims(resp.NATSJWT)
	require.NoError(t, err)
	assert.Equal(t, userPub, claims.Subject)
	assert.Contains(t, []string(claims.Pub.Allow), "chat.user.alice.>")
	assert.Contains(t, []string(claims.Sub.Allow), "chat.user.alice.>")
}

func TestHandleAuth_DevMode_MissingAccount(t *testing.T) {
	signingKP := mustAccountKP(t)
	userPub := mustUserNKey(t)

	handler := NewAuthHandler(nil, signingKP, 2*time.Hour)
	handler.devMode = true
	router := setupRouter(t, handler)

	body := `{"natsPublicKey":"` + userPub + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "account")
}

func TestHandleAuth_DevMode_InvalidNKey(t *testing.T) {
	signingKP := mustAccountKP(t)

	handler := NewAuthHandler(nil, signingKP, 2*time.Hour)
	handler.devMode = true
	router := setupRouter(t, handler)

	body := `{"account":"alice","natsPublicKey":"NOT-VALID"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid natsPublicKey")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
make test SERVICE=auth-service
```
Expected: FAIL — `handler.devMode` field does not exist yet.

- [ ] **Step 3: Add devMode field and dev-mode request struct to handler.go**

In `auth-service/handler.go`, add the `devMode` field to `AuthHandler` and a new request struct:

```go
type devAuthRequest struct {
	Account       string `json:"account" binding:"required"`
	NATSPublicKey string `json:"natsPublicKey" binding:"required"`
}
```

Add the `devMode` field to `AuthHandler`:

```go
type AuthHandler struct {
	validator  TokenValidator
	signingKey nkeys.KeyPair
	jwtExpiry  time.Duration
	devMode    bool
}
```

- [ ] **Step 4: Implement dev-mode branch in HandleAuth**

At the top of `HandleAuth` in `auth-service/handler.go`, add the dev-mode branch before the existing OIDC flow:

```go
func (h *AuthHandler) HandleAuth(c *gin.Context) {
	if h.devMode {
		h.handleDevAuth(c)
		return
	}

	// ... existing OIDC flow unchanged ...
}

func (h *AuthHandler) handleDevAuth(c *gin.Context) {
	var req devAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "account and natsPublicKey are required"})
		return
	}

	if !nkeys.IsValidPublicUserKey(req.NATSPublicKey) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid natsPublicKey format"})
		return
	}

	natsJWT, err := h.signNATSJWT(req.NATSPublicKey, req.Account)
	if err != nil {
		slog.Error("nats jwt signing failed", "error", err, "account", req.Account)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate NATS token"})
		return
	}

	slog.Debug("dev auth success", "account", req.Account)

	c.JSON(http.StatusOK, authResponse{
		NATSJWT: natsJWT,
		UserInfo: userInfoResp{
			Email:   req.Account + "@dev.local",
			Account: req.Account,
			EngName: req.Account,
		},
	})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run:
```bash
make test SERVICE=auth-service
```
Expected: ALL PASS — both dev-mode tests and all existing tests.

- [ ] **Step 6: Update main.go config to support DEV_MODE**

In `auth-service/main.go`, update the config struct:

```go
type config struct {
	Port           string        `env:"PORT"                  envDefault:"8080"`
	DevMode        bool          `env:"DEV_MODE"              envDefault:"false"`
	AuthSigningKey string        `env:"AUTH_SIGNING_KEY,required"`
	NATSJWTExpiry  time.Duration `env:"NATS_JWT_EXPIRY"       envDefault:"2h"`

	// OIDC settings — required when DEV_MODE is false.
	OIDCIssuerURL string `env:"OIDC_ISSUER_URL"`
	OIDCAudience  string `env:"OIDC_AUDIENCE"`
	OIDCVerifyAZP bool   `env:"OIDC_VERIFY_AZP"           envDefault:"false"`
	TLSSkipVerify bool   `env:"TLS_SKIP_VERIFY"            envDefault:"false"`
}
```

Note: Remove `,required` from `OIDCIssuerURL` and `OIDCAudience` since they're only needed when `DevMode=false`.

Update the `run()` function to conditionally initialize OIDC and set devMode:

```go
func run() error {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	signingKP, err := nkeys.FromSeed([]byte(cfg.AuthSigningKey))
	if err != nil {
		return fmt.Errorf("parse signing key: %w", err)
	}

	var handler *AuthHandler

	if cfg.DevMode {
		slog.Info("dev mode enabled — OIDC validation disabled")
		handler = NewAuthHandler(nil, signingKP, cfg.NATSJWTExpiry)
		handler.devMode = true
	} else {
		if cfg.OIDCIssuerURL == "" || cfg.OIDCAudience == "" {
			return fmt.Errorf("OIDC_ISSUER_URL and OIDC_AUDIENCE are required when DEV_MODE is false")
		}

		ctx := context.Background()
		oidcValidator, err := pkgoidc.NewValidator(ctx, pkgoidc.Config{
			IssuerURL:     cfg.OIDCIssuerURL,
			Audience:      cfg.OIDCAudience,
			VerifyAZP:     cfg.OIDCVerifyAZP,
			TLSSkipVerify: cfg.TLSSkipVerify,
		})
		if err != nil {
			return fmt.Errorf("create oidc validator: %w", err)
		}
		slog.Info("oidc validator initialized", "issuer", cfg.OIDCIssuerURL)
		handler = NewAuthHandler(oidcValidator, signingKP, cfg.NATSJWTExpiry)
	}

	// ... rest unchanged (gin setup, server start, shutdown) ...
}
```

- [ ] **Step 7: Run all auth-service tests**

Run:
```bash
make test SERVICE=auth-service
```
Expected: ALL PASS.

- [ ] **Step 8: Run lint**

Run:
```bash
make lint
```
Expected: No errors.

- [ ] **Step 9: Commit**

```bash
git add auth-service/handler.go auth-service/handler_test.go auth-service/main.go
git commit -m "feat(auth-service): add DEV_MODE bypass for local development"
```

---

## Task 3: NATS context provider

**Files:**
- Create: `chat-frontend/src/context/NatsContext.jsx`

This is the core integration layer. It manages the NATS WebSocket connection, exposes `connect`, `request`, `publish`, `subscribe`, and `disconnect` via React context.

- [ ] **Step 1: Create NatsContext.jsx**

```jsx
import { createContext, useContext, useRef, useState, useCallback } from 'react'
import { connect as natsConnect, StringCodec } from 'nats.ws'

const NatsContext = createContext(null)

const sc = StringCodec()

export function NatsProvider({ children }) {
  const ncRef = useRef(null)
  const [connected, setConnected] = useState(false)
  const [user, setUser] = useState(null)
  const [error, setError] = useState(null)

  const authUrl = import.meta.env.VITE_AUTH_URL || 'http://localhost:8080'
  const natsUrl = import.meta.env.VITE_NATS_URL || 'ws://localhost:4223'

  const connectToNats = useCallback(async (account, siteId) => {
    setError(null)

    // 1. Authenticate with auth-service (dev mode)
    const { createUser } = await import('nkeys.js')
    const nkey = createUser()
    const natsPublicKey = new TextDecoder().decode(nkey.getPublicKey())

    const authResp = await fetch(`${authUrl}/auth`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ account, natsPublicKey }),
    })

    if (!authResp.ok) {
      const body = await authResp.json().catch(() => ({}))
      throw new Error(body.error || `Auth failed: ${authResp.status}`)
    }

    const { natsJwt, user: userInfo } = await authResp.json()

    // 2. Connect to NATS via WebSocket with JWT
    const nc = await natsConnect({
      servers: natsUrl,
      authenticator: {
        authenticate(nonce) {
          const sig = nkey.sign(new TextEncoder().encode(nonce))
          return { nkey: natsPublicKey, sig, jwt: natsJwt }
        },
      },
    })

    ncRef.current = nc
    setUser({ ...userInfo, siteId })
    setConnected(true)

    // Handle unexpected disconnection
    nc.closed().then((err) => {
      if (err) {
        setError(`Disconnected: ${err.message}`)
      }
      setConnected(false)
    })
  }, [authUrl, natsUrl])

  const request = useCallback(async (subject, data = {}) => {
    if (!ncRef.current) throw new Error('Not connected')
    const payload = sc.encode(JSON.stringify(data))
    const resp = await ncRef.current.request(subject, payload, { timeout: 5000 })
    const parsed = JSON.parse(sc.decode(resp.data))
    if (parsed.error) throw new Error(parsed.error)
    return parsed
  }, [])

  const publish = useCallback((subject, data = {}) => {
    if (!ncRef.current) throw new Error('Not connected')
    const payload = sc.encode(JSON.stringify(data))
    ncRef.current.publish(subject, payload)
  }, [])

  const subscribe = useCallback((subject, callback) => {
    if (!ncRef.current) throw new Error('Not connected')
    const sub = ncRef.current.subscribe(subject)
    ;(async () => {
      for await (const msg of sub) {
        try {
          const data = JSON.parse(sc.decode(msg.data))
          callback(data)
        } catch {
          // skip malformed messages
        }
      }
    })()
    return sub
  }, [])

  const disconnect = useCallback(async () => {
    if (ncRef.current) {
      await ncRef.current.drain()
      ncRef.current = null
    }
    setConnected(false)
    setUser(null)
  }, [])

  return (
    <NatsContext.Provider value={{
      connected, user, error,
      connect: connectToNats, request, publish, subscribe, disconnect,
    }}>
      {children}
    </NatsContext.Provider>
  )
}

export function useNats() {
  const ctx = useContext(NatsContext)
  if (!ctx) throw new Error('useNats must be used within NatsProvider')
  return ctx
}
```

- [ ] **Step 2: Verify build**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds (NatsContext is created but not yet imported by App).

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/context/NatsContext.jsx
git commit -m "feat(chat-frontend): add NATS context provider with connect/request/publish/subscribe"
```

---

## Task 4: Login page

**Files:**
- Create: `chat-frontend/src/pages/LoginPage.jsx`
- Modify: `chat-frontend/src/App.jsx`
- Modify: `chat-frontend/src/styles/index.css`

- [ ] **Step 1: Create LoginPage.jsx**

```jsx
import { useState } from 'react'
import { useNats } from '../context/NatsContext'

export default function LoginPage() {
  const { connect, error: natsError } = useNats()
  const defaultSiteId = import.meta.env.VITE_DEFAULT_SITE_ID || 'site-A'

  const [account, setAccount] = useState('')
  const [siteId, setSiteId] = useState(defaultSiteId)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!account.trim()) return

    setLoading(true)
    setError(null)
    try {
      await connect(account.trim(), siteId.trim())
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="login-page">
      <form className="login-form" onSubmit={handleSubmit}>
        <h1>Chat</h1>
        <p className="login-subtitle">Dev Mode Login</p>

        <label htmlFor="account">Account</label>
        <input
          id="account"
          type="text"
          value={account}
          onChange={(e) => setAccount(e.target.value)}
          placeholder="e.g. alice"
          autoFocus
          disabled={loading}
        />

        <label htmlFor="siteId">Site ID</label>
        <input
          id="siteId"
          type="text"
          value={siteId}
          onChange={(e) => setSiteId(e.target.value)}
          disabled={loading}
        />

        <button type="submit" disabled={loading || !account.trim()}>
          {loading ? 'Connecting...' : 'Connect'}
        </button>

        {(error || natsError) && (
          <div className="login-error">{error || natsError}</div>
        )}
      </form>
    </div>
  )
}
```

- [ ] **Step 2: Update App.jsx to wire NatsProvider and LoginPage**

Replace `chat-frontend/src/App.jsx`:

```jsx
import { NatsProvider, useNats } from './context/NatsContext'
import LoginPage from './pages/LoginPage'

function AppContent() {
  const { connected } = useNats()

  if (!connected) {
    return <LoginPage />
  }

  return <div className="app">Connected! (Chat page coming next)</div>
}

export default function App() {
  return (
    <NatsProvider>
      <AppContent />
    </NatsProvider>
  )
}
```

- [ ] **Step 3: Add login styles to index.css**

Append to `chat-frontend/src/styles/index.css`:

```css
/* Login */
.login-page {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  background: #f5f5f5;
}

.login-form {
  background: #fff;
  padding: 2rem;
  border-radius: 8px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  width: 320px;
}

.login-form h1 {
  margin-bottom: 0.25rem;
}

.login-subtitle {
  color: #666;
  margin-bottom: 1.5rem;
  font-size: 13px;
}

.login-form label {
  display: block;
  margin-bottom: 0.25rem;
  font-weight: 500;
  font-size: 13px;
  color: #444;
}

.login-form input {
  display: block;
  width: 100%;
  padding: 0.5rem;
  margin-bottom: 1rem;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-size: 14px;
}

.login-form input:focus {
  outline: none;
  border-color: #4a9eff;
  box-shadow: 0 0 0 2px rgba(74, 158, 255, 0.2);
}

.login-form button {
  width: 100%;
  padding: 0.6rem;
  background: #4a9eff;
  color: #fff;
  border: none;
  border-radius: 4px;
  font-size: 14px;
  cursor: pointer;
}

.login-form button:hover:not(:disabled) {
  background: #3a8eef;
}

.login-form button:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.login-error {
  margin-top: 1rem;
  padding: 0.5rem;
  background: #fee;
  color: #c00;
  border-radius: 4px;
  font-size: 13px;
}
```

- [ ] **Step 4: Verify build**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/pages/LoginPage.jsx chat-frontend/src/App.jsx \
  chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): add login page and wire NatsProvider"
```

---

## Task 5: Room list component

**Files:**
- Create: `chat-frontend/src/components/RoomList.jsx`
- Modify: `chat-frontend/src/styles/index.css`

Fetches rooms via NATS request/reply on mount, subscribes to `subscription.update` and `room.metadata.update` for real-time changes. Calls `onSelectRoom(room)` when clicked.

- [ ] **Step 1: Create RoomList.jsx**

```jsx
import { useState, useEffect, useRef } from 'react'
import { useNats } from '../context/NatsContext'

export default function RoomList({ selectedRoomId, onSelectRoom }) {
  const { user, request, subscribe } = useNats()
  const [rooms, setRooms] = useState([])
  const [error, setError] = useState(null)
  const subsRef = useRef([])

  useEffect(() => {
    if (!user) return

    const account = user.account

    // Fetch initial room list
    request(`chat.user.${account}.request.rooms.list`, {})
      .then((resp) => {
        const sorted = (resp.rooms || []).sort(
          (a, b) => new Date(b.lastMsgAt) - new Date(a.lastMsgAt)
        )
        setRooms(sorted)
      })
      .catch((err) => setError(err.message))

    // Subscribe to subscription updates (room added/removed)
    const subUpdate = subscribe(
      `chat.user.${account}.event.subscription.update`,
      (evt) => {
        if (evt.action === 'added') {
          // Fetch the full room details and add to list
          request(`chat.user.${account}.request.rooms.get.${evt.subscription.roomId}`, {})
            .then((room) => {
              setRooms((prev) => {
                if (prev.some((r) => r.id === room.id)) return prev
                return [room, ...prev]
              })
            })
            .catch(() => {})
        } else if (evt.action === 'removed') {
          setRooms((prev) => prev.filter((r) => r.id !== evt.subscription.roomId))
        }
      }
    )

    // Subscribe to room metadata updates (name, userCount, lastMsgAt)
    const metaUpdate = subscribe(
      `chat.user.${account}.event.room.metadata.update`,
      (evt) => {
        setRooms((prev) => {
          const updated = prev.map((r) =>
            r.id === evt.roomId
              ? { ...r, name: evt.name, userCount: evt.userCount, lastMsgAt: evt.lastMsgAt }
              : r
          )
          return updated.sort(
            (a, b) => new Date(b.lastMsgAt) - new Date(a.lastMsgAt)
          )
        })
      }
    )

    subsRef.current = [subUpdate, metaUpdate]

    return () => {
      subsRef.current.forEach((s) => s.unsubscribe())
      subsRef.current = []
    }
  }, [user, request, subscribe])

  return (
    <div className="room-list">
      <div className="room-list-header">Rooms</div>
      {error && <div className="room-list-error">{error}</div>}
      <div className="room-list-items">
        {rooms.map((room) => (
          <div
            key={room.id}
            className={`room-item ${room.id === selectedRoomId ? 'room-item-selected' : ''}`}
            onClick={() => onSelectRoom(room)}
          >
            <span className="room-name">
              {room.type === 'dm' ? '@ ' : '# '}{room.name}
            </span>
            <span className="room-meta">{room.userCount}</span>
          </div>
        ))}
        {rooms.length === 0 && !error && (
          <div className="room-list-empty">No rooms yet</div>
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Add room list styles to index.css**

Append to `chat-frontend/src/styles/index.css`:

```css
/* Room List */
.room-list {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: #2b2d31;
  color: #b5bac1;
}

.room-list-header {
  padding: 1rem;
  font-weight: 600;
  font-size: 13px;
  text-transform: uppercase;
  letter-spacing: 0.02em;
  color: #949ba4;
}

.room-list-items {
  flex: 1;
  overflow-y: auto;
}

.room-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.5rem 1rem;
  cursor: pointer;
  border-radius: 4px;
  margin: 0 0.5rem;
}

.room-item:hover {
  background: #35373c;
  color: #dbdee1;
}

.room-item-selected {
  background: #404249;
  color: #fff;
}

.room-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.room-meta {
  font-size: 12px;
  color: #949ba4;
  flex-shrink: 0;
  margin-left: 0.5rem;
}

.room-list-empty {
  padding: 1rem;
  font-size: 13px;
  color: #949ba4;
  text-align: center;
}

.room-list-error {
  padding: 0.5rem 1rem;
  font-size: 12px;
  color: #f38ba8;
}
```

- [ ] **Step 3: Verify build**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/RoomList.jsx chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): add RoomList component with real-time updates"
```

---

## Task 6: Message area with history loading

**Files:**
- Create: `chat-frontend/src/components/MessageArea.jsx`
- Modify: `chat-frontend/src/styles/index.css`

Displays messages for the selected room. On room change: subscribes to `chat.room.{roomID}.event`, loads history via `msg.history` request/reply. Incoming `RoomEvent` with `type: "new_message"` appends the message. Auto-scrolls to bottom.

- [ ] **Step 1: Create MessageArea.jsx**

```jsx
import { useState, useEffect, useRef } from 'react'
import { useNats } from '../context/NatsContext'

function formatTime(dateStr) {
  const d = new Date(dateStr)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function senderName(msg) {
  if (msg.sender) {
    return msg.sender.engName || msg.sender.account || msg.sender.userId || 'Unknown'
  }
  return msg.userAccount || msg.userId || 'Unknown'
}

function messageContent(msg) {
  return msg.content || msg.msg || ''
}

function messageId(msg) {
  return msg.id || msg.messageId
}

export default function MessageArea({ room }) {
  const { user, request, subscribe } = useNats()
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const bottomRef = useRef(null)
  const subRef = useRef(null)

  useEffect(() => {
    if (!room || !user) return

    const account = user.account
    const siteId = user.siteId

    setMessages([])
    setError(null)
    setLoading(true)

    // Unsubscribe from previous room
    if (subRef.current) {
      subRef.current.unsubscribe()
      subRef.current = null
    }

    // Subscribe to room events for real-time messages
    const sub = subscribe(`chat.room.${room.id}.event`, (evt) => {
      if (evt.type === 'new_message' && evt.message) {
        setMessages((prev) => {
          const id = messageId(evt.message)
          if (prev.some((m) => messageId(m) === id)) return prev
          return [...prev, evt.message]
        })
      }
    })
    subRef.current = sub

    // Load message history
    request(`chat.user.${account}.request.room.${room.id}.${siteId}.msg.history`, {
      limit: 50,
    })
      .then((resp) => {
        // History comes in descending order — reverse for display
        const hist = (resp.messages || []).reverse()
        setMessages(hist)
      })
      .catch((err) => setError(err.message))
      .finally(() => setLoading(false))

    return () => {
      if (subRef.current) {
        subRef.current.unsubscribe()
        subRef.current = null
      }
    }
  }, [room, user, request, subscribe])

  // Auto-scroll to bottom when messages change
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  if (!room) {
    return (
      <div className="message-area">
        <div className="message-area-empty">Select a room to start chatting</div>
      </div>
    )
  }

  return (
    <div className="message-area">
      <div className="message-area-header">
        <span className="message-area-room-name">
          {room.type === 'dm' ? '@ ' : '# '}{room.name}
        </span>
        <span className="message-area-members">{room.userCount} members</span>
      </div>
      <div className="message-list">
        {loading && <div className="message-loading">Loading messages...</div>}
        {error && <div className="message-error">{error}</div>}
        {messages.map((msg) => (
          <div key={messageId(msg)} className="message">
            <span className="message-sender">{senderName(msg)}</span>
            <span className="message-time">{formatTime(msg.createdAt)}</span>
            <div className="message-content">{messageContent(msg)}</div>
          </div>
        ))}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Add message area styles to index.css**

Append to `chat-frontend/src/styles/index.css`:

```css
/* Message Area */
.message-area {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: #313338;
}

.message-area-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #949ba4;
}

.message-area-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid #3f4147;
  background: #313338;
  flex-shrink: 0;
}

.message-area-room-name {
  font-weight: 600;
  color: #f2f3f5;
}

.message-area-members {
  font-size: 12px;
  color: #949ba4;
}

.message-list {
  flex: 1;
  overflow-y: auto;
  padding: 1rem;
}

.message {
  margin-bottom: 1rem;
}

.message-sender {
  font-weight: 600;
  color: #f2f3f5;
  margin-right: 0.5rem;
}

.message-time {
  font-size: 11px;
  color: #949ba4;
}

.message-content {
  color: #dbdee1;
  margin-top: 0.25rem;
  line-height: 1.4;
  white-space: pre-wrap;
  word-break: break-word;
}

.message-loading,
.message-error {
  padding: 0.5rem;
  text-align: center;
  font-size: 13px;
  color: #949ba4;
}

.message-error {
  color: #f38ba8;
}
```

- [ ] **Step 3: Verify build**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/MessageArea.jsx chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): add MessageArea with history loading and real-time events"
```

---

## Task 7: Message input component

**Files:**
- Create: `chat-frontend/src/components/MessageInput.jsx`
- Modify: `chat-frontend/src/styles/index.css`

Text input at the bottom of the message area. On submit (Enter or click Send), publishes a `SendMessageRequest` to the user's message send subject. Generates UUIDs for message ID and request ID.

- [ ] **Step 1: Create MessageInput.jsx**

```jsx
import { useState } from 'react'
import { v4 as uuidv4 } from 'uuid'
import { useNats } from '../context/NatsContext'

export default function MessageInput({ room }) {
  const { user, publish } = useNats()
  const [text, setText] = useState('')

  const handleSubmit = (e) => {
    e.preventDefault()
    if (!text.trim() || !room || !user) return

    const account = user.account
    const siteId = user.siteId
    const subject = `chat.user.${account}.room.${room.id}.${siteId}.msg.send`

    publish(subject, {
      id: uuidv4(),
      content: text.trim(),
      requestId: uuidv4(),
    })

    setText('')
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSubmit(e)
    }
  }

  return (
    <form className="message-input" onSubmit={handleSubmit}>
      <input
        type="text"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder={room ? `Message #${room.name}` : 'Select a room...'}
        disabled={!room}
      />
      <button type="submit" disabled={!room || !text.trim()}>
        Send
      </button>
    </form>
  )
}
```

- [ ] **Step 2: Add message input styles to index.css**

Append to `chat-frontend/src/styles/index.css`:

```css
/* Message Input */
.message-input {
  display: flex;
  padding: 0.75rem 1rem;
  background: #313338;
  border-top: 1px solid #3f4147;
  gap: 0.5rem;
  flex-shrink: 0;
}

.message-input input {
  flex: 1;
  padding: 0.6rem 0.75rem;
  background: #383a40;
  border: none;
  border-radius: 8px;
  color: #dbdee1;
  font-size: 14px;
}

.message-input input:focus {
  outline: none;
  background: #404249;
}

.message-input input::placeholder {
  color: #6d6f78;
}

.message-input button {
  padding: 0.6rem 1.2rem;
  background: #5865f2;
  color: #fff;
  border: none;
  border-radius: 8px;
  font-size: 14px;
  cursor: pointer;
}

.message-input button:hover:not(:disabled) {
  background: #4752c4;
}

.message-input button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

- [ ] **Step 3: Verify build**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/MessageInput.jsx chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): add MessageInput component"
```

---

## Task 8: Create room dialog

**Files:**
- Create: `chat-frontend/src/components/CreateRoomDialog.jsx`
- Modify: `chat-frontend/src/styles/index.css`

A simple dialog/modal with fields for room name, type (group/DM dropdown), and comma-separated member accounts. Sends a NATS request to create the room.

- [ ] **Step 1: Create CreateRoomDialog.jsx**

```jsx
import { useState } from 'react'
import { useNats } from '../context/NatsContext'

export default function CreateRoomDialog({ onClose, onCreated }) {
  const { user, request } = useNats()
  const [name, setName] = useState('')
  const [roomType, setRoomType] = useState('group')
  const [members, setMembers] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!name.trim() || !user) return

    setLoading(true)
    setError(null)

    const account = user.account
    const siteId = user.siteId

    const memberList = members
      .split(',')
      .map((m) => m.trim())
      .filter(Boolean)

    try {
      const room = await request(`chat.user.${account}.request.rooms.create`, {
        name: name.trim(),
        type: roomType,
        createdByAccount: account,
        siteId: siteId,
        members: memberList,
      })
      onCreated(room)
      onClose()
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div className="dialog" onClick={(e) => e.stopPropagation()}>
        <h2>Create Room</h2>
        <form onSubmit={handleSubmit}>
          <label htmlFor="room-name">Room Name</label>
          <input
            id="room-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. frontend-team"
            autoFocus
            disabled={loading}
          />

          <label htmlFor="room-type">Type</label>
          <select
            id="room-type"
            value={roomType}
            onChange={(e) => setRoomType(e.target.value)}
            disabled={loading}
          >
            <option value="group">Group</option>
            <option value="dm">DM</option>
          </select>

          <label htmlFor="room-members">Members (comma-separated accounts)</label>
          <input
            id="room-members"
            type="text"
            value={members}
            onChange={(e) => setMembers(e.target.value)}
            placeholder="e.g. bob, charlie"
            disabled={loading}
          />

          {error && <div className="dialog-error">{error}</div>}

          <div className="dialog-actions">
            <button type="button" className="dialog-cancel" onClick={onClose} disabled={loading}>
              Cancel
            </button>
            <button type="submit" disabled={loading || !name.trim()}>
              {loading ? 'Creating...' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Add dialog styles to index.css**

Append to `chat-frontend/src/styles/index.css`:

```css
/* Dialog */
.dialog-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}

.dialog {
  background: #313338;
  border-radius: 8px;
  padding: 1.5rem;
  width: 400px;
  max-width: 90vw;
}

.dialog h2 {
  color: #f2f3f5;
  margin-bottom: 1rem;
  font-size: 18px;
}

.dialog label {
  display: block;
  margin-bottom: 0.25rem;
  font-weight: 500;
  font-size: 13px;
  color: #b5bac1;
}

.dialog input,
.dialog select {
  display: block;
  width: 100%;
  padding: 0.5rem;
  margin-bottom: 1rem;
  background: #1e1f22;
  border: 1px solid #3f4147;
  border-radius: 4px;
  color: #dbdee1;
  font-size: 14px;
}

.dialog input:focus,
.dialog select:focus {
  outline: none;
  border-color: #5865f2;
}

.dialog-error {
  padding: 0.5rem;
  margin-bottom: 1rem;
  background: rgba(243, 139, 168, 0.1);
  color: #f38ba8;
  border-radius: 4px;
  font-size: 13px;
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
}

.dialog-actions button {
  padding: 0.5rem 1rem;
  border: none;
  border-radius: 4px;
  font-size: 14px;
  cursor: pointer;
  background: #5865f2;
  color: #fff;
}

.dialog-actions button:hover:not(:disabled) {
  background: #4752c4;
}

.dialog-actions button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.dialog-cancel {
  background: transparent !important;
  color: #b5bac1 !important;
}

.dialog-cancel:hover:not(:disabled) {
  background: #404249 !important;
}
```

- [ ] **Step 3: Verify build**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/components/CreateRoomDialog.jsx chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): add CreateRoomDialog component"
```

---

## Task 9: Chat page layout (wiring all components)

**Files:**
- Create: `chat-frontend/src/pages/ChatPage.jsx`
- Modify: `chat-frontend/src/App.jsx`
- Modify: `chat-frontend/src/styles/index.css`

ChatPage is the main layout shell. It renders the RoomList sidebar on the left, MessageArea + MessageInput on the right, and a header bar showing the logged-in user. The "Create Room" button opens the CreateRoomDialog.

- [ ] **Step 1: Create ChatPage.jsx**

```jsx
import { useState } from 'react'
import { useNats } from '../context/NatsContext'
import RoomList from '../components/RoomList'
import MessageArea from '../components/MessageArea'
import MessageInput from '../components/MessageInput'
import CreateRoomDialog from '../components/CreateRoomDialog'

export default function ChatPage() {
  const { user, disconnect } = useNats()
  const [selectedRoom, setSelectedRoom] = useState(null)
  const [showCreateRoom, setShowCreateRoom] = useState(false)

  return (
    <div className="chat-layout">
      <div className="chat-header">
        <span className="chat-header-title">Chat</span>
        <span className="chat-header-user">
          {user?.account} &middot; {user?.siteId}
        </span>
        <button className="chat-header-logout" onClick={disconnect}>
          Logout
        </button>
      </div>
      <div className="chat-body">
        <div className="chat-sidebar">
          <RoomList
            selectedRoomId={selectedRoom?.id}
            onSelectRoom={setSelectedRoom}
          />
          <button
            className="create-room-btn"
            onClick={() => setShowCreateRoom(true)}
          >
            + Create Room
          </button>
        </div>
        <div className="chat-main">
          <MessageArea room={selectedRoom} />
          <MessageInput room={selectedRoom} />
        </div>
      </div>
      {showCreateRoom && (
        <CreateRoomDialog
          onClose={() => setShowCreateRoom(false)}
          onCreated={(room) => setSelectedRoom(room)}
        />
      )}
    </div>
  )
}
```

- [ ] **Step 2: Update App.jsx to render ChatPage**

Replace `chat-frontend/src/App.jsx`:

```jsx
import { NatsProvider, useNats } from './context/NatsContext'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'

function AppContent() {
  const { connected } = useNats()

  if (!connected) {
    return <LoginPage />
  }

  return <ChatPage />
}

export default function App() {
  return (
    <NatsProvider>
      <AppContent />
    </NatsProvider>
  )
}
```

- [ ] **Step 3: Add chat layout styles to index.css**

Append to `chat-frontend/src/styles/index.css`:

```css
/* Chat Layout */
.chat-layout {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.chat-header {
  display: flex;
  align-items: center;
  padding: 0.5rem 1rem;
  background: #1e1f22;
  color: #f2f3f5;
  gap: 1rem;
  flex-shrink: 0;
}

.chat-header-title {
  font-weight: 700;
  font-size: 16px;
}

.chat-header-user {
  font-size: 13px;
  color: #949ba4;
  margin-left: auto;
}

.chat-header-logout {
  padding: 0.3rem 0.75rem;
  background: transparent;
  border: 1px solid #4e5058;
  border-radius: 4px;
  color: #b5bac1;
  font-size: 12px;
  cursor: pointer;
}

.chat-header-logout:hover {
  background: #404249;
  color: #f2f3f5;
}

.chat-body {
  display: flex;
  flex: 1;
  overflow: hidden;
}

.chat-sidebar {
  display: flex;
  flex-direction: column;
  width: 240px;
  flex-shrink: 0;
}

.chat-sidebar .room-list {
  flex: 1;
  overflow-y: auto;
}

.create-room-btn {
  padding: 0.6rem;
  margin: 0.5rem;
  background: transparent;
  border: 1px dashed #4e5058;
  border-radius: 4px;
  color: #949ba4;
  font-size: 13px;
  cursor: pointer;
  text-align: center;
  flex-shrink: 0;
}

.create-room-btn:hover {
  border-color: #b5bac1;
  color: #b5bac1;
}

.chat-main {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
}

.chat-main .message-area {
  flex: 1;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.chat-main .message-list {
  flex: 1;
  overflow-y: auto;
}
```

- [ ] **Step 4: Verify build**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/App.jsx \
  chat-frontend/src/styles/index.css
git commit -m "feat(chat-frontend): add ChatPage layout wiring all components"
```

---

## Task 10: Docker and NATS WebSocket setup

**Files:**
- Create: `chat-frontend/deploy/Dockerfile`
- Create: `chat-frontend/deploy/docker-compose.yml`
- Create: `chat-frontend/deploy/nats.conf`

The Dockerfile builds the React app and serves it via nginx. The docker-compose.yml runs the frontend, NATS (with WebSocket on port 4223), and auth-service in dev mode. The nats.conf enables JetStream and a WebSocket listener.

- [ ] **Step 1: Create nats.conf with WebSocket support**

```conf
# NATS server config for chat-frontend local development
listen: 0.0.0.0:4222

http_port: 8222

jetstream {
  store_dir: /data/jetstream
}

websocket {
  listen: "0.0.0.0:4223"
  no_tls: true
}
```

- [ ] **Step 2: Create Dockerfile**

```dockerfile
FROM node:22-alpine AS builder
WORKDIR /app
COPY chat-frontend/package.json chat-frontend/package-lock.json ./
RUN npm ci
COPY chat-frontend/ .
RUN npm run build

FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
```

- [ ] **Step 3: Create docker-compose.yml**

```yaml
services:
  frontend:
    build:
      context: ../..
      dockerfile: chat-frontend/deploy/Dockerfile
    ports:
      - "3000:80"
    depends_on:
      - nats
      - auth-service

  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "4223:4223"
      - "8222:8222"
    volumes:
      - ./nats.conf:/etc/nats/nats.conf:ro
      - nats-data:/data/jetstream
    command: ["-c", "/etc/nats/nats.conf"]

  auth-service:
    build:
      context: ../..
      dockerfile: auth-service/deploy/Dockerfile
    ports:
      - "8080:8080"
    environment:
      DEV_MODE: "true"
      AUTH_SIGNING_KEY: "${AUTH_SIGNING_KEY}"
    depends_on:
      - nats

volumes:
  nats-data:
```

Note: `AUTH_SIGNING_KEY` must be provided via a `.env` file or shell env variable. This is the NATS account NKey seed used for signing JWTs.

- [ ] **Step 4: Verify the Dockerfile builds**

Run:
```bash
cd chat-frontend && npx vite build
```
Expected: Build succeeds (verifies the frontend compiles before Docker build).

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/deploy/Dockerfile chat-frontend/deploy/docker-compose.yml \
  chat-frontend/deploy/nats.conf
git commit -m "feat(chat-frontend): add Docker setup with NATS WebSocket config"
```

---

## Task 11: End-to-end smoke test

This is a manual verification step. No new files.

- [ ] **Step 1: Start the dev environment**

Run docker-compose from the chat-frontend deploy directory:
```bash
cd chat-frontend/deploy && docker compose up --build -d
```
Expected: All three services start (frontend, nats, auth-service).

Alternatively, for faster iteration during development, run the Vite dev server directly:
```bash
cd chat-frontend && npm run dev
```
And ensure NATS and auth-service are running separately.

- [ ] **Step 2: Verify login**

1. Open `http://localhost:3000` in a browser
2. Enter account name "alice" and site ID "site-A"
3. Click "Connect"

Expected: The login form disappears and the chat layout appears.

- [ ] **Step 3: Verify room creation**

1. Click "+ Create Room"
2. Enter name "test-room", type "group"
3. Click "Create"

Expected: The room appears in the sidebar and is auto-selected.

- [ ] **Step 4: Verify sending and receiving messages**

1. Open a second browser tab, login as "bob"
2. In bob's tab, create or join the same room
3. Send a message as alice: "Hello from alice"
4. Observe it appears in both tabs

Expected: Messages appear in real-time in both tabs.

- [ ] **Step 5: Verify message history**

1. Refresh alice's tab
2. Re-login and select the same room

Expected: Previous messages load from history.

- [ ] **Step 6: Commit final state (if any fixes were needed)**

```bash
git add -A
git commit -m "fix(chat-frontend): address issues found during smoke test"
```
