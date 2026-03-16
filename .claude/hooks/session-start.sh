#!/bin/bash
set -euo pipefail

# Only run in remote (web) environments
if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}"

# Download Go module dependencies if go.mod exists
if [ -f "go.mod" ]; then
  echo "Installing Go dependencies..."
  go mod download
fi
