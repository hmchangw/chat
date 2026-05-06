# Chat Frontend — Lighter Theme with Light/Dark Toggle

**Status:** Design approved, awaiting implementation plan
**Date:** 2026-05-06
**Scope:** `chat-frontend` only

## Summary

Add a light theme to `chat-frontend`, default to it, and let users toggle
between light and dark from the chat header. Implement via CSS custom
properties switched by a `data-theme` attribute on `<html>`. Respect the
operating system's `prefers-color-scheme` until the user makes an explicit
choice (which then persists in `localStorage` and wins over system changes).

## Motivation

The frontend currently ships only a Discord-inspired dark theme defined as
hardcoded hex values throughout `src/styles/index.css`. A lighter,
demo-friendly default plus a user toggle improves first-impression contrast,
broadens accessibility, and gives users control. A token-based CSS-variable
approach contains the change to one stylesheet and one tiny React context.

## Goals

- Light theme as the default (Slack-ish clean white).
- User-controllable toggle in the chat header (sun/moon icon button).
- Respect `prefers-color-scheme` on first load when no explicit choice exists.
- Persist user choice across reloads via `localStorage`.
- No flash of incorrect theme on initial paint.
- Existing component tests continue to pass without changes.

## Non-Goals

- A "system / light / dark" tri-state UI. Implicit "no choice = follow system"
  covers it.
- Per-component theming or user-customizable accent colors.
- Animated transitions between themes.
- Refactoring `index.css` structure beyond replacing color values with tokens.

## Architecture

### Token layer — `src/styles/tokens.css` (new)

Single source of truth for color tokens. Light palette declared on `:root`;
dark palette overrides via `:root[data-theme="dark"]`. Tokens are semantic,
not literal — components reference role (`--bg-sidebar`, `--text-muted`),
not appearance.

### Component layer — `src/styles/index.css` (edited in place)

Every hardcoded hex / `rgba()` is replaced with `var(--…)`. Selectors,
layout, spacing, typography untouched.

### React layer

- `src/context/ThemeContext.jsx` (new) — provider + `useTheme()` hook.
- `src/components/ThemeToggle.jsx` (new) — sun/moon icon button.
- `src/main.jsx` — import `tokens.css` before `index.css`, wrap `<App />` in
  `<ThemeProvider>`.
- `src/pages/ChatPage.jsx` — render `<ThemeToggle />` in `chat-header`,
  between `chat-header-user` and `chat-header-logout`.

### Pre-paint script — `index.html`

A tiny inline script in `<head>` resolves the initial theme and sets
`data-theme` on `<html>` before any stylesheet paints, preventing FOUC.

## Palette

### Light theme (`:root`, default)

| Token | Value | Usage |
|---|---|---|
| `--bg-app` | `#ffffff` | body background, message area |
| `--bg-sidebar` | `#f5f7fa` | room list, in-room search panel |
| `--bg-surface` | `#ffffff` | dialogs, header, message area |
| `--bg-elevated` | `#ebedf0` | search dropdown, footer strips |
| `--bg-input` | `#f0f2f5` | message input, search inputs |
| `--bg-input-hover` | `#e8eaed` | input hover |
| `--bg-hover` | `#ebedf0` | row hover (rooms, results) |
| `--bg-selected` | `#dbe2ea` | selected room, active result |
| `--text-primary` | `#1a1a1a` | message body, room names |
| `--text-secondary` | `#4a4a4a` | usernames, dialog labels |
| `--text-muted` | `#6b7280` | timestamps, meta, placeholders |
| `--text-on-accent` | `#ffffff` | text on accent buttons |
| `--border-subtle` | `#e4e6ea` | dividers, input borders |
| `--border-strong` | `#cfd3d8` | dashed create-room border |
| `--accent` | `#4752c4` | primary buttons, focus, selected tab |
| `--accent-hover` | `#3a44a8` | button hover |
| `--accent-soft` | `rgba(71, 82, 196, 0.15)` | focus ring, flash highlight |
| `--status-error` | `#b42318` | error text |
| `--status-error-bg` | `#fef3f2` | error backgrounds |
| `--status-success` | `#0f8a4f` | success text |
| `--status-success-bg` | `#ecfdf3` | success backgrounds |
| `--mention` | `#b45309` | individual mention badge |
| `--mention-all` | `#b42318` | @here/@all badge |
| `--shadow-dialog` | `0 8px 24px rgba(0, 0, 0, 0.12)` | dialog / dropdown shadow |
| `--scrim` | `rgba(15, 23, 42, 0.45)` | dialog overlay |

