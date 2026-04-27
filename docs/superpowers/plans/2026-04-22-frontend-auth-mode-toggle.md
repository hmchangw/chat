# Frontend Auth Mode Toggle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a build-time `VITE_AUTH_MODE` toggle to `chat-frontend` so it can log in against the auth-service in either dev mode (current behavior) or real Keycloak OIDC SSO, without touching the backend.

**Architecture:** Introduce a discriminated-input `connect` API in `NatsContext` that accepts either `{account, ...}` (dev body) or `{ssoToken, ...}` (OIDC body) and posts to the existing `POST /auth`. A new `src/lib/oidc.js` wraps `oidc-client-ts` for the Authorization Code + PKCE flow. `LoginPage` branches its UI on the mode; `App.jsx` completes the OIDC redirect callback on initial mount.

**Tech Stack:** React 19, Vite 6, `oidc-client-ts` for OIDC, `vitest` for unit tests (new dev dependency — the frontend currently has no test runner), pure-logic unit tests only (no DOM/component tests in this plan).

**Spec:** `docs/superpowers/specs/2026-04-22-frontend-auth-mode-toggle-design.md`

---

## File Structure

**New files:**
- `chat-frontend/vitest.config.js` — minimal vitest config, Node environment
- `chat-frontend/src/lib/authRequestBody.js` — pure function that builds the `/auth` request body from discriminated input
- `chat-frontend/src/lib/authRequestBody.test.js` — unit tests
- `chat-frontend/src/lib/authMode.js` — reads and validates `VITE_AUTH_MODE`
- `chat-frontend/src/lib/authMode.test.js` — unit tests
- `chat-frontend/src/lib/oidcCallback.js` — pure helpers for OIDC callback URL detection and cleanup
- `chat-frontend/src/lib/oidcCallback.test.js` — unit tests
- `chat-frontend/src/lib/oidc.js` — `oidc-client-ts` `UserManager` singleton + helpers
- `chat-frontend/README.md` — frontend-specific docs (may already exist; created if not)

**Modified files:**
- `chat-frontend/package.json` — add `oidc-client-ts` + `vitest`, add `test` script
- `chat-frontend/src/context/NatsContext.jsx` — new `connect` input shape, use `authRequestBody`
- `chat-frontend/src/pages/LoginPage.jsx` — branch UI on auth mode
- `chat-frontend/src/App.jsx` — complete OIDC callback on mount when in OIDC mode

**Not touched:** auth-service, NATS connection logic, any other service.

---

### Task 1: Install vitest and add the `test` script

**Files:**
- Modify: `chat-frontend/package.json`
- Create: `chat-frontend/vitest.config.js`

- [ ] **Step 1: Install vitest as a dev dependency**

Run from `chat-frontend/`:
```bash
cd chat-frontend
npm install --save-dev vitest@^2.1.0
```

Expected: `package.json` now lists `"vitest": "^2.1.0"` under `devDependencies`, `package-lock.json` updated.

- [ ] **Step 2: Add a `test` script to `package.json`**

In `chat-frontend/package.json`, under `"scripts"`, add a `"test"` entry:

```json
"scripts": {
  "dev": "vite",
  "build": "vite build",
  "preview": "vite preview",
  "test": "vitest run"
}
```

- [ ] **Step 3: Create `chat-frontend/vitest.config.js`**

```js
import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.{js,jsx}'],
  },
})
```

- [ ] **Step 4: Verify the runner starts**

Run from `chat-frontend/`:
```bash
npm test
```

Expected: vitest reports "No test files found" and exits 1. That's OK — it confirms the runner is wired up. We'll fix the exit code in the next task by adding the first real test.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/package.json chat-frontend/package-lock.json chat-frontend/vitest.config.js
git commit -m "chore(chat-frontend): add vitest for unit tests"
```

---

### Task 2: `authRequestBody` pure helper (TDD)

Pure function that turns discriminated input into the JSON body for `POST /auth`. Isolates the mode-specific shape so `NatsContext` stays simple.

**Files:**
- Create: `chat-frontend/src/lib/authRequestBody.js`
- Test: `chat-frontend/src/lib/authRequestBody.test.js`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/lib/authRequestBody.test.js`:

