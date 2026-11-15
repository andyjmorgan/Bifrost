#!/usr/bin/env bash
# Installs the Python and TypeScript reference servers' dependencies from
# their pinned files. Safe to run more than once.
#
#   pkg/hooks/harness/servers/setup.sh            # both
#   pkg/hooks/harness/servers/setup.sh python     # just one
#   pkg/hooks/harness/servers/setup.sh typescript
#
# Python: python/requirements.txt into python/.venv, using uv.
# TypeScript: npm ci in typescript/ (uses package-lock.json).
# The Go server needs nothing: the tests build it with the repo's go.mod.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
targets=("$@")
if [ ${#targets[@]} -eq 0 ]; then
  targets=(python typescript)
fi

setup_python() {
  local dir="$here/python"
  if [ ! -f "$dir/requirements.txt" ]; then
    echo "python: $dir/requirements.txt not found; skipping" >&2
    return 1
  fi
  if ! command -v uv >/dev/null 2>&1; then
    echo "python: uv not found on PATH; install it from https://docs.astral.sh/uv/" >&2
    return 1
  fi
  if [ ! -x "$dir/.venv/bin/python" ]; then
    local pyver=()
    if [ -f "$dir/.python-version" ]; then
      pyver=(--python "$(tr -d '[:space:]' < "$dir/.python-version")")
    fi
    uv venv "${pyver[@]}" "$dir/.venv"
  fi
  # Installing pinned versions into an existing venv is a no-op when they are
  # already there, so a rerun is safe.
  uv pip install --python "$dir/.venv/bin/python" -r "$dir/requirements.txt"
  echo "python: dependencies installed in $dir/.venv"
}

setup_typescript() {
  local dir="$here/typescript"
  if [ ! -f "$dir/package.json" ]; then
    echo "typescript: $dir/package.json not found; skipping" >&2
    return 1
  fi
  if [ ! -f "$dir/package-lock.json" ]; then
    echo "typescript: $dir/package-lock.json not found; npm ci needs a lockfile" >&2
    return 1
  fi
  if ! command -v npm >/dev/null 2>&1; then
    echo "typescript: npm not found on PATH; install Node.js" >&2
    return 1
  fi
  # npm ci deletes node_modules and installs exactly what the lockfile pins.
  (cd "$dir" && npm ci --no-audit --no-fund)
  echo "typescript: dependencies installed in $dir/node_modules"
}

status=0
for t in "${targets[@]}"; do
  case "$t" in
    python) setup_python || status=1 ;;
    typescript) setup_typescript || status=1 ;;
    *) echo "unknown target $t (want python or typescript)" >&2; status=1 ;;
  esac
done
exit $status