### Dark theme (`:root[data-theme="dark"]`, preserves current look)

| Token | Value |
|---|---|
| `--bg-app` | `#313338` |
| `--bg-sidebar` | `#2b2d31` |
| `--bg-surface` | `#313338` |
| `--bg-elevated` | `#232529` |
| `--bg-input` | `#383a40` |
| `--bg-input-hover` | `#404249` |
| `--bg-hover` | `#35373c` |
| `--bg-selected` | `#404249` |
| `--text-primary` | `#f2f3f5` |
| `--text-secondary` | `#dbdee1` |
| `--text-muted` | `#949ba4` |
| `--text-on-accent` | `#ffffff` |
| `--border-subtle` | `#3f4147` |
| `--border-strong` | `#4e5058` |
| `--accent` | `#5865f2` |
| `--accent-hover` | `#4752c4` |
| `--accent-soft` | `rgba(88, 101, 242, 0.20)` |
| `--status-error` | `#f38ba8` |
| `--status-error-bg` | `rgba(243, 139, 168, 0.10)` |
| `--status-success` | `#43b581` |
| `--status-success-bg` | `rgba(67, 181, 129, 0.12)` |
| `--mention` | `#faa61a` |
| `--mention-all` | `#ed4245` |
| `--shadow-dialog` | `0 8px 24px rgba(0, 0, 0, 0.32)` |
| `--scrim` | `rgba(0, 0, 0, 0.6)` |

### Mapping notes for the `index.css` rewrite

- The dark theme's chat header uses `#1e1f22` (darker than the sidebar). For
  light, it should be `--bg-surface` (white) with a `--border-subtle` bottom
  border — solid white headers read better than gray-on-gray. The dark
  theme's header maps to `--bg-elevated`.
- The header search bar's `rgba(255,255,255,0.06)` overlay maps to
  `var(--bg-input)` in both themes — semantic, not literal — so the input has
  a clear shape on white as well as dark.
- The "jump latest" pill uses `--accent` and `--text-on-accent` in both themes.

## Components & Data Flow

### `ThemeContext`

```
{
  theme: 'light' | 'dark',     // currently applied
  source: 'system' | 'user',    // for tests / introspection
  setTheme(next),               // explicit choice; persists
  toggleTheme(),                // flip and persist
}
```

**Initialization order on mount:**

1. Read `localStorage.theme`. If it equals `"light"` or `"dark"`, use it and
   set `source: 'user'`.
2. Else read `window.matchMedia('(prefers-color-scheme: dark)')`. Use
   `"dark"` if it matches, `"light"` otherwise. Set `source: 'system'`.
3. If `matchMedia` is undefined, default to `"light"` with `source: 'system'`.
4. Apply by setting `document.documentElement.dataset.theme`.

**`matchMedia` change subscription:**

- Always registered.
- Handler updates `theme` only when `source === 'system'` (no explicit user
  choice). When `source === 'user'`, the handler no-ops.

**`setTheme(next)`:**

- Writes `localStorage.theme = next` (try/catch — ignore failures).
- Sets `document.documentElement.dataset.theme = next`.
- Updates context state with `source: 'user'`.

**`toggleTheme()`:**

- Calls `setTheme(theme === 'dark' ? 'light' : 'dark')`.

### `ThemeToggle`

- `<button>` placed in `chat-header` between `chat-header-user` and
  `chat-header-logout`.
- Renders an inline SVG (no new dependency): sun icon when `theme === 'dark'`
  (clicking switches to light), moon icon when `theme === 'light'` (clicking
  switches to dark).
- `aria-label`: `"Switch to dark theme"` or `"Switch to light theme"` based
  on current state.
- Sized to match `chat-header-logout`.
- Calls `toggleTheme()` on click.

### Pre-paint script

Inline in `index.html` `<head>`, before any stylesheet link:

