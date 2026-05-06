# Chat Frontend — Lighter Theme Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a default light theme and a header toggle to `chat-frontend`, switchable via a `data-theme` attribute on `<html>`, persisted in `localStorage`, respecting `prefers-color-scheme` until the user makes an explicit choice.

**Architecture:** A new `src/styles/tokens.css` declares semantic CSS custom properties — light values on `:root`, dark overrides on `:root[data-theme="dark"]`. The existing `src/styles/index.css` is rewritten in place to reference tokens via `var(--…)`. A new `ThemeContext` initializes from `localStorage` (or `matchMedia`), subscribes to system changes, and exposes `toggleTheme()`. A `ThemeToggle` icon button sits in the chat header. An inline pre-paint script in `index.html` sets `data-theme` before React mounts, preventing FOUC.

**Tech Stack:** React 19, Vite 6, vanilla CSS, vitest + @testing-library/react. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-06-lighter-chat-theme-design.md`

---

## File Map

**New files:**
- `chat-frontend/src/styles/tokens.css` — light + dark palettes as CSS variables.
- `chat-frontend/src/context/ThemeContext.jsx` — provider + `useTheme` hook.
- `chat-frontend/src/context/ThemeContext.test.jsx` — provider/hook tests.
- `chat-frontend/src/components/ThemeToggle.jsx` — sun/moon button.
- `chat-frontend/src/components/ThemeToggle.test.jsx` — toggle tests.

**Modified files:**
- `chat-frontend/index.html` — inline pre-paint script.
- `chat-frontend/src/main.jsx` — import `tokens.css`, wrap `<App />` in `<ThemeProvider>`.
- `chat-frontend/src/styles/index.css` — replace every hardcoded color with `var(--…)`.
- `chat-frontend/src/pages/ChatPage.jsx` — render `<ThemeToggle />` between user info and logout.

---

## Task 1: Add design tokens

**Files:**
- Create: `chat-frontend/src/styles/tokens.css`

- [ ] **Step 1: Create the tokens stylesheet**

Create `chat-frontend/src/styles/tokens.css` with the following content:

```css
/* Semantic color tokens. Light is the default on :root; the dark theme
 * overrides via :root[data-theme="dark"]. Components reference roles
 * (e.g. --bg-sidebar, --text-muted), never literal colors. */

:root {
  /* Surfaces */
  --bg-app: #ffffff;
  --bg-sidebar: #f5f7fa;
  --bg-surface: #ffffff;
  --bg-elevated: #ebedf0;
  --bg-input: #f0f2f5;
  --bg-input-hover: #e8eaed;
  --bg-hover: #ebedf0;
  --bg-selected: #dbe2ea;

  /* Text */
  --text-primary: #1a1a1a;
  --text-secondary: #4a4a4a;
  --text-muted: #6b7280;
  --text-on-accent: #ffffff;

  /* Borders */
  --border-subtle: #e4e6ea;
  --border-strong: #cfd3d8;

  /* Accent */
  --accent: #4752c4;
  --accent-hover: #3a44a8;
  --accent-soft: rgba(71, 82, 196, 0.15);

  /* Status */
  --status-error: #b42318;
  --status-error-bg: #fef3f2;
  --status-success: #0f8a4f;
  --status-success-bg: #ecfdf3;
  --mention: #b45309;
  --mention-all: #b42318;

  /* Effects */
  --shadow-dialog: 0 8px 24px rgba(0, 0, 0, 0.12);
  --scrim: rgba(15, 23, 42, 0.45);
}