```js
import { describe, it, expect } from 'vitest'
import { buildAuthRequestBody } from './authRequestBody'

describe('buildAuthRequestBody', () => {
  it('builds dev body when given an account', () => {
    const body = buildAuthRequestBody({ account: 'alice', natsPublicKey: 'UABC' })
    expect(body).toEqual({ account: 'alice', natsPublicKey: 'UABC' })
  })

  it('builds oidc body when given an ssoToken', () => {
    const body = buildAuthRequestBody({ ssoToken: 'tok-123', natsPublicKey: 'UABC' })
    expect(body).toEqual({ ssoToken: 'tok-123', natsPublicKey: 'UABC' })
  })

  it('prefers ssoToken when both are provided', () => {
    const body = buildAuthRequestBody({
      account: 'alice',
      ssoToken: 'tok-123',
      natsPublicKey: 'UABC',
    })
    expect(body).toEqual({ ssoToken: 'tok-123', natsPublicKey: 'UABC' })
  })

  it('throws when neither account nor ssoToken is provided', () => {
    expect(() => buildAuthRequestBody({ natsPublicKey: 'UABC' })).toThrow(
      /account or ssoToken/,
    )
  })

  it('throws when natsPublicKey is missing', () => {
    expect(() => buildAuthRequestBody({ account: 'alice' })).toThrow(/natsPublicKey/)
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd chat-frontend && npm test
```

Expected: FAIL with "Cannot find module './authRequestBody'" or similar import error on all 5 tests.

- [ ] **Step 3: Write the minimal implementation**

Create `chat-frontend/src/lib/authRequestBody.js`:

```js
// buildAuthRequestBody constructs the JSON body for POST /auth from
// discriminated input. Exactly one of `account` (dev mode) or `ssoToken`
// (oidc mode) must be provided. If both are present, ssoToken wins.
export function buildAuthRequestBody({ account, ssoToken, natsPublicKey }) {
  if (!natsPublicKey) {
    throw new Error('natsPublicKey is required')
  }
  if (ssoToken) {
    return { ssoToken, natsPublicKey }
  }
  if (account) {
    return { account, natsPublicKey }
  }
  throw new Error('account or ssoToken is required')
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd chat-frontend && npm test
```

Expected: PASS, 5 tests in 1 file.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/authRequestBody.js chat-frontend/src/lib/authRequestBody.test.js
git commit -m "feat(chat-frontend): add authRequestBody helper for /auth body shape"
```

---

### Task 3: `authMode` env var reader (TDD)

Reads `VITE_AUTH_MODE` and validates it. Exposes `'dev' | 'oidc'`. Takes the env object as a parameter so tests don't have to mutate `import.meta.env`.

**Files:**
- Create: `chat-frontend/src/lib/authMode.js`
- Test: `chat-frontend/src/lib/authMode.test.js`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/lib/authMode.test.js`:

```js
import { describe, it, expect } from 'vitest'
import { resolveAuthMode } from './authMode'

describe('resolveAuthMode', () => {
  it('returns "dev" when VITE_AUTH_MODE is unset', () => {
    expect(resolveAuthMode({})).toBe('dev')
  })

  it('returns "dev" when VITE_AUTH_MODE is empty', () => {
    expect(resolveAuthMode({ VITE_AUTH_MODE: '' })).toBe('dev')
  })

  it('returns "dev" when VITE_AUTH_MODE is "dev"', () => {
    expect(resolveAuthMode({ VITE_AUTH_MODE: 'dev' })).toBe('dev')
  })

  it('returns "oidc" when VITE_AUTH_MODE is "oidc"', () => {
    expect(resolveAuthMode({ VITE_AUTH_MODE: 'oidc' })).toBe('oidc')
  })

  it('throws for any other value', () => {
    expect(() => resolveAuthMode({ VITE_AUTH_MODE: 'saml' })).toThrow(
      /VITE_AUTH_MODE must be "dev" or "oidc"/,
    )
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd chat-frontend && npm test
```

Expected: FAIL with module-not-found on the new test file.

- [ ] **Step 3: Write the implementation**

Create `chat-frontend/src/lib/authMode.js`:

```js
// resolveAuthMode returns the validated auth mode: 'dev' (default) or 'oidc'.
// Takes the env object explicitly so callers can pass import.meta.env and
// tests can pass a plain object.
export function resolveAuthMode(env) {
  const raw = env?.VITE_AUTH_MODE ?? ''
  if (raw === '' || raw === 'dev') return 'dev'
  if (raw === 'oidc') return 'oidc'
  throw new Error(`VITE_AUTH_MODE must be "dev" or "oidc" (got "${raw}")`)
}

// AUTH_MODE is the resolved mode for runtime use in the app.
export const AUTH_MODE = resolveAuthMode(import.meta.env)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd chat-frontend && npm test
```

