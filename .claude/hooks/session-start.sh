#!/bin/bash
set -euo pipefail

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}"

# Add superpowers marketplace (if not already added)
claude plugin marketplace add obra/superpowers-marketplace 2>/dev/null || true

# Install superpowers plugin
claude plugin install superpowers@superpowers-marketplace --scope project

# Persist go-installed tools' bin dir on PATH for the whole session, not just
# this subshell — gosec/govulncheck/air/mockgen all install here.
GOBIN_DIR="$(go env GOPATH)/bin"
export PATH="${GOBIN_DIR}:${PATH}"
if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
  echo "export PATH=\"${GOBIN_DIR}:\${PATH}\"" >> "$CLAUDE_ENV_FILE"
fi

# semgrep installs via pipx (see Makefile `tools` target); pipx isn't in the
# base image, but uv is — use it to provide pipx.
command -v pipx >/dev/null 2>&1 || uv tool install pipx

# Install pinned dev/SAST tooling (golangci-lint, gosec, govulncheck, air,
# semgrep). Versions are owned by the Makefile so they stay in sync with CI.
make tools

# mockgen is not part of `make tools`; pin to the go.mod version so generated
# mocks match. Needed by `make generate`.
go install go.uber.org/mock/mockgen@v0.6.0

# Download Go module dependencies and pre-populate source cache
# go mod download fetches .mod/.info but not full source zips
# go vet forces download of all source needed by golangci-lint typecheck
# klauspost/compress often fails via the Go module proxy — download directly first
GOPROXY=direct go mod download github.com/klauspost/compress || true
GOPROXY=https://proxy.golang.org,direct go mod download
GOPROXY=https://proxy.golang.org,direct go vet ./... || true
