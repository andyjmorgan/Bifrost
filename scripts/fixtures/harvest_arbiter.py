"""Harvest unique tool definitions from recent Arbiter sessions.

Usage (needs arbiterctl and Arbiter access; writes harness-tools.json):
  arbiterctl --ndjson sessions --from <RFC3339> --limit 200 > sessions.ndjson
  python3 harvest_arbiter.py
The output is personal data: keep it under internal/e2e/testdata/real/, which is gitignored.
"""
import json, subprocess, sys
def sh(*a):
    r = subprocess.run(["arbiterctl", *a], capture_output=True, text=True)
    return r.stdout
def find_tools(o, depth=0):
    if isinstance(o, dict):
        t = o.get("tools")
        if isinstance(t, list) and t and isinstance(t[0], dict) and ("name" in t[0] or "type" in t[0]):
            return t
        for v in o.values():
            r = find_tools(v, depth + 1)
            if r: return r
    elif isinstance(o, list):
        for v in o:
            r = find_tools(v, depth + 1)
            if r: return r
    elif isinstance(o, str) and depth < 5 and o[:1] == "{":
        try: return find_tools(json.loads(o), depth + 1)
        except Exception: return None
    return None
sessions = [json.loads(l)["session_id"] for l in open("sessions.ndjson")]
tools = {}
for i, sid in enumerate(sessions[:120]):
    evs = [json.loads(l) for l in sh("--ndjson", "events", "--session-id", sid, "--limit", "5").splitlines() if l.strip()]
    for e in evs:
        if e.get("Protocol") not in ("messages", "responses", "chat"): continue
        body = sh("event", e["CorrelationID"], "--view", "body")
        try: d = json.loads(body)
        except Exception: continue
        ts = find_tools(d) or []
        for t in ts:
            name = t.get("name") or (t.get("function") or {}).get("name")
            if not name: continue
            src = e.get("Provider")
            if name not in tools: tools[name] = {"source": src, "def": t}
        break
    print(i, sid[:8], len(tools), file=sys.stderr)
json.dump(tools, open("harness-tools.json", "w"), indent=1)
print(len(tools))
