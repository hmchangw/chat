#!/usr/bin/env bash
#
# Generates per-site NATS operator + account keys for the two local-dev NATS
# instances (site-a, site-b) and writes their nats.conf, backend.creds,
# leaf.creds, account.seed, and per-site env files. Uses the nats-box Docker
# image so it works on any OS without requiring local nsc/nk installation.
#
# Each site's nats.conf includes a leafnode remote pointing at the other
# site's NATS, using the peer's leaf user creds (mounted into the container
# by compose.deps.yaml). The leafnode bridges the chatapp account between
# the two NATSes so JetStream stream sources can pull across them; the
# actual cross-site source wiring is applied by tools/federation-init.
#
# Run once before `make deps-up`.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
FRONTEND_ENV_FILE="$REPO_ROOT/chat-frontend/.env.local"
NATS_BOX_IMAGE="natsio/nats-box:latest"

# Per-site host port assignments. Site-a keeps the historical defaults so
# standalone `docker compose -f <service>/deploy/docker-compose.yml up`
# still matches today's behavior; site-b shifts on each exposed service.
SITE_A_AUTH_PORT=8080
SITE_B_AUTH_PORT=8081
SITE_A_HISTORY_PORT=8082
SITE_B_HISTORY_PORT=8083
SITE_A_ROOM_PORT=8084
SITE_B_ROOM_PORT=8085
SITE_A_SEARCH_PORT=8086
SITE_B_SEARCH_PORT=8087
SITE_A_MOCK_USER_PORT=8088
SITE_B_MOCK_USER_PORT=8089
SITE_A_SEARCH_METRICS_PORT=9090
SITE_B_SEARCH_METRICS_PORT=9091

echo "=== docker-local — Multi-site NATS setup (site-a + site-b) ==="
echo ""

if ! command -v docker &>/dev/null; then
  echo "ERROR: docker not found. Install Docker first."
  exit 1
fi

# gen_keys <site>: generates an operator + chatapp account + backend user +
# leaf user inside a nats-box container, then copies the artifacts into
# docker-local/<site>/.
gen_keys() {
  local site="$1"
  local site_dir="$SCRIPT_DIR/$site"
  mkdir -p "$site_dir"

  echo "--- Generating $site keys ---"
  local tmpdir
  tmpdir=$(mktemp -d)

  docker run --rm \
    -v "$tmpdir:/output" \
    -e SITE="$site" \
    "$NATS_BOX_IMAGE" \
    sh -c '
      set -e

      nsc add operator --name "localdev-${SITE}" --sys 2>&1 | sed "s/^/  /"
      nsc env -o "localdev-${SITE}" >/dev/null 2>&1

      nsc add account --name chatapp 2>&1 | sed "s/^/  /"
      nsc edit account chatapp --js-mem-storage 512M --js-disk-storage 5G --js-streams 10 2>&1 | sed "s/^/  /"

      nsc describe operator --raw > /output/operator.jwt
      nsc describe account chatapp --raw > /output/account.jwt
      nsc describe account SYS --raw > /output/sys.jwt

      nsc describe account chatapp 2>/dev/null | grep "Account ID" | awk -F"|" "{gsub(/[ \t]/, \"\", \$3); print \$3}" > /output/account_pub.txt
      nsc describe account SYS 2>/dev/null | grep "Account ID" | awk -F"|" "{gsub(/[ \t]/, \"\", \$3); print \$3}" > /output/sys_pub.txt

      ACCOUNT_PUB=$(cat /output/account_pub.txt)
      SEED_FILE=$(find /root/.local/share/nats/nsc/keys -name "${ACCOUNT_PUB}.nk" 2>/dev/null | head -1)
      if [ -z "$SEED_FILE" ]; then
        SEED_FILE=$(find /nsc -name "${ACCOUNT_PUB}.nk" 2>/dev/null | head -1)
      fi
      if [ -z "$SEED_FILE" ]; then
        echo "ERROR: Could not find account seed for ${ACCOUNT_PUB}"
        exit 1
      fi
      cat "$SEED_FILE" > /output/account_seed.txt

      # backend user: read/write everywhere; used by every microservice.
      nsc add user --account chatapp --name backend
      nsc edit user --account chatapp --name backend --allow-sub ">" --allow-pub ">"
      nsc generate creds --account chatapp --name backend > /output/backend.creds

      # leaf user: identical permissions, but used by the PEER site as a
      # leafnode remote credential so it can connect into this account.
      # Separate from backend so revoking the leaf bridge does not also
      # log every microservice out.
      nsc add user --account chatapp --name leaf
      nsc edit user --account chatapp --name leaf --allow-sub ">" --allow-pub ">"
      nsc generate creds --account chatapp --name leaf > /output/leaf.creds
    '

  cp "$tmpdir/operator.jwt"     "$site_dir/operator.jwt"
  cp "$tmpdir/account.jwt"      "$site_dir/account.jwt"
  cp "$tmpdir/sys.jwt"          "$site_dir/sys.jwt"
  cp "$tmpdir/account_pub.txt"  "$site_dir/account_pub.txt"
  cp "$tmpdir/sys_pub.txt"      "$site_dir/sys_pub.txt"
  cp "$tmpdir/backend.creds"    "$site_dir/backend.creds"
  cp "$tmpdir/leaf.creds"       "$site_dir/leaf.creds"
  cp "$tmpdir/account_seed.txt" "$site_dir/account.seed"
  # 0644 on creds: service containers run as non-root (uid 10001) and
  # bind-mount these read-only — the in-container user must read them.
  # Acceptable only because these are throwaway local-dev credentials.
  chmod 644 "$site_dir/backend.creds" "$site_dir/leaf.creds"
  chmod 600 "$site_dir/account.seed"

  rm -rf "$tmpdir"
}