Expected: PASS, all tests across `authRequestBody` and `authMode` green.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/authMode.js chat-frontend/src/lib/authMode.test.js
git commit -m "feat(chat-frontend): add VITE_AUTH_MODE resolver"
```

---

### Task 4: `oidcCallback` pure helpers (TDD)

Pure helpers that (a) detect whether a URL looks like an OIDC callback, (b) produce the cleaned-up URL to replace into history after the exchange. Keeps `oidc.js` thin.

**Files:**
- Create: `chat-frontend/src/lib/oidcCallback.js`
- Test: `chat-frontend/src/lib/oidcCallback.test.js`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/lib/oidcCallback.test.js`:

```js
import { describe, it, expect } from 'vitest'
import { isOidcCallbackUrl, stripOidcCallbackParams } from './oidcCallback'

describe('isOidcCallbackUrl', () => {
  it('returns true when both code and state are present', () => {
    expect(isOidcCallbackUrl('http://localhost:3000/?code=abc&state=xyz')).toBe(true)
  })

  it('returns true when code, state, and session_state are present', () => {
    const url = 'http://localhost:3000/?code=abc&state=xyz&session_state=s1'
    expect(isOidcCallbackUrl(url)).toBe(true)
  })

  it('returns true for error callbacks', () => {
    const url = 'http://localhost:3000/?error=access_denied&state=xyz'
    expect(isOidcCallbackUrl(url)).toBe(true)
  })

  it('returns false when only code is present without state', () => {
    expect(isOidcCallbackUrl('http://localhost:3000/?code=abc')).toBe(false)
  })

  it('returns false for a plain URL', () => {
    expect(isOidcCallbackUrl('http://localhost:3000/')).toBe(false)
  })
})

describe('stripOidcCallbackParams', () => {
  it('removes code, state, session_state, error, error_description', () => {
    const url =
      'http://localhost:3000/?code=abc&state=xyz&session_state=s1&error=e&error_description=d&keep=yes'
    expect(stripOidcCallbackParams(url)).toBe('http://localhost:3000/?keep=yes')
  })

  it('returns URL without query when no params remain', () => {
    const url = 'http://localhost:3000/?code=abc&state=xyz'
    expect(stripOidcCallbackParams(url)).toBe('http://localhost:3000/')
  })

  it('preserves the path', () => {
    const url = 'http://localhost:3000/chat?code=abc&state=xyz'
    expect(stripOidcCallbackParams(url)).toBe('http://localhost:3000/chat')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd chat-frontend && npm test
```

Expected: FAIL, module not found.

- [ ] **Step 3: Write the implementation**

Create `chat-frontend/src/lib/oidcCallback.js`:

```js
const CALLBACK_PARAMS = [
  'code',
  'state',
  'session_state',
  'iss',
  'error',
  'error_description',
]

// isOidcCallbackUrl returns true if the URL's query string looks like an
// OIDC Authorization Code callback (has state plus either code or error).
export function isOidcCallbackUrl(urlString) {
  const url = new URL(urlString)
  const hasState = url.searchParams.has('state')
  const hasCodeOrError =
    url.searchParams.has('code') || url.searchParams.has('error')
  return hasState && hasCodeOrError
}

// stripOidcCallbackParams returns the URL with all OIDC callback query params
// removed. Other query params are preserved.
export function stripOidcCallbackParams(urlString) {
  const url = new URL(urlString)
  for (const key of CALLBACK_PARAMS) {
    url.searchParams.delete(key)
  }
  const query = url.searchParams.toString()
  return query ? `${url.origin}${url.pathname}?${query}` : `${url.origin}${url.pathname}`
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd chat-frontend && npm test
```

