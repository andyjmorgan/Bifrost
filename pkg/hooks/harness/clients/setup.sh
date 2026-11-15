#!/usr/bin/env bash
# Installs the official Python and TypeScript MCP client SDKs that the client
# adherence battery drives, at the versions pinned in this folder.
#
#   python/requirements.txt       the Python SDK (the `mcp` package)
#   typescript/package-lock.json  the TypeScript SDKs: @modelcontextprotocol/client
#                                 (current) and @modelcontextprotocol/sdk (1.x)
#
# Needs `uv` (or python3 with venv) and `npm`. The only network use is PyPI
# and the npm registry. Run it again after changing a pinned version.
set -euo pipefail
cd "$(dirname "$0")"

echo "Python SDK"
if command -v uv >/dev/null 2>&1; then
  uv venv python/.venv -q --allow-existing
  uv pip install -q --python python/.venv/bin/python -r python/requirements.txt
else
  python3 -m venv python/.venv
  python/.venv/bin/python -m pip install -q -r python/requirements.txt
fi
python/.venv/bin/python -c 'import importlib.metadata as m; print("  mcp", m.version("mcp"))'

echo "TypeScript SDKs"
(cd typescript && npm ci --no-audit --no-fund --silent)
node -e '
const r = require("module").createRequire(process.cwd() + "/typescript/");
for (const [name, entry] of [["@modelcontextprotocol/client", "@modelcontextprotocol/client"], ["@modelcontextprotocol/sdk", "@modelcontextprotocol/sdk/client/index.js"]]) {
  const p = r.resolve(entry);
  console.log("  " + name, r(p.slice(0, p.lastIndexOf(name) + name.length) + "/package.json").version);
}'
echo "Done. Run: go test ./pkg/hooks/harness/clients/ -count=1 -v"
