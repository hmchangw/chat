#!/bin/sh
# Renders /config.js from env vars at container start.
set -eu

: "${AUTH_URL:=http://localhost:8080}"
: "${NATS_URL:=ws://localhost:9222}"
: "${DEFAULT_SITE_ID:=site-local}"
export AUTH_URL NATS_URL DEFAULT_SITE_ID

envsubst '${AUTH_URL} ${NATS_URL} ${DEFAULT_SITE_ID}' \
  < /etc/config.js.template \
  > /usr/share/nginx/html/config.js

echo "rendered /config.js  AUTH_URL=$AUTH_URL  NATS_URL=$NATS_URL  DEFAULT_SITE_ID=$DEFAULT_SITE_ID"