:root[data-theme='dark'] {
  --bg-app: #313338;
  --bg-sidebar: #2b2d31;
  --bg-surface: #313338;
  --bg-elevated: #232529;
  --bg-input: #383a40;
  --bg-input-hover: #404249;
  --bg-hover: #35373c;
  --bg-selected: #404249;

  --text-primary: #f2f3f5;
  --text-secondary: #dbdee1;
  --text-muted: #949ba4;
  --text-on-accent: #ffffff;

  --border-subtle: #3f4147;
  --border-strong: #4e5058;

  --accent: #5865f2;
  --accent-hover: #4752c4;
  --accent-soft: rgba(88, 101, 242, 0.20);

  --status-error: #f38ba8;
  --status-error-bg: rgba(243, 139, 168, 0.10);
  --status-success: #43b581;
  --status-success-bg: rgba(67, 181, 129, 0.12);
  --mention: #faa61a;
  --mention-all: #ed4245;

  --shadow-dialog: 0 8px 24px rgba(0, 0, 0, 0.32);
  --scrim: rgba(0, 0, 0, 0.6);
}
```

- [ ] **Step 2: Commit**

```bash
git add chat-frontend/src/styles/tokens.css
git commit -m "feat(chat-frontend): add light/dark color token stylesheet"
```

---

## Task 2: ThemeContext — initial-state tests (red)

**Files:**
- Create: `chat-frontend/src/context/ThemeContext.test.jsx`

- [ ] **Step 1: Write the failing initial-state tests**

Create `chat-frontend/src/context/ThemeContext.test.jsx`:

```jsx
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import { ThemeProvider, useTheme } from './ThemeContext'

function Probe() {
  const { theme, source } = useTheme()
  return (
    <div>
      <span data-testid="theme">{theme}</span>
      <span data-testid="source">{source}</span>
    </div>
  )
}

function setMatchMedia(matches) {
  const listeners = new Set()
  const mql = {
    matches,
    media: '(prefers-color-scheme: dark)',
    addEventListener: (_event, cb) => listeners.add(cb),
    removeEventListener: (_event, cb) => listeners.delete(cb),
    dispatchEvent: (event) => {
      mql.matches = event.matches
      listeners.forEach((cb) => cb(event))
      return true
    },
  }
  window.matchMedia = vi.fn().mockReturnValue(mql)
  return mql
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
})

afterEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  vi.unstubAllGlobals()
})

