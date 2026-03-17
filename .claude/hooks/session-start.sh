#!/bin/bash
set -euo pipefail

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}"

# Register the superpowers marketplace and install plugin
claude plugin marketplace add obra/superpowers-marketplace --scope project
claude plugin install superpowers@superpowers-marketplace --scope project

# Download Go module dependencies
go mod download
