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

export async function signinRedirect(siteId) {
  if (siteId) {
    window.sessionStorage.setItem('oidc.siteId', siteId)
  }
  await getUserManager().signinRedirect()
}

export function popSiteId() {
  const v = window.sessionStorage.getItem('oidc.siteId')
  window.sessionStorage.removeItem('oidc.siteId')
  return v
}

export async function completeSigninIfCallback() {
  const currentUrl = window.location.href
  if (!isOidcCallbackUrl(currentUrl)) return null

  const user = await getUserManager().signinRedirectCallback()
  const cleaned = stripOidcCallbackParams(currentUrl)
  window.history.replaceState({}, '', cleaned)
  return user
}
