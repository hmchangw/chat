// Runtime config: window.__APP_CONFIG__ in prod (rendered by nginx envsubst),
// import.meta.env.VITE_* in `vite dev`, literal defaults as last resort.

const runtime = (typeof window !== 'undefined' && window.__APP_CONFIG__) || {}

// In GitHub Codespaces the browser runs on the user's laptop, so `localhost`
// targets their machine — not the codespace. Each forwarded port is exposed
// at `<name>-<port>.<suffix>` (suffix e.g. `app.github.dev`). When we're
// served from such a hostname, derive sibling-port URLs from it.
function deriveCodespacesUrl(port, protocol) {
  if (typeof window === 'undefined' || !window.location) return null
  const m = window.location.hostname.match(
    /^(.+)-\d+\.(app\.github\.dev|githubpreview\.dev|preview\.app\.github\.dev)$/,
  )
  if (!m) return null
  return `${protocol}//${m[1]}-${port}.${m[2]}`
}

export const AUTH_URL =
  runtime.AUTH_URL ||
  import.meta.env.VITE_AUTH_URL ||
  deriveCodespacesUrl(8080, 'https:') ||
  'http://localhost:8080'

export const NATS_URL =
  runtime.NATS_URL ||
  import.meta.env.VITE_NATS_URL ||
  deriveCodespacesUrl(9222, 'wss:') ||
  'ws://localhost:9222'

export const DEFAULT_SITE_ID =
  runtime.DEFAULT_SITE_ID || import.meta.env.VITE_DEFAULT_SITE_ID || 'site-local'

export const DEV_MODE =
  (runtime.DEV_MODE ?? import.meta.env.VITE_DEV_MODE ?? 'true') === 'true'

export const OIDC_ISSUER_URL =
  runtime.OIDC_ISSUER_URL ||
  import.meta.env.VITE_OIDC_ISSUER_URL ||
  'http://localhost:8180/realms/chatapp'

export const OIDC_CLIENT_ID =
  runtime.OIDC_CLIENT_ID || import.meta.env.VITE_OIDC_CLIENT_ID || 'nats-chat'
