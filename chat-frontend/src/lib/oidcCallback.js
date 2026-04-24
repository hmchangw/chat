const CALLBACK_PARAMS = [
  'code',
  'state',
  'session_state',
  'iss',
  'error',
  'error_description',
]

export function isOidcCallbackUrl(urlString) {
  const url = new URL(urlString)
  const hasState = url.searchParams.has('state')
  const hasCodeOrError =
    url.searchParams.has('code') || url.searchParams.has('error')
  return hasState && hasCodeOrError
}

export function stripOidcCallbackParams(urlString) {
  const url = new URL(urlString)
  for (const key of CALLBACK_PARAMS) {
    url.searchParams.delete(key)
  }
  const query = url.searchParams.toString()
  return query ? `${url.origin}${url.pathname}?${query}` : `${url.origin}${url.pathname}`
}
