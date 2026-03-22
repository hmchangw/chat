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

# Install Go tools
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Download Go module dependencies
go mod download
