#!/usr/bin/env bash
# new-scenario.sh — scaffold a new scenario.
#
# Usage:
#   ./new-scenario.sh NAME
#
# Creates:
#   - tools/loadgen/scripts/run-NAME.sh  (from quickstart.sh template)
#   - tools/loadgen/scenario_NAME.go     (registration stub with panic placeholder)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

usage() {
    cat <<EOF
Usage: $(basename "$0") NAME

Scaffolds a new scenario:
  - tools/loadgen/scenario_NAME.go     (registration stub)
  - tools/loadgen/scripts/run-NAME.sh  (operator script)

Next steps (printed at end):
  1. Implement NewGenerator in scenario_NAME.go.
  2. Add a test file scenario_NAME_test.go.
  3. Append |NAME to the --scenario help in flags.go.
  4. Regenerate the golden file.
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }
if [ $# -ne 1 ]; then usage >&2; exit 2; fi

NAME="$1"
if ! [[ "$NAME" =~ ^[a-z][a-z0-9-]+$ ]]; then
    echo "error: NAME must be lowercase alphanumeric with hyphens; got '$NAME'" >&2
    exit 2
fi

TYPE_NAME="$(echo "$NAME" | awk -F- '{for(i=1;i<=NF;i++) printf "%s%s", toupper(substr($i,1,1)), substr($i,2); print ""}')Scenario"

SCENARIO_FILE="$ROOT_DIR/tools/loadgen/scenario_${NAME//-/_}.go"
SCRIPT_FILE="$ROOT_DIR/tools/loadgen/scripts/run-$NAME.sh"

if [ -e "$SCENARIO_FILE" ]; then
    echo "error: $SCENARIO_FILE already exists" >&2
    exit 1
fi
if [ -e "$SCRIPT_FILE" ]; then
    echo "error: $SCRIPT_FILE already exists" >&2
    exit 1
fi

# Write the scenario stub using a heredoc (single-quoted to suppress expansion).
cat > "$SCENARIO_FILE" <<EOF
// Package main: $NAME scenario (scaffolded by new-scenario.sh).
package main

import "context"

type $TYPE_NAME struct{}

func ($TYPE_NAME) Name() string          { return "$NAME" }
func ($TYPE_NAME) DefaultPreset() string { return "small" }

func ($TYPE_NAME) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
    return nil, fmt.Errorf("scenario $NAME: not yet implemented; see TODO in scenario_${NAME//-/_}.go")
}

func init() { RegisterScenario($TYPE_NAME{}) }
EOF

# Note: the generated file won't compile because of the missing fmt import.
# That's intentional — forces the implementer to immediately add imports +
# implement NewGenerator. Step 1 of the next-steps printed at the end.

# Copy quickstart.sh as the run script template.
if [ -f "$ROOT_DIR/tools/loadgen/scripts/quickstart.sh" ]; then
    cp "$ROOT_DIR/tools/loadgen/scripts/quickstart.sh" "$SCRIPT_FILE"
    sed -i.bak "s/messaging-pipeline/$NAME/g; s/quickstart/run-$NAME/g" "$SCRIPT_FILE"
    rm -f "$SCRIPT_FILE.bak"
else
    # Fallback: minimal template if quickstart.sh isn't present.
    cat > "$SCRIPT_FILE" <<EOF
#!/usr/bin/env bash
# run-$NAME.sh — operator script for the $NAME scenario.
set -euo pipefail
# TODO: customize via env vars and 'loadgen run --scenario=$NAME ...'
EOF
fi
chmod +x "$SCRIPT_FILE"

echo "Created:"
echo "  $SCENARIO_FILE"
echo "  $SCRIPT_FILE"
echo
echo "Next steps:"
echo "  1. Edit $SCENARIO_FILE: add 'fmt' import, implement NewGenerator."
echo "  2. Create scenario_${NAME//-/_}_test.go with at minimum a TestX_Registered test."
echo "  3. Append |$NAME to the --scenario help text in tools/loadgen/flags.go."
echo "  4. Regenerate tools/loadgen/testdata/refactor-baseline/run-help.golden:"
echo "       NATS_URL=x MONGO_URI=x go run ./tools/loadgen run --help > tools/loadgen/testdata/refactor-baseline/run-help.golden"
