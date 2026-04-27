export function resolveAuthMode(env) {
  const raw = env?.VITE_AUTH_MODE ?? ''
  if (raw === '' || raw === 'dev') return 'dev'
  if (raw === 'oidc') return 'oidc'
  throw new Error(`VITE_AUTH_MODE must be "dev" or "oidc" (got "${raw}")`)
}

export const AUTH_MODE = resolveAuthMode(import.meta.env)
