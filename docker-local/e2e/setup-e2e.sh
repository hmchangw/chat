#!/usr/bin/env bash
#
# Generates NATS operator + chatapp account JWTs + backend.creds for the e2e
# supercluster. BOTH site A and site B NATS instances share the same operator
# and chatapp account so a JWT minted on either site is accepted by the other
# (gateway-federated accounts). One operator + one account is the simplest
# correct topology for a test environment; production trust isolation across
# operators is intentionally not modelled here.
#
# Uses the nats-box Docker image so it works on any OS without requiring local
# nsc/nk installation. Forked from docker-local/setup.sh; key differences:
#   - Outputs to docker-local/e2e/secrets/ (gitignored), not docker-local/
#   - Emits an auth.conf fragment that both NATS configs `include`
#   - Emits e2e-secrets.env with AUTH_SIGNING_KEY for the auth-service-{a,b}
#     containers' `env_file` directive
#   - Idempotent: skips if secrets/operator.jwt already exists
#
# Run by `make e2e-up` via the $(E2E_SECRETS) prerequisite. The Makefile
# rule only invokes this on the first run; rerun manually with
# `rm -rf docker-local/e2e/secrets && make e2e-up` to regenerate.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SECRETS_DIR="$SCRIPT_DIR/secrets"
NATS_BOX_IMAGE="${NATS_BOX_IMAGE:-natsio/nats-box:latest}"

if [ -f "$SECRETS_DIR/operator.jwt" ]; then
  echo "[setup-e2e] $SECRETS_DIR/operator.jwt exists; nothing to do."
  exit 0
fi

if ! command -v docker &>/dev/null; then
  echo "[setup-e2e] ERROR: docker not found. Install Docker first."
  exit 1
fi

# host.docker.internal must resolve on the host so the e2e harness can reach
# Keycloak via the same URL that auth-service-{a,b} uses (preserves OIDC iss
# claim alignment). Docker Desktop sets this up automatically; plain Linux
# Docker Engine users must add it once.
if ! getent hosts host.docker.internal >/dev/null 2>&1; then
  echo "[setup-e2e] WARNING: host.docker.internal does not resolve on this host."
  echo "  On Docker Desktop (Mac/Windows) it's automatic. On plain Linux Docker"
  echo "  Engine, run once:"
  echo "    echo '127.0.0.1 host.docker.internal' | sudo tee -a /etc/hosts"
  echo ""
fi

echo "[setup-e2e] generating NATS operator + chatapp account via nats-box..."

mkdir -p "$SECRETS_DIR"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# Inside the nats-box container: provision a fresh nsc store, create operator
# + chatapp account + SYS account + backend user, emit the JWTs and the
# account seed. Extraction patterns mirror docker-local/setup.sh -- the
# `Account ID` grep + awk is the proven path; the chapter-4 plan's
# `nsc describe --field sub --raw` shape doesn't reliably work across
# nsc versions and was replaced per amendment R1 4.A.
docker run --rm \
  -v "$TMPDIR:/output" \
  "$NATS_BOX_IMAGE" \
  sh -c '
    set -e

    nsc add operator --name e2e --sys 2>&1 | sed "s/^/  /"
    nsc env -o e2e >/dev/null 2>&1

    nsc add account --name chatapp 2>&1 | sed "s/^/  /"
    nsc edit account chatapp --js-mem-storage 512M --js-disk-storage 5G --js-streams 32 2>&1 | sed "s/^/  /"

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

    nsc add user --account chatapp --name backend
    nsc edit user --account chatapp --name backend --allow-sub ">" --allow-pub ">"
    nsc generate creds --account chatapp --name backend > /output/backend.creds
  '

cp "$TMPDIR/operator.jwt"  "$SECRETS_DIR/operator.jwt"
cp "$TMPDIR/account.jwt"   "$SECRETS_DIR/account.jwt"
cp "$TMPDIR/sys.jwt"       "$SECRETS_DIR/sys.jwt"
cp "$TMPDIR/backend.creds" "$SECRETS_DIR/backend.creds"

OPERATOR_JWT=$(cat "$TMPDIR/operator.jwt")
ACCOUNT_JWT=$(cat "$TMPDIR/account.jwt")
SYS_JWT=$(cat "$TMPDIR/sys.jwt")
ACCOUNT_PUB_KEY=$(cat "$TMPDIR/account_pub.txt")
SYS_PUB_KEY=$(cat "$TMPDIR/sys_pub.txt")
ACCOUNT_SEED=$(cat "$TMPDIR/account_seed.txt")

# auth.conf fragment is `include`'d by both nats-{a,b}.conf. Includes:
#   - operator: root of trust for both clusters
#   - system_account: required by NATS to perform internal heartbeats with
#     elevated permissions (per amendment R3.D's belt-and-braces position)
#   - resolver_preload: account JWTs that resolver: MEMORY serves
cat > "$SECRETS_DIR/auth.conf" <<EOF
operator: ${OPERATOR_JWT}
system_account: ${SYS_PUB_KEY}
resolver: MEMORY
resolver_preload {
  ${ACCOUNT_PUB_KEY}: ${ACCOUNT_JWT}
  ${SYS_PUB_KEY}: ${SYS_JWT}
}
EOF

cat > "$SECRETS_DIR/e2e-secrets.env" <<EOF
# Generated by docker-local/e2e/setup-e2e.sh.
# Sourced via compose env_file: into auth-service-{a,b}.
AUTH_SIGNING_KEY=${ACCOUNT_SEED}
EOF
# backend.creds is read by 22 services running as a mix of root and non-root
# users (search-service / search-sync-worker drop to uid 10001 `app`); 644 lets
# every container user read it. e2e-secrets.env carries the chatapp account
# SEED -- only auth-service mounts it, and auth-service runs as root, so 600
# is safe there.
chmod 644 "$SECRETS_DIR/backend.creds"
chmod 600 "$SECRETS_DIR/e2e-secrets.env"

echo "[setup-e2e] wrote:"
echo "  $SECRETS_DIR/operator.jwt"
echo "  $SECRETS_DIR/account.jwt"
echo "  $SECRETS_DIR/sys.jwt"
echo "  $SECRETS_DIR/backend.creds"
echo "  $SECRETS_DIR/auth.conf"
echo "  $SECRETS_DIR/e2e-secrets.env"
