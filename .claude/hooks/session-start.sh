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

# Ensure ~/go/bin is on PATH for go install'd tools
export PATH="${HOME}/go/bin:${PATH}"

# Install Go tools (golangci-lint v2 is pre-installed at /usr/local/bin)
go install go.uber.org/mock/mockgen@latest

# Download Go module dependencies
go mod download