Expected: PASS, all tests green.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/lib/oidcCallback.js chat-frontend/src/lib/oidcCallback.test.js
git commit -m "feat(chat-frontend): add oidcCallback URL helpers"
```

---

### Task 5: Install `oidc-client-ts` and wrap it in `src/lib/oidc.js`

No TDD on this task — `oidc-client-ts` is thin wrapper code that can only be validated end-to-end against a real Keycloak. We exercise it manually in Task 8 and keep all testable logic in the pure helpers from Tasks 2–4.

**Files:**
- Modify: `chat-frontend/package.json`
- Create: `chat-frontend/src/lib/oidc.js`

- [ ] **Step 1: Install `oidc-client-ts`**

```bash
cd chat-frontend
npm install oidc-client-ts@^3.1.0
```

Expected: `package.json` lists `"oidc-client-ts": "^3.1.0"` under `dependencies`; `package-lock.json` updated.

- [ ] **Step 2: Create `chat-frontend/src/lib/oidc.js`**

```js
import { UserManager, WebStorageStateStore } from 'oidc-client-ts'
import { isOidcCallbackUrl, stripOidcCallbackParams } from './oidcCallback'

let userManager = null

function readOidcConfig() {
  const authority = import.meta.env.VITE_OIDC_AUTHORITY
  const clientId = import.meta.env.VITE_OIDC_CLIENT_ID
  if (!authority || !clientId) {
    throw new Error(
      'VITE_OIDC_AUTHORITY and VITE_OIDC_CLIENT_ID must be set when VITE_AUTH_MODE=oidc',
    )
  }
  return { authority, clientId }
}

// getUserManager returns a singleton UserManager configured from env vars.
// Throws if OIDC config is missing.
export function getUserManager() {
  if (userManager) return userManager
  const { authority, clientId } = readOidcConfig()
  userManager = new UserManager({
    authority,
    client_id: clientId,
    redirect_uri: window.location.origin + '/',
    response_type: 'code',
    scope: 'openid profile email',
    post_logout_redirect_uri: window.location.origin + '/',
    userStore: new WebStorageStateStore({ store: window.sessionStorage }),
  })
  return userManager
}

// signinRedirect stashes siteId in sessionStorage (so it survives the round
// trip through Keycloak) and redirects the browser to the OIDC provider.
export async function signinRedirect(siteId) {
  if (siteId) {
    window.sessionStorage.setItem('oidc.siteId', siteId)
  }
  await getUserManager().signinRedirect()
}

// popSiteId returns and clears the stashed siteId, or null.
export function popSiteId() {
  const v = window.sessionStorage.getItem('oidc.siteId')
  window.sessionStorage.removeItem('oidc.siteId')
  return v
}

// completeSigninIfCallback checks whether the current URL is an OIDC
// callback. If so, completes the code exchange, strips callback params from
// the URL via history.replaceState, and returns the OIDC user. Otherwise
// returns null.
export async function completeSigninIfCallback() {
  const currentUrl = window.location.href
  if (!isOidcCallbackUrl(currentUrl)) return null

  const user = await getUserManager().signinRedirectCallback()
  const cleaned = stripOidcCallbackParams(currentUrl)
  window.history.replaceState({}, '', cleaned)
  return user
}
```

- [ ] **Step 3: Run tests to confirm nothing regressed**

```bash
cd chat-frontend && npm test
```

Expected: PASS — the previous tests for `authRequestBody`, `authMode`, `oidcCallback` all still pass. `oidc.js` has no test file.

- [ ] **Step 4: Verify the production build still succeeds**

```bash
cd chat-frontend && npm run build
```

Expected: Vite build completes with no errors. This confirms the new import graph resolves.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/package.json chat-frontend/package-lock.json chat-frontend/src/lib/oidc.js
git commit -m "feat(chat-frontend): wrap oidc-client-ts for Keycloak SSO"
```

---

### Task 6: Refactor `NatsContext.connect` to take discriminated input

Changes the `connect` signature from positional `(account, siteId)` to `({ account?, ssoToken?, siteId })`. Uses `buildAuthRequestBody` for the POST body. This is the single breaking change for existing callers; `LoginPage` is updated in Task 7.

**Files:**
- Modify: `chat-frontend/src/context/NatsContext.jsx`

- [ ] **Step 1: Read the current file**

Open `chat-frontend/src/context/NatsContext.jsx` to confirm the current `connectToNats` body (lines 18–52). The existing implementation generates an nkey, posts `{account, natsPublicKey}`, and sets `user = {...userInfo, siteId}`.

- [ ] **Step 2: Update the imports**

At the top of `chat-frontend/src/context/NatsContext.jsx`, add this import below the existing ones:

```js
import { buildAuthRequestBody } from '../lib/authRequestBody'
```

- [ ] **Step 3: Replace the `connectToNats` function**

Replace the existing `connectToNats` definition (the whole `useCallback` block) with:

