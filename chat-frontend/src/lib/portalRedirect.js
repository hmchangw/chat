// Navigates to a portal-issued redirectTo target (wrong-site login or
// mid-session site move). Returns true when navigation was triggered.
export function followPortalRedirect(data) {
  if (!data?.redirectTo) return false
  window.location.assign(data.redirectTo)
  return true
}
