#!/bin/sh
# Renders /config.js from env vars at container start.
set -eu

: "${PORTAL_URL:=http://localhost:8080}"
: "${DEFAULT_SITE_ID:=site-local}"
: "${DEV_MODE:=false}"
: "${OIDC_ISSUER_URL:=}"
: "${OIDC_CLIENT_ID:=nats-chat}"
export PORTAL_URL DEFAULT_SITE_ID DEV_MODE OIDC_ISSUER_URL OIDC_CLIENT_ID

envsubst '${PORTAL_URL} ${DEFAULT_SITE_ID} ${DEV_MODE} ${OIDC_ISSUER_URL} ${OIDC_CLIENT_ID}' \
  < /etc/config.js.template \
  > /usr/share/nginx/html/config.js

echo "rendered /config.js  PORTAL_URL=$PORTAL_URL  DEFAULT_SITE_ID=$DEFAULT_SITE_ID  DEV_MODE=$DEV_MODE  OIDC_ISSUER_URL=$OIDC_ISSUER_URL  OIDC_CLIENT_ID=$OIDC_CLIENT_ID"