```js
const connectToNats = useCallback(async ({ account, ssoToken, siteId }) => {
  setError(null)

  const nkey = createUser()
  const natsPublicKey = nkey.getPublicKey()

  const body = buildAuthRequestBody({ account, ssoToken, natsPublicKey })

  const authResp = await fetch(`${authUrl}/auth`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })

  if (!authResp.ok) {
    const errBody = await authResp.json().catch(() => ({}))
    throw new Error(errBody.error || `Auth failed: ${authResp.status}`)
  }

  const { natsJwt, user: userInfo } = await authResp.json()

  const nc = await natsConnect({
    servers: natsUrl,
    authenticator: jwtAuthenticator(natsJwt, nkey.getSeed()),
  })

  ncRef.current = nc
  setUser({ ...userInfo, siteId })
  setConnected(true)

  nc.closed().then((err) => {
    if (err) {
      setError(`Disconnected: ${err.message}`)
    }
    setConnected(false)
  })
}, [authUrl, natsUrl])
```

- [ ] **Step 4: Run the unit tests**

```bash
cd chat-frontend && npm test
```

Expected: PASS — no test file for `NatsContext` itself; the pure helpers it depends on all pass.

- [ ] **Step 5: Build to catch breakage in existing callers**

```bash
cd chat-frontend && npm run build
```

Expected: FAIL — `LoginPage.jsx` still calls `connect(account.trim(), siteId.trim())` with positional args. The build will report a lint/parse success but the old call shape will silently pass a string as `{account, ssoToken, siteId}`, and `buildAuthRequestBody` will throw at runtime. This is expected and will be fixed in Task 7.

If Vite reports no build error, that is OK — the runtime breakage is what's important. Do NOT patch `LoginPage.jsx` yet; that is Task 7's job.

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/context/NatsContext.jsx
git commit -m "refactor(chat-frontend): NatsContext.connect takes discriminated input"
```

---

### Task 7: Branch `LoginPage` UI on auth mode

Updates `LoginPage` so it renders:
- in `dev` mode: the current account-name form (with the updated `connect` signature)
- in `oidc` mode: a single "Login with SSO" button that triggers `signinRedirect`

Uses `AUTH_MODE` from `authMode.js`.

**Files:**
- Modify: `chat-frontend/src/pages/LoginPage.jsx`

- [ ] **Step 1: Read the current file**

Open `chat-frontend/src/pages/LoginPage.jsx` to confirm the current structure (lines 1–65).

- [ ] **Step 2: Replace the file contents**

Replace the entire contents of `chat-frontend/src/pages/LoginPage.jsx` with:

```jsx
import { useState } from 'react'
import { useNats } from '../context/NatsContext'
import { AUTH_MODE } from '../lib/authMode'
import { signinRedirect } from '../lib/oidc'

