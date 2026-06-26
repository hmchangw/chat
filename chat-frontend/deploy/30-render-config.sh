#!/bin/sh
# Renders /config.js from env vars at container start. AUTH_URL/NATS_URL/SITE_ID
# have no defaults: a misconfigured deploy must fail here, not send browsers to
# localhost.
set -eu

: "${AUTH_URL:?AUTH_URL is required (auth-service base URL)}"
: "${NATS_URL:?NATS_URL is required (NATS WebSocket URL)}"
: "${SITE_ID:?SITE_ID is required}"
: "${DEV_MODE:=false}"
: "${OIDC_ISSUER_URL:=}"
: "${OIDC_CLIENT_ID:=nats-chat}"
export AUTH_URL NATS_URL SITE_ID DEV_MODE OIDC_ISSUER_URL OIDC_CLIENT_ID

envsubst '${AUTH_URL} ${NATS_URL} ${SITE_ID} ${DEV_MODE} ${OIDC_ISSUER_URL} ${OIDC_CLIENT_ID}' \
  < /etc/config.js.template \
  > /usr/share/nginx/html/config.js

echo "rendered /config.js  AUTH_URL=$AUTH_URL  NATS_URL=$NATS_URL  SITE_ID=$SITE_ID  DEV_MODE=$DEV_MODE  OIDC_ISSUER_URL=$OIDC_ISSUER_URL  OIDC_CLIENT_ID=$OIDC_CLIENT_ID"