describe('ThemeProvider initial state', () => {
  it('uses stored "dark" from localStorage and marks source=user', () => {
    localStorage.setItem('theme', 'dark')
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('dark')
    expect(screen.getByTestId('source').textContent).toBe('user')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('uses stored "light" from localStorage and marks source=user', () => {
    localStorage.setItem('theme', 'light')
    setMatchMedia(true)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')
    expect(screen.getByTestId('source').textContent).toBe('user')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('falls back to matchMedia when localStorage is empty', () => {
    setMatchMedia(true)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('dark')
    expect(screen.getByTestId('source').textContent).toBe('system')
  })

  it('treats invalid localStorage value as absent', () => {
    localStorage.setItem('theme', 'turquoise')
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')
    expect(screen.getByTestId('source').textContent).toBe('system')
  })

  it('defaults to light when matchMedia is undefined', () => {
    // eslint-disable-next-line no-undef
    delete window.matchMedia
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')
    expect(screen.getByTestId('source').textContent).toBe('system')
  })
})
```

- [ ] **Step 2: Run tests; verify they fail**

Run: `cd chat-frontend && npm test -- ThemeContext`
Expected: FAIL — `Cannot find module './ThemeContext'` (or similar resolution error).

---

## Task 3: ThemeContext — implement initial state (green)

**Files:**
- Create: `chat-frontend/src/context/ThemeContext.jsx`

- [ ] **Step 1: Create the provider with initial-state logic only**

Create `chat-frontend/src/context/ThemeContext.jsx`:

```jsx
import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'

const STORAGE_KEY = 'theme'
const MEDIA_QUERY = '(prefers-color-scheme: dark)'

const ThemeContext = createContext(null)

function readStored() {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    // localStorage unavailable; fall through
  }
  return null
}

function readSystem() {
  try {
    if (typeof window === 'undefined' || !window.matchMedia) return 'light'
    return window.matchMedia(MEDIA_QUERY).matches ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

function applyAttribute(theme) {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('data-theme', theme)
}

function resolveInitial() {
  const stored = readStored()
  if (stored) return { theme: stored, source: 'user' }
  return { theme: readSystem(), source: 'system' }
}

export function ThemeProvider({ children }) {
  const [state, setState] = useState(resolveInitial)

  useEffect(() => {
    applyAttribute(state.theme)
  }, [state.theme])

  const value = {
    theme: state.theme,
    source: state.source,
    setTheme: () => {},
    toggleTheme: () => {},
  }

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used within a ThemeProvider')
  return ctx
}
```

- [ ] **Step 2: Run tests; verify they pass**

Run: `cd chat-frontend && npm test -- ThemeContext`
Expected: PASS — all 5 initial-state tests green.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/context/ThemeContext.jsx chat-frontend/src/context/ThemeContext.test.jsx
git commit -m "feat(chat-frontend): add ThemeProvider with initial-state resolution"
```

---

## Task 4: ThemeContext — setTheme / toggleTheme tests (red)

**Files:**
- Modify: `chat-frontend/src/context/ThemeContext.test.jsx` (append a new `describe` block)

- [ ] **Step 1: Append the failing tests**

Append to `chat-frontend/src/context/ThemeContext.test.jsx` (after the existing `describe`):

```jsx
function ToggleProbe() {
  const { theme, toggleTheme, setTheme } = useTheme()
  return (
    <div>
      <span data-testid="theme">{theme}</span>
      <button data-testid="toggle" onClick={toggleTheme}>
        toggle
      </button>
      <button data-testid="set-dark" onClick={() => setTheme('dark')}>
        set dark
      </button>
      <button data-testid="set-light" onClick={() => setTheme('light')}>
        set light
      </button>
    </div>
  )
}

describe('ThemeProvider mutations', () => {
  it('setTheme updates state, attribute, and localStorage', () => {
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <ToggleProbe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')

    act(() => {
      screen.getByTestId('set-dark').click()
    })

    expect(screen.getByTestId('theme').textContent).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(localStorage.getItem('theme')).toBe('dark')
  })

  it('toggleTheme flips light <-> dark and persists', () => {
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <ToggleProbe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')

    act(() => {
      screen.getByTestId('toggle').click()
    })
    expect(screen.getByTestId('theme').textContent).toBe('dark')
    expect(localStorage.getItem('theme')).toBe('dark')

    act(() => {
      screen.getByTestId('toggle').click()
    })
    expect(screen.getByTestId('theme').textContent).toBe('light')
    expect(localStorage.getItem('theme')).toBe('light')
  })

  it('does not crash when localStorage.setItem throws', () => {
    setMatchMedia(false)
    const original = Storage.prototype.setItem
    Storage.prototype.setItem = vi.fn(() => {
      throw new Error('quota exceeded')
    })
    try {
      render(
        <ThemeProvider>
          <ToggleProbe />
        </ThemeProvider>
      )
      act(() => {
        screen.getByTestId('set-dark').click()
      })
      expect(screen.getByTestId('theme').textContent).toBe('dark')
      expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    } finally {
      Storage.prototype.setItem = original
    }
  })
})
```

- [ ] **Step 2: Run tests; verify the new ones fail**

Run: `cd chat-frontend && npm test -- ThemeContext`
Expected: FAIL — `setTheme` and `toggleTheme` are no-op stubs, so theme never changes.

---

## Task 5: ThemeContext — implement mutations (green)

**Files:**
- Modify: `chat-frontend/src/context/ThemeContext.jsx`

- [ ] **Step 1: Implement setTheme and toggleTheme**

Replace the body of `ThemeProvider` in `chat-frontend/src/context/ThemeContext.jsx` so the file reads:

```jsx
import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'

const STORAGE_KEY = 'theme'
const MEDIA_QUERY = '(prefers-color-scheme: dark)'

const ThemeContext = createContext(null)

function readStored() {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    // localStorage unavailable; fall through
  }
  return null
}

function readSystem() {
  try {
    if (typeof window === 'undefined' || !window.matchMedia) return 'light'
    return window.matchMedia(MEDIA_QUERY).matches ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

function writeStored(theme) {
  try {
    localStorage.setItem(STORAGE_KEY, theme)
  } catch {
    // ignore — choice still applies in-memory
  }
}

function applyAttribute(theme) {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('data-theme', theme)
}

function resolveInitial() {
  const stored = readStored()
  if (stored) return { theme: stored, source: 'user' }
  return { theme: readSystem(), source: 'system' }
}

export function ThemeProvider({ children }) {
  const [state, setState] = useState(resolveInitial)

  useEffect(() => {
    applyAttribute(state.theme)
  }, [state.theme])

  const setTheme = useCallback((next) => {
    if (next !== 'light' && next !== 'dark') return
    writeStored(next)
    setState({ theme: next, source: 'user' })
  }, [])

  const toggleTheme = useCallback(() => {
    setState((prev) => {
      const next = prev.theme === 'dark' ? 'light' : 'dark'
      writeStored(next)
      return { theme: next, source: 'user' }
    })
  }, [])

  const value = { theme: state.theme, source: state.source, setTheme, toggleTheme }

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used within a ThemeProvider')
  return ctx
}
```

- [ ] **Step 2: Run tests; verify they pass**

Run: `cd chat-frontend && npm test -- ThemeContext`
Expected: PASS — all initial-state and mutation tests green.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/context/ThemeContext.jsx chat-frontend/src/context/ThemeContext.test.jsx
git commit -m "feat(chat-frontend): implement setTheme and toggleTheme with localStorage persistence"
```

---

## Task 6: ThemeContext — system-preference subscription tests (red)

**Files:**
- Modify: `chat-frontend/src/context/ThemeContext.test.jsx` (append)

- [ ] **Step 1: Append the failing tests**

Append to `chat-frontend/src/context/ThemeContext.test.jsx`:

```jsx
describe('ThemeProvider system-preference subscription', () => {
  it('updates theme when system pref changes and source=system', () => {
    const mql = setMatchMedia(false)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')

    act(() => {
      mql.dispatchEvent({ matches: true })
    })
    expect(screen.getByTestId('theme').textContent).toBe('dark')
  })

  it('ignores system pref changes when source=user', () => {
    localStorage.setItem('theme', 'light')
    const mql = setMatchMedia(false)
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')

    act(() => {
      mql.dispatchEvent({ matches: true })
    })
    expect(screen.getByTestId('theme').textContent).toBe('light')
  })

  it('explicit setTheme makes subsequent system changes a no-op', () => {
    const mql = setMatchMedia(false)
    render(
      <ThemeProvider>
        <ToggleProbe />
      </ThemeProvider>
    )
    expect(screen.getByTestId('theme').textContent).toBe('light')

    act(() => {
      screen.getByTestId('set-light').click()
    })
    act(() => {
      mql.dispatchEvent({ matches: true })
    })
    expect(screen.getByTestId('theme').textContent).toBe('light')
  })
})
```

- [ ] **Step 2: Run tests; verify the new ones fail**

Run: `cd chat-frontend && npm test -- ThemeContext`
Expected: FAIL — there's no subscription wired up yet.

---

## Task 7: ThemeContext — implement system subscription (green)

**Files:**
- Modify: `chat-frontend/src/context/ThemeContext.jsx`

- [ ] **Step 1: Add the matchMedia subscription**

In `chat-frontend/src/context/ThemeContext.jsx`, replace the `ThemeProvider` body with the following (the rest of the file stays the same):

```jsx
export function ThemeProvider({ children }) {
  const [state, setState] = useState(resolveInitial)
  const sourceRef = useRef(state.source)

  useEffect(() => {
    sourceRef.current = state.source
  }, [state.source])

  useEffect(() => {
    applyAttribute(state.theme)
  }, [state.theme])

  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return undefined
    const mql = window.matchMedia(MEDIA_QUERY)
    const handler = (e) => {
      if (sourceRef.current !== 'system') return
      setState({ theme: e.matches ? 'dark' : 'light', source: 'system' })
    }
    mql.addEventListener('change', handler)
    return () => mql.removeEventListener('change', handler)
  }, [])

  const setTheme = useCallback((next) => {
    if (next !== 'light' && next !== 'dark') return
    writeStored(next)
    setState({ theme: next, source: 'user' })
  }, [])

  const toggleTheme = useCallback(() => {
    setState((prev) => {
      const next = prev.theme === 'dark' ? 'light' : 'dark'
      writeStored(next)
      return { theme: next, source: 'user' }
    })
  }, [])

  const value = { theme: state.theme, source: state.source, setTheme, toggleTheme }

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}
```

A `sourceRef` mirrors `state.source` so the long-lived `matchMedia` listener
reads the current value without re-subscribing each time the user toggles.

- [ ] **Step 2: Run tests; verify they pass**

Run: `cd chat-frontend && npm test -- ThemeContext`
Expected: PASS — all initial-state, mutation, and subscription tests green.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/context/ThemeContext.jsx chat-frontend/src/context/ThemeContext.test.jsx
git commit -m "feat(chat-frontend): subscribe ThemeProvider to prefers-color-scheme changes"
```

---

## Task 8: ThemeToggle — tests (red)

**Files:**
- Create: `chat-frontend/src/components/ThemeToggle.test.jsx`

- [ ] **Step 1: Write the failing tests**

Create `chat-frontend/src/components/ThemeToggle.test.jsx`:

```jsx
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import { ThemeProvider } from '../context/ThemeContext'
import ThemeToggle from './ThemeToggle'

function setMatchMedia(matches) {
  const mql = {
    matches,
    media: '(prefers-color-scheme: dark)',
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }
  window.matchMedia = vi.fn().mockReturnValue(mql)
  return mql
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
})

afterEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  vi.unstubAllGlobals()
})

describe('ThemeToggle', () => {
  it('renders moon icon and "switch to dark" label when current theme is light', () => {
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    )
    const btn = screen.getByRole('button', { name: /switch to dark theme/i })
    expect(btn).toBeInTheDocument()
    expect(btn.querySelector('[data-icon="moon"]')).not.toBeNull()
  })

  it('renders sun icon and "switch to light" label when current theme is dark', () => {
    localStorage.setItem('theme', 'dark')
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    )
    const btn = screen.getByRole('button', { name: /switch to light theme/i })
    expect(btn).toBeInTheDocument()
    expect(btn.querySelector('[data-icon="sun"]')).not.toBeNull()
  })

  it('clicking flips the theme and updates the data-theme attribute', () => {
    setMatchMedia(false)
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    )
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')

    act(() => {
      screen.getByRole('button', { name: /switch to dark theme/i }).click()
    })

    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(localStorage.getItem('theme')).toBe('dark')
  })
})
```

- [ ] **Step 2: Run tests; verify they fail**

Run: `cd chat-frontend && npm test -- ThemeToggle`
Expected: FAIL — `Cannot find module './ThemeToggle'`.

---

## Task 9: ThemeToggle — implement (green)

**Files:**
- Create: `chat-frontend/src/components/ThemeToggle.jsx`

- [ ] **Step 1: Create the component**

Create `chat-frontend/src/components/ThemeToggle.jsx`:

```jsx
import { useTheme } from '../context/ThemeContext'

function MoonIcon() {
  return (
    <svg
      data-icon="moon"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
    </svg>
  )
}

function SunIcon() {
  return (
    <svg
      data-icon="sun"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
    </svg>
  )
}

export default function ThemeToggle() {
  const { theme, toggleTheme } = useTheme()
  const next = theme === 'dark' ? 'light' : 'dark'
  return (
    <button
      type="button"
      className="chat-header-logout theme-toggle"
      onClick={toggleTheme}
      aria-label={`Switch to ${next} theme`}
      title={`Switch to ${next} theme`}
    >
      {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
    </button>
  )
}
```

- [ ] **Step 2: Run tests; verify they pass**

Run: `cd chat-frontend && npm test -- ThemeToggle`
Expected: PASS — all 3 toggle tests green.

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/components/ThemeToggle.jsx chat-frontend/src/components/ThemeToggle.test.jsx
git commit -m "feat(chat-frontend): add ThemeToggle button"
```

---

## Task 10: Pre-paint script in index.html

**Files:**
- Modify: `chat-frontend/index.html`

- [ ] **Step 1: Add the inline pre-paint script**

Replace the contents of `chat-frontend/index.html` with:

```html
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Chat</title>
    <script>
      (function () {
        try {
          var stored = localStorage.getItem('theme');
          if (stored === 'light' || stored === 'dark') {
            document.documentElement.setAttribute('data-theme', stored);
            return;
          }
        } catch (e) {}
        var prefersDark =
          window.matchMedia &&
          window.matchMedia('(prefers-color-scheme: dark)').matches;
        document.documentElement.setAttribute(
          'data-theme',
          prefersDark ? 'dark' : 'light'
        );
      })();
    </script>
    <!-- Runtime config; loaded synchronously before module bundle. -->
    <script src="/config.js"></script>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.jsx"></script>
  </body>
</html>
```

- [ ] **Step 2: Commit**

```bash
git add chat-frontend/index.html
git commit -m "feat(chat-frontend): inline pre-paint script to set data-theme before React mounts"
```

---

## Task 11: Wire ThemeProvider into the app

**Files:**
- Modify: `chat-frontend/src/main.jsx`

- [ ] **Step 1: Import tokens.css and wrap App in ThemeProvider**

Replace the contents of `chat-frontend/src/main.jsx` with:

```jsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { ThemeProvider } from './context/ThemeContext'
import './styles/tokens.css'
import './styles/index.css'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <ThemeProvider>
      <App />
    </ThemeProvider>
  </StrictMode>,
)
```

`tokens.css` must be imported before `index.css` so the variables exist when
the consuming rules are parsed.

- [ ] **Step 2: Run the full test suite to confirm nothing regressed**

Run: `cd chat-frontend && npm test`
Expected: PASS — all existing tests still green (they don't touch theme).

- [ ] **Step 3: Commit**

```bash
git add chat-frontend/src/main.jsx
git commit -m "feat(chat-frontend): wire ThemeProvider and tokens.css into root"
```

---

## Task 12: Place ThemeToggle in the chat header

**Files:**
- Modify: `chat-frontend/src/pages/ChatPage.jsx`

- [ ] **Step 1: Add the toggle between user info and the logout button**

In `chat-frontend/src/pages/ChatPage.jsx`:

1. Add this import alongside the others (after the `InRoomSearch` import):

```jsx
import ThemeToggle from '../components/ThemeToggle'
```

2. In the JSX returned by `ChatPage`, locate the block:

```jsx
        <span className="chat-header-user">
          {user?.account} &middot; {user?.siteId}
        </span>
        <button className="chat-header-logout" onClick={disconnect}>
          Logout
        </button>
```

Insert `<ThemeToggle />` between the `</span>` and the logout `<button>` so it
becomes:

```jsx
        <span className="chat-header-user">
          {user?.account} &middot; {user?.siteId}
        </span>
        <ThemeToggle />
        <button className="chat-header-logout" onClick={disconnect}>
          Logout
        </button>
```

- [ ] **Step 2: Run the ChatPage tests**

Run: `cd chat-frontend && npm test -- ChatPage`
Expected: PASS. If a test fails because `ChatPage` is rendered without
`ThemeProvider`, wrap the rendered tree in the test with `<ThemeProvider>`
imported from `../context/ThemeContext`. Do not change the production code to
make the toggle optional.

- [ ] **Step 3: Run the full test suite**

Run: `cd chat-frontend && npm test`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add chat-frontend/src/pages/ChatPage.jsx chat-frontend/src/pages/ChatPage.test.jsx
git commit -m "feat(chat-frontend): render ThemeToggle in the chat header"
```

(Stage `ChatPage.test.jsx` only if it was modified.)

---

## Task 13: Replace hardcoded colors in index.css with tokens

**Files:**
- Modify: `chat-frontend/src/styles/index.css`

This is the largest mechanical step. Work top to bottom through the file,
replacing every literal color (`#…`, `rgb(...)`, `rgba(...)`) with the
appropriate `var(--…)` token. **Do not change any other property** — no
spacing, typography, layout, or selector edits.

- [ ] **Step 1: Replace `body` and login styles**

Find:

```css
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  font-size: 14px;
  color: #1a1a1a;
  background: #f5f5f5;
}
```

Replace with:

```css
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  font-size: 14px;
  color: var(--text-primary);
  background: var(--bg-app);
}
```

Then map every color in the `/* Login */` block:

| Original | Token |
|---|---|
| `background: #f5f5f5` (`.login-page`) | `var(--bg-app)` |
| `background: #fff` (`.login-form`) | `var(--bg-surface)` |
| `box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1)` | `var(--shadow-dialog)` |
| `color: #666` (`.login-subtitle`) | `var(--text-muted)` |
| `color: #444` (`.login-form label`) | `var(--text-secondary)` |
| `border: 1px solid #ddd` (`.login-form input`) | `1px solid var(--border-subtle)` |
| `border-color: #4a9eff` (input focus) | `var(--accent)` |
| `box-shadow: 0 0 0 2px rgba(74, 158, 255, 0.2)` (input focus) | `0 0 0 2px var(--accent-soft)` |
| `background: #4a9eff` (button) | `var(--accent)` |
| `color: #fff` (button text) | `var(--text-on-accent)` |
| `background: #3a8eef` (button hover) | `var(--accent-hover)` |
| `background: #fee` (`.login-error`) | `var(--status-error-bg)` |
| `color: #c00` (`.login-error`) | `var(--status-error)` |

- [ ] **Step 2: Replace Room List, Message Area, Message Input, Dialog, Chat Layout**

Use the same mapping pattern. Reference table for the rest of the file:

| Original literal | Token |
|---|---|
| `#2b2d31` | `var(--bg-sidebar)` |
| `#313338` | `var(--bg-surface)` |
| `#1e1f22` | `var(--bg-elevated)` (header, search-results-header, in-room-search-header, dialog inputs) |
| `#232529` | `var(--bg-elevated)` (search-footer, search-results-tabs) |
| `#35373c` | `var(--bg-hover)` |
| `#404249` | `var(--bg-selected)` (room-item-selected, search-result hover, etc.) |
| `#383a40` | `var(--bg-input)` |
| `#3f4147` | `var(--border-subtle)` |
| `#4e5058` | `var(--border-strong)` |
| `#f2f3f5` | `var(--text-primary)` |
| `#dbdee1` | `var(--text-secondary)` |
| `#b5bac1` | `var(--text-secondary)` |
| `#949ba4` | `var(--text-muted)` |
| `#6d6f78` | `var(--text-muted)` |
| `#fff` (text on accent) | `var(--text-on-accent)` |
| `#5865f2` | `var(--accent)` |
| `#4752c4` | `var(--accent-hover)` |
| `#f38ba8` | `var(--status-error)` |
| `rgba(243, 139, 168, 0.1)` / `0.08` | `var(--status-error-bg)` |
| `#43b581` | `var(--status-success)` |
| `rgba(67, 181, 129, 0.12)` | `var(--status-success-bg)` |
| `#faa61a` | `var(--mention)` |
| `#ed4245` | `var(--mention-all)` |
| `rgba(0, 0, 0, 0.6)` (`.dialog-overlay`) | `var(--scrim)` |
| `rgba(0, 0, 0, 0.32)` (search-dropdown shadow) | `var(--shadow-dialog)` (replace the entire `box-shadow` value) |
| `rgba(255, 255, 255, 0.06)` (search-bar bg) | `var(--bg-input)` |
| `rgba(255, 255, 255, 0.09)` (search-bar hover bg) | `var(--bg-input-hover)` |
| `rgba(88, 101, 242, 0.2)` (flash-jump keyframe `0%`) | `var(--accent-soft)` |

For `@keyframes flash-jump`, replace:

```css
@keyframes flash-jump {
  0% {
    background-color: rgba(88, 101, 242, 0.2);
  }
  100% {
    background-color: transparent;
  }
}
```

with:

```css
@keyframes flash-jump {
  0% {
    background-color: var(--accent-soft);
  }
  100% {
    background-color: transparent;
  }
}
```

- [ ] **Step 3: Verify no literal colors remain**

Run: `cd chat-frontend && grep -nE '#[0-9a-fA-F]{3,8}\b|rgba?\(' src/styles/index.css`
Expected: No output (zero matches). If any remain, map them and re-run.

- [ ] **Step 4: Run the full test suite**

Run: `cd chat-frontend && npm test`
Expected: PASS — color tokens don't affect DOM/text assertions.

- [ ] **Step 5: Commit**

```bash
git add chat-frontend/src/styles/index.css
git commit -m "refactor(chat-frontend): replace hardcoded colors in index.css with theme tokens"
```

---

## Task 14: Manual verification

This task has no code change. Run the dev server, exercise both themes, and
fix any visual regressions you find by adjusting only the affected CSS rule
to use a more appropriate token (do not introduce new hex values).

- [ ] **Step 1: Start the dev server**

Run: `cd chat-frontend && npm run dev`
Open the app in a browser.

- [ ] **Step 2: Verify the light theme (default)**

With `localStorage.theme` cleared and OS in light mode:
- Login page renders white card on white background with the deeper-blue
  primary button.
- After login: chat header is white with a subtle bottom border; sidebar is
  pale gray; selected room has a light-blue background; messages are dark
  text on white.
- Open every dialog (CreateRoom, ManageMembers, LeaveRoom confirmation).
  Backgrounds, inputs, and buttons all read as light theme — no dark
  artifacts.
- Open the in-room search panel (Ctrl/Cmd-F) and the full search results
  pane (type in header search and press Enter). Both should match the light
  surfaces.
- Mention badges and error/success messages have legible contrast.

- [ ] **Step 3: Verify the dark theme**

Click the moon icon to switch to dark. Confirm the look matches the previous
Discord-ish appearance — same hexes in DevTools should resolve via the
tokens. Refresh the page; theme persists.

- [ ] **Step 4: Verify system-preference behavior**

Clear `localStorage.theme` in DevTools. Reload. Theme follows OS preference.
Change the OS to the opposite mode while the page is open — the theme
follows. Click the toggle once (now `source: 'user'`); change OS again — the
theme stays put.

- [ ] **Step 5: Verify no FOUC**

Hard-refresh the page (Cmd/Ctrl-Shift-R) several times in each theme.
Background should never flash the wrong color.

- [ ] **Step 6: Fix any issues, then commit**

If you adjusted any rule, commit with:

```bash
git add chat-frontend/src/styles/index.css
git commit -m "fix(chat-frontend): adjust theme token usage based on manual verification"
```

If no adjustments were needed, skip the commit.

---

## Task 15: Final lint + test pass + push

- [ ] **Step 1: Run the build to catch any Vite errors**

Run: `cd chat-frontend && npm run build`
Expected: PASS — clean build.

- [ ] **Step 2: Run the full test suite one more time**

Run: `cd chat-frontend && npm test`
Expected: PASS.

- [ ] **Step 3: Push the branch**

```bash
git push -u origin claude/lighter-chat-theme-8U53L
```

---

## Self-Review Checklist

**Spec coverage:** Every spec section is covered:
- Light/dark palette → Tasks 1, 13.
- `data-theme` switching mechanism → Tasks 1, 3, 7.
- ThemeContext (init order, mutations, subscription, error handling) → Tasks 2-7.
- ThemeToggle (icon swap, aria-label, click) → Tasks 8-9.
- Pre-paint script → Task 10.
- Wire-up (`main.jsx`, `ChatPage.jsx`) → Tasks 11-12.
- Manual verification of dialogs / panes / persistence / FOUC → Task 14.

**Placeholders:** None. Every code step has full code. Every command has expected output.

**Type / name consistency:** `theme`, `source`, `setTheme`, `toggleTheme` are used the same way in tests and implementation. Token names in `tokens.css` (Task 1) match every reference in the `index.css` mapping table (Task 13). The pre-paint script (Task 10) and `resolveInitial()` (Task 3) read the same `localStorage` key (`theme`) and the same media query (`(prefers-color-scheme: dark)`).
