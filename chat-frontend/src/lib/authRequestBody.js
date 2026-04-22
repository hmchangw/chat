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
