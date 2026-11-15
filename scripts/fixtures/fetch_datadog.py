"""Fetch the Datadog MCP server catalog (tools, prompts, resources, templates).

Usage: DDH="$(dwvault credentials header datadog-pat)" python3 fetch_datadog.py all
The token is read from the DDH environment variable and never written. The
output is a third-party catalog: keep it under internal/e2e/testdata/real/,
which is gitignored.
"""
import json, os, sys, urllib.request
BASE = "https://mcp.datadoghq.eu/api/unstable/mcp-server/mcp"
H = os.environ["DDH"]  # "Authorization: Bearer ..."
hk, hv = H.split(": ", 1)
def post(url, body, sid=None):
    req = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST")
    req.add_header(hk, hv); req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    req.add_header("MCP-Protocol-Version", "2025-06-18")
    if sid: req.add_header("Mcp-Session-Id", sid)
    with urllib.request.urlopen(req, timeout=60) as r:
        sid2 = r.headers.get("mcp-session-id"); raw = r.read().decode()
    if not raw.strip(): return None, sid2
    if raw.lstrip().startswith("data:") or "\ndata:" in raw or raw.startswith("event:"):
        data = [l[5:].strip() for l in raw.splitlines() if l.startswith("data:")]
        raw = data[-1]
    return json.loads(raw), sid2
def run(toolsets):
    url = BASE + (f"?toolsets={toolsets}" if toolsets else "")
    res, sid = post(url, {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"tool-shape-fixtures","version":"0"}}})
    post(url, {"jsonrpc":"2.0","method":"notifications/initialized"}, sid)
    out = {"initialize": res.get("result"), "tools": [], "prompts": [], "resources": [], "resourceTemplates": []}
    for method, key in [("tools/list","tools"),("prompts/list","prompts"),("resources/list","resources"),("resources/templates/list","resourceTemplates")]:
        cursor, n = None, 2
        while True:
            params = {"cursor": cursor} if cursor else {}
            r, _ = post(url, {"jsonrpc":"2.0","id":n,"method":method,"params":params}, sid); n += 1
            if not r or "error" in r: out[key+"_error"] = (r or {}).get("error"); break
            out[key] += r["result"].get(key, [])
            cursor = r["result"].get("nextCursor")
            if not cursor: break
    return out
ts = sys.argv[1] if len(sys.argv) > 1 else ""
o = run(ts)
json.dump(o, open(f"datadog-{ts or 'default'}.json","w"), indent=1)
print(ts or "default", {k: len(v) for k, v in o.items() if isinstance(v, list)}, {k: v for k, v in o.items() if k.endswith("_error")})