export default function LoginPage() {
  const { connect, error: natsError } = useNats()
  const defaultSiteId = import.meta.env.VITE_DEFAULT_SITE_ID || 'site-A'

  const [account, setAccount] = useState('')
  const [siteId, setSiteId] = useState(defaultSiteId)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const handleDevSubmit = async (e) => {
    e.preventDefault()
    if (!account.trim()) return
    setLoading(true)
    setError(null)
    try {
      await connect({ account: account.trim(), siteId: siteId.trim() })
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  const handleOidcSignin = async () => {
    setLoading(true)
    setError(null)
    try {
      await signinRedirect(siteId.trim())
      // Browser navigates away to Keycloak; no further work here.
    } catch (err) {
      setError(err.message)
      setLoading(false)
    }
  }

  if (AUTH_MODE === 'oidc') {
    return (
      <div className="login-page">
        <form className="login-form" onSubmit={(e) => e.preventDefault()}>
          <h1>Chat</h1>
          <p className="login-subtitle">SSO Login</p>

          <label htmlFor="siteId">Site ID</label>
          <input
            id="siteId"
            type="text"
            value={siteId}
            onChange={(e) => setSiteId(e.target.value)}
            disabled={loading}
          />

          <button type="button" onClick={handleOidcSignin} disabled={loading}>
            {loading ? 'Redirecting...' : 'Login with SSO'}
          </button>

          {(error || natsError) && (
            <div className="login-error">{error || natsError}</div>
          )}
        </form>
      </div>
    )
  }

  return (
    <div className="login-page">
      <form className="login-form" onSubmit={handleDevSubmit}>
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

- [ ] **Step 3: Run unit tests**

```bash
cd chat-frontend && npm test
```

Expected: PASS — the pure-logic tests still pass.

- [ ] **Step 4: Build to confirm no import errors**

```bash
cd chat-frontend && npm run build
```

Expected: PASS (no build errors).

- [ ] **Step 5: Manual smoke — dev mode**

In one terminal, start the auth-service with `DEV_MODE=true` (per existing `auth-service/deploy/docker-compose.yml`). In another:

```bash
cd chat-frontend && npm run dev
```

Open `http://localhost:3000/`. Confirm:
- Subtitle reads "Dev Mode Login"
- Entering account "alice" and clicking Connect logs in successfully (chat UI renders)

- [ ] **Step 6: Commit**

```bash
git add chat-frontend/src/pages/LoginPage.jsx
git commit -m "feat(chat-frontend): branch LoginPage on VITE_AUTH_MODE"
```

---

### Task 8: Complete OIDC callback on mount in `App.jsx`

After Keycloak redirects back, `App.jsx` needs to (1) complete the code exchange, (2) extract the ID token, and (3) call `connect({ ssoToken, siteId })`. While this is in progress the app shows a neutral "Signing in..." state instead of flashing the login page.

**Files:**
- Modify: `chat-frontend/src/App.jsx`

- [ ] **Step 1: Read the current file**

Open `chat-frontend/src/App.jsx`. It's 21 lines.

- [ ] **Step 2: Replace the file contents**

Replace the entire contents of `chat-frontend/src/App.jsx` with:

```jsx
import { useEffect, useState } from 'react'
import { NatsProvider, useNats } from './context/NatsContext'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'
import { AUTH_MODE } from './lib/authMode'
import { completeSigninIfCallback, popSiteId } from './lib/oidc'

function AppContent() {
  const { connected, connect } = useNats()
  const [callbackState, setCallbackState] = useState(
    AUTH_MODE === 'oidc' ? 'pending' : 'idle',
  )
  const [callbackError, setCallbackError] = useState(null)

  useEffect(() => {
    if (AUTH_MODE !== 'oidc') return
    let cancelled = false
    ;(async () => {
      try {
        const user = await completeSigninIfCallback()
        if (cancelled) return
        if (user) {
          const siteId = popSiteId() || import.meta.env.VITE_DEFAULT_SITE_ID || 'site-A'
          await connect({ ssoToken: user.id_token, siteId })
        }
        setCallbackState('idle')
      } catch (err) {
        if (cancelled) return
        setCallbackError(err.message)
        setCallbackState('idle')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [connect])

  if (callbackState === 'pending') {
    return <div className="login-page"><p>Signing in...</p></div>
  }

  if (!connected) {
    return (
      <>
        {callbackError && (
          <div className="login-error" role="alert">
            SSO callback error: {callbackError}
          </div>
        )}
        <LoginPage />
      </>
    )
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

- [ ] **Step 3: Run unit tests**

```bash
cd chat-frontend && npm test
```

Expected: PASS.

- [ ] **Step 4: Build**

```bash
cd chat-frontend && npm run build
```

Expected: no errors.

- [ ] **Step 5: Manual smoke — dev mode regression**

```bash
cd chat-frontend && npm run dev
```

With `VITE_AUTH_MODE` unset (default `dev`), confirm the dev login form still works end-to-end against the local auth-service.

- [ ] **Step 6: Manual smoke — OIDC mode (requires a Keycloak)**

Set up a local Keycloak realm with a public SPA client configured with:
- Valid redirect URI: `http://localhost:3000/`
- Web origin: `http://localhost:3000`

Run the auth-service with `DEV_MODE=false`, `OIDC_ISSUER_URL=<keycloak realm URL>`, `OIDC_AUDIENCE=<client-id>`.

Start the frontend with OIDC env vars:
```bash
VITE_AUTH_MODE=oidc \
VITE_OIDC_AUTHORITY=https://<keycloak-host>/realms/<realm> \
VITE_OIDC_CLIENT_ID=<client-id> \
npm run dev
```

Open `http://localhost:3000/`. Confirm:
- Subtitle reads "SSO Login"
- Clicking "Login with SSO" redirects to Keycloak
- After authenticating at Keycloak, the browser returns to `http://localhost:3000/` with a brief "Signing in..." screen
- The URL's `?code=...&state=...` params are removed via `replaceState`
- The chat UI renders with the authenticated user's account

If this manual flow cannot be completed in the reviewer's environment (no Keycloak handy), note that in the task checkoff comment and defer to CI/integration testing later.

- [ ] **Step 7: Commit**

```bash
git add chat-frontend/src/App.jsx
git commit -m "feat(chat-frontend): complete OIDC callback on mount"
```

---

### Task 9: Document auth modes in `chat-frontend/README.md`

The frontend currently has no README. Create one documenting the two auth modes and the required env vars.

**Files:**
- Create: `chat-frontend/README.md`

- [ ] **Step 1: Create `chat-frontend/README.md`**

```markdown
# chat-frontend

Test frontend for the chat system. React 19 + Vite 6.

## Scripts

- `npm run dev` — start the Vite dev server on port 3000
- `npm run build` — produce a production build
- `npm run preview` — preview the production build
- `npm test` — run unit tests with vitest

## Environment Variables

| Name | Default | Required | Description |
|------|---------|----------|-------------|
| `VITE_AUTH_URL` | `""` (proxied to `http://localhost:8080` in dev) | no | Base URL of the auth-service |
| `VITE_NATS_URL` | `ws://localhost:4223` | no | NATS WebSocket URL |
| `VITE_DEFAULT_SITE_ID` | `site-A` | no | Default value for the Site ID input |
| `VITE_AUTH_MODE` | `dev` | no | `dev` (account-name login) or `oidc` (Keycloak SSO) |
| `VITE_OIDC_AUTHORITY` | — | yes when `VITE_AUTH_MODE=oidc` | Keycloak realm URL, e.g. `https://keycloak.example.com/realms/chat` |
| `VITE_OIDC_CLIENT_ID` | — | yes when `VITE_AUTH_MODE=oidc` | Keycloak client ID (public SPA client with PKCE) |

## Auth Modes

### `dev` mode (default)

Matches the auth-service running with `DEV_MODE=true`. The login page collects an account name; the frontend posts `{account, natsPublicKey}` to `POST /auth` and receives a NATS JWT.

### `oidc` mode

Matches the auth-service running with `DEV_MODE=false` and real OIDC configured. The login page shows a single "Login with SSO" button that redirects to Keycloak's login page via OAuth2 Authorization Code + PKCE. After Keycloak redirects back, the frontend exchanges the code for tokens, extracts the ID token, and posts `{ssoToken, natsPublicKey}` to `POST /auth`.

To run against a local Keycloak:

1. Configure a Keycloak realm with a public SPA client:
   - Valid redirect URI: `http://localhost:3000/`
   - Web origin: `http://localhost:3000`
   - Standard flow (Authorization Code + PKCE) enabled
2. Start the auth-service with `DEV_MODE=false`, `OIDC_ISSUER_URL=<realm URL>`, `OIDC_AUDIENCE=<client-id>`.
3. Start the frontend:
   ```bash
   VITE_AUTH_MODE=oidc \
     VITE_OIDC_AUTHORITY=https://<keycloak-host>/realms/<realm> \
     VITE_OIDC_CLIENT_ID=<client-id> \
     npm run dev
   ```
```

- [ ] **Step 2: Commit**

```bash
git add chat-frontend/README.md
git commit -m "docs(chat-frontend): document VITE_AUTH_MODE and OIDC setup"
```

---

## Self-Review Summary

- **Spec coverage:** All spec sections are implemented — env vars (Task 3, 5, 9), dependency (Task 5), component changes (Tasks 5–8), data flow (Tasks 6–8), error handling (Tasks 2, 5, 8), testing (Tasks 1–4 unit; Tasks 7–8 manual smoke), files changed (all covered).
- **Types & names:** `connect({account?, ssoToken?, siteId})` is consistent across Tasks 6–8. `AUTH_MODE` is the single exported constant across Tasks 3, 7, 8. `buildAuthRequestBody` is defined in Task 2 and only used in Task 6. `signinRedirect`, `completeSigninIfCallback`, `popSiteId` are defined in Task 5 and used in Tasks 7–8 exactly as exported.
- **No placeholders:** Every code block is complete. Every command has expected output noted. No "TBD" / "TODO" / "similar to Task N" references.
- **Known deferred:** Component-level UI tests (React Testing Library) and automated OIDC end-to-end are out of scope; the spec flagged both as deferred. Pure-logic units are fully covered.
