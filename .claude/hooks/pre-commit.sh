#!/bin/bash
set -euo pipefail

# Read tool input JSON from stdin
INPUT=$(cat)

# Extract the command from the Bash tool input
COMMAND=$(echo "$INPUT" | jq -r '.tool_input.command // empty')

# Only run checks for git commit commands
if ! echo "$COMMAND" | grep -qE '\bgit\s+commit\b'; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}"

# Ensure ~/go/bin is on PATH for go install'd tools
export PATH="${HOME}/go/bin:${PATH}"

# Check if only non-Go files are staged (docs, config, etc.)
STAGED_GO_FILES=$(git diff --cached --name-only --diff-filter=ACMR -- '*.go' 'go.mod' 'go.sum' | head -1)
if [ -z "$STAGED_GO_FILES" ]; then
  echo "Pre-commit: only non-Go files staged, skipping lint and tests." >&2
  exit 0
fi

echo "Pre-commit: running make lint..." >&2
if ! make lint >&2 2>&1; then
  echo "BLOCKED: make lint failed. Fix lint errors before committing." >&2
  exit 2
fi

echo "Pre-commit: running make test..." >&2
if ! make test >&2 2>&1; then
  echo "BLOCKED: make test failed. Fix test failures before committing." >&2
  exit 2
fi

echo "Lint and tests passed." >&2
exit 0