# write_nats_conf <site> <peer_nats_host>: writes <site>/nats.conf with a
# JetStream domain matching <site> and a leafnode remote pointing at
# <peer_nats_host>:7422 using the peer's leaf.creds (mounted as
# /etc/nats/peer.creds in the NATS container).
write_nats_conf() {
  local site="$1"
  local peer_nats_host="$2"
  local site_dir="$SCRIPT_DIR/$site"

  local op_jwt acc_jwt sys_jwt acc_pub sys_pub
  op_jwt=$(cat "$site_dir/operator.jwt")
  acc_jwt=$(cat "$site_dir/account.jwt")
  sys_jwt=$(cat "$site_dir/sys.jwt")
  acc_pub=$(cat "$site_dir/account_pub.txt")
  sys_pub=$(cat "$site_dir/sys_pub.txt")

  cat > "$site_dir/nats.conf" <<EOF
# Generated by docker-local/setup.sh — do not commit this file.
# Regenerate with: ./docker-local/setup.sh

port: 4222
http_port: 8222

operator: ${op_jwt}

resolver: MEMORY

resolver_preload {
  ${acc_pub}: ${acc_jwt}
  ${sys_pub}: ${sys_jwt}
}

jetstream {
  # Domain-scoped so tools/federation-init can address streams on the
  # peer site as OUTBOX_<peer>@<peer>-domain.
  domain: ${site}
  store_dir: /data/jetstream
  max_mem: 1G
  max_file: 10G
}

websocket {
  port: 9222
  no_tls: true
}

# Leafnode bridge to the peer site. The peer's leaf.creds is mounted at
# /etc/nats/peer.creds by compose.deps.yaml; binding it to THIS site's
# chatapp account lets JetStream sources pull OUTBOX_<peer> across the
# bridge with no External config.
leafnodes {
  port: 7422
  remotes: [
    {
      url: "nats-leaf://${peer_nats_host}:7422"
      credentials: "/etc/nats/peer.creds"
      account: "${acc_pub}"
    }
  ]
}
EOF
}

# write_env <site> <nats_host> <ports…>
write_env() {
  local site="$1"
  local nats_host="$2"
  local auth_port="$3"
  local history_port="$4"
  local room_port="$5"
  local search_port="$6"
  local mock_user_port="$7"
  local search_metrics_port="$8"
  local env_file="$SCRIPT_DIR/${site}.env"

  local acc_seed
  acc_seed=$(cat "$SCRIPT_DIR/$site/account.seed")

  cat > "$env_file" <<EOF
# Generated by docker-local/setup.sh — do not commit this file.
# Regenerate with: ./docker-local/setup.sh

SITE_ID=${site}
NATS_HOST=${nats_host}

# auth-service signs user JWTs with the chatapp account nkey (private seed).
# All other microservices authenticate with backend.creds via NATS_CREDS_FILE.
AUTH_SIGNING_KEY=${acc_seed}

# Bypass OIDC in auth-service; flip to false to test the OIDC flow.
DEV_MODE=true

# Host port mappings for services that publish a port on the host.
AUTH_HOST_PORT=${auth_port}
HISTORY_HOST_PORT=${history_port}
ROOM_HOST_PORT=${room_port}
SEARCH_HOST_PORT=${search_port}
MOCK_USER_HOST_PORT=${mock_user_port}
SEARCH_METRICS_HOST_PORT=${search_metrics_port}
EOF
  chmod 600 "$env_file"
}

# --- Pass 1: generate keys for both sites --------------------------------
gen_keys site-a
gen_keys site-b

# --- Pass 2: write nats.conf with cross-site leafnode refs ---------------
write_nats_conf site-a nats-b
write_nats_conf site-b nats-a

# --- Pass 3: write per-site env files ------------------------------------
write_env site-a nats-a \
  "$SITE_A_AUTH_PORT" "$SITE_A_HISTORY_PORT" "$SITE_A_ROOM_PORT" \
  "$SITE_A_SEARCH_PORT" "$SITE_A_MOCK_USER_PORT" "$SITE_A_SEARCH_METRICS_PORT"

write_env site-b nats-b \
  "$SITE_B_AUTH_PORT" "$SITE_B_HISTORY_PORT" "$SITE_B_ROOM_PORT" \
  "$SITE_B_SEARCH_PORT" "$SITE_B_MOCK_USER_PORT" "$SITE_B_SEARCH_METRICS_PORT"

# Drop any pre-multisite single-site shims; no compose references them.
rm -f "$SCRIPT_DIR/.env" "$SCRIPT_DIR/nats.conf" "$SCRIPT_DIR/backend.creds"

# chat-frontend/.env.local feeds `npm run dev` (Vite). Written only on first
# run so devs can edit it (e.g. point at staging) without losing changes.
if [ ! -f "$FRONTEND_ENV_FILE" ]; then
  cat > "$FRONTEND_ENV_FILE" <<EOF
VITE_AUTH_URL=http://localhost:${SITE_A_AUTH_PORT}
VITE_NATS_URL=ws://localhost:9222
VITE_DEFAULT_SITE_ID=site-a
EOF
fi

echo ""
echo "=== Ready! ==="
echo ""
echo "  # Start third-party deps (both NATSes + shared Mongo/Cassandra/ES/etc.)"
echo "  make deps-up"
echo ""
echo "  # Start every microservice for both sites (foreground, streams logs)"
echo "  make up"
echo ""
echo "  ──────────────────────────────────────"
echo "  site-a NATS:        nats://localhost:4222   (ws: 9222, monitor: 8222)"
echo "  site-b NATS:        nats://localhost:4322   (ws: 9322, monitor: 8322)"
echo "  ──────────────────────────────────────"
echo ""