```html
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
```

The `ThemeContext` `useState` initializer mirrors this logic exactly so the
inline script and the React state agree on first render.

## Error Handling & Edge Cases

- **`localStorage` unavailable** (private mode, disabled): all reads/writes
  wrapped in try/catch. On read failure, fall back to `prefers-color-scheme`.
  On write failure, the theme still applies in-memory but does not persist.
  No user-visible error.
- **Invalid `localStorage.theme` value** (anything other than `"light"` /
  `"dark"`): treat as absent, fall back to system preference.
- **`matchMedia` unavailable** (very old browsers, jsdom default): use
  optional chaining; if undefined, default to `"light"`.
- **Race between inline script and React init:** both read the same sources
  in the same order, so they always agree on first render.

## Testing

### `src/context/ThemeContext.test.jsx` (new)

Table-driven tests for the provider/hook:

- Initial theme = `"dark"` when `localStorage.theme = "dark"`.
- Initial theme = `"light"` when `localStorage.theme = "light"`.
- Initial theme follows `matchMedia` when `localStorage` is empty.
- Initial theme defaults to `"light"` when `matchMedia` is undefined.
- `setTheme("dark")` updates `document.documentElement.dataset.theme` and
  writes `localStorage.theme`.
- `toggleTheme()` flips light→dark and dark→light.
- System preference `change` event updates theme when `source === 'system'`.
- System preference `change` event is ignored when `source === 'user'`.
- `localStorage.setItem` throwing does not crash the provider.

### `src/components/ThemeToggle.test.jsx` (new)

- Renders a button with the correct `aria-label` for each theme.
- Click invokes `toggleTheme` (verify by checking `data-theme` attribute
  changes).
- Renders the sun icon when current theme is dark, moon icon when light.

### Existing tests

All current component tests render via `@testing-library/react` and assert on
DOM structure / text — none assert on color hex values, so they continue to
pass unchanged. `ChatPage.test.jsx` (and any other test that renders
components consuming `useTheme`) gets a `ThemeProvider` wrapper if it doesn't
already render `<App />`. A small `renderWithTheme` helper may be added to
the existing test setup.

### Manual / demo verification

- `npm run dev`; both themes render with no broken contrast.
- Toggle in the header, refresh — selection persists.
- Clear `localStorage.theme`, set OS to dark, refresh — page comes up dark;
  switch OS to light, page follows.
- Set explicit choice, change OS — page does not follow (explicit wins).
- Open every dialog (CreateRoom, ManageMembers, LeaveRoom confirmation), the
  in-room search panel, the full search results pane, and the login page —
  no hardcoded colors leak through.

## Files Changed

**New:**
- `chat-frontend/src/styles/tokens.css`
- `chat-frontend/src/context/ThemeContext.jsx`
- `chat-frontend/src/context/ThemeContext.test.jsx`
- `chat-frontend/src/components/ThemeToggle.jsx`
- `chat-frontend/src/components/ThemeToggle.test.jsx`

**Edited:**
- `chat-frontend/src/styles/index.css` — replace hardcoded colors with tokens.
- `chat-frontend/src/main.jsx` — import `tokens.css`; wrap in `ThemeProvider`.
- `chat-frontend/index.html` — inline pre-paint script.
- `chat-frontend/src/pages/ChatPage.jsx` — render `<ThemeToggle />` in header.
- (As needed) test files that render themed components without `<App />`.

## Risks & Mitigations

- **Risk:** A token gets missed and a hardcoded color slips through. **Mitigation:**
  After the rewrite, grep `index.css` for `#`, `rgb(`, and `rgba(` and confirm
  every match lives inside a `var(--…)` definition or a comment. This is part
  of the implementation plan's verification step.
- **Risk:** Light-theme contrast fails for status colors / mention badges.
  **Mitigation:** Light status colors chosen from a tested palette
  (Tailwind-style 600/700 weights for foreground on near-white backgrounds).
  Manual verification step covers all dialogs and badges.
- **Risk:** Tests that assert presence of dark-specific markup break.
  **Mitigation:** Tests assert on text and DOM structure, not colors; spot-check
  during implementation and add a `ThemeProvider` wrapper where missing.
