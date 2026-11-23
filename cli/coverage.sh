#!/usr/bin/env bash
# Coverage gate for the CLI module, carried over from the Vault repository.
#
# Computes statement coverage over hand-written code and fails if it is below the
# threshold. Conventionally-untestable code is excluded from the denominator:
#   - the generated API client (oapi-codegen output: *.gen.go)
#   - the command-dispatch files (main.go, auth.go, update.go, skill.go)
#   - blocks tagged //coverage:ignore in source (see covfilter.py)
#
# Usage: ./coverage.sh        (COVERAGE_THRESHOLD overrides the default 95.0)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

THRESHOLD="${COVERAGE_THRESHOLD:-95.0}"
EXCLUDE_RE='\.gen\.go|/cli/main\.go|/cli/auth\.go|/cli/update\.go|/cli/skill\.go'

prof="$(mktemp)"
filtered="$(mktemp)"
trap 'rm -f "$prof" "$filtered"' EXIT

echo "==> cli: test + coverage"
go test ./... -coverpkg=./... -coverprofile="$prof"
python3 covfilter.py "$prof" "$(go list -m)" "$(pwd)" "$EXCLUDE_RE" >"$filtered"
pct="$(go tool cover -func="$filtered" | awk '/^total:/ {print $3}')"
echo "    cli: ${pct} (excluding generated + entrypoints; threshold ${THRESHOLD}%)"
awk -v p="${pct%\%}" -v t="$THRESHOLD" 'BEGIN{exit !(p+0 >= t+0)}' || {
  echo "    FAIL: cli below ${THRESHOLD}%"
  exit 1
}
