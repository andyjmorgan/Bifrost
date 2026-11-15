"""Self-test for the Python reference server.

Starts server.py with the contract's launch command, then exercises every tool,
prompt, resource, the template and the unlisted resource through the SDK's own
`Client`, once on 2026-07-28 (mode="auto": server/discover, then the per-request
envelope) and once on the handshake era (mode="legacy": initialize, stateless
HTTP). It finishes with raw HTTP requests to show the wire, then closes the
server's stdin and checks that it exits and printed nothing else on stdout.

Run from the repository root:
    pkg/hooks/harness/servers/python/.venv/bin/python pkg/hooks/harness/servers/python/selftest.py
"""

from __future__ import annotations

import asyncio
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

import mcp_types as types

from mcp import Client
from mcp.shared.exceptions import MCPError

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[4]
PNG_1X1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
INSTRUCTIONS = "Reference server.\n\n## Notes\nUse search first."

failures: list[str] = []
passes = 0


def check(label: str, cond: bool, detail: Any = None) -> None:
    global passes
    if cond:
        passes += 1
        print(f"  ok   {label}")
    else:
        failures.append(label)
        print(f"  FAIL {label}: {detail!r}")


def dump(model: Any) -> Any:
    return model.model_dump(mode="json", by_alias=True, exclude_none=True)


def texts(result: types.CallToolResult) -> list[str]:
    return [c.text for c in result.content if isinstance(c, types.TextContent)]


class Elicitor:
    """Elicitation callback that answers with whatever `next_action` says."""

    def __init__(self) -> None:
        self.next_action = "accept"
        self.seen: list[types.ElicitRequestParams] = []

    async def __call__(self, context: Any, params: types.ElicitRequestParams) -> types.ElicitResult:
        self.seen.append(params)
        if self.next_action == "accept":
            return types.ElicitResult(action="accept", content={"project": "apollo"})
        return types.ElicitResult(action=self.next_action)  # type: ignore[arg-type]


async def exercise(url: str, mode: str, expect_version: str) -> None:
    print(f"\n== SDK client, mode={mode}")
    elicitor = Elicitor()
    async with Client(url, mode=mode, elicitation_callback=elicitor) as client:
        check("negotiated version", client.protocol_version == expect_version, client.protocol_version)
        info = client.server_info
        check(
            "server identity",
            info is not None and info.name == "refserver-python" and info.version == "1.0.0",
            info,
        )
        check("instructions", client.instructions == INSTRUCTIONS, client.instructions)

        # ---- tools/list
        tools = (await client.list_tools()).tools
        by_name = {t.name: t for t in tools}
        expected = [
            "echo", "add", "search", "delete_page", "get_report", "slow_count",
            "fail", "crash", "picture", "ask", "inspect",
        ]
        check("tool names and order", [t.name for t in tools] == expected, [t.name for t in tools])
        check(
            "search schema",
            by_name["search"].input_schema
            == {
                "type": "object",
                "properties": {
                    "query": {"type": "string"},
                    "limit": {"type": "integer"},
                    "space": {"type": "string"},
                    "telemetry": {
                        "type": "object",
                        "properties": {"intent": {"type": "string"}},
                        "required": ["intent"],
                    },
                },
                "required": ["query", "telemetry"],
            },
            by_name["search"].input_schema,
        )
        check("delete_page destructive", dump(by_name["delete_page"].annotations) == {"destructiveHint": True})
        check("echo read-only", dump(by_name["echo"].annotations) == {"readOnlyHint": True})
        check("fail no annotations", by_name["fail"].annotations is None)

        # ---- tools/call
        r = await client.call_tool("echo", {"text": "hi"})
        check("echo", texts(r) == ["hi"] and r.structured_content == {"text": "hi"}, dump(r))
        r = await client.call_tool("add", {"a": 2, "b": 3})
        check("add 2+3", texts(r) == ["5"] and r.structured_content == {"sum": 5}, dump(r))
        r = await client.call_tool("add", {"a": 1, "b": 1.5})
        check("add 1+1.5", texts(r) == ["2.5"] and r.structured_content == {"sum": 2.5}, dump(r))
        args = {"query": "q", "limit": 3, "telemetry": {"intent": "find"}, "space": "ENG"}
        r = await client.call_tool("search", args)
        check(
            "search echoes args",
            r.structured_content == {"args": args}
            and texts(r) == ['{"query":"q","limit":3,"telemetry":{"intent":"find"},"space":"ENG"}'],
            dump(r),
        )
        r = await client.call_tool("search", {"query": "q"})
        check("search missing telemetry -> tool error", r.is_error is True, dump(r))
        r = await client.call_tool("delete_page", {"id": "p1"})
        check("delete_page", texts(r) == ["deleted p1"], dump(r))
        r = await client.call_tool("get_report", {})
        check(
            "get_report",
            texts(r) == ["Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7."]
            and r.structured_content
            == {
                "user": {"email": "ana@example.com", "name": "Ana"},
                "rows": [{"id": 1, "secret": "s1"}, {"id": 2, "secret": "s2"}],
                "note": None,
            },
            dump(r),
        )

        progress: list[tuple[float, float | None, str | None]] = []

        async def on_progress(p: float, total: float | None, message: str | None) -> None:
            progress.append((p, total, message))

        r = await client.call_tool("slow_count", {"steps": 3, "delayMs": 10}, progress_callback=on_progress)
        check("slow_count result", texts(r) == ["counted 3"], dump(r))
        check(
            "slow_count progress",
            progress == [(i, 3, f"step {i} of 3: reading db-internal-7") for i in (1, 2, 3)],
            progress,
        )
        r = await client.call_tool("slow_count", {"steps": 2})
        check("slow_count without token", texts(r) == ["counted 2"], dump(r))

        r = await client.call_tool("fail", {})
        check("fail -> isError boom", r.is_error is True and texts(r) == ["boom"], dump(r))
        try:
            r = await client.call_tool("crash", {})
            check("crash -> JSON-RPC error", False, dump(r))
        except MCPError as e:
            check("crash -> JSON-RPC error", True)
            print(f"       (crash error: code={e.error.code} message={e.error.message!r})")

        r = await client.call_tool("picture", {})
        c = r.content
        check(
            "picture blocks",
            len(c) == 3
            and isinstance(c[0], types.TextContent) and c[0].text == "a picture"
            and isinstance(c[1], types.ImageContent) and c[1].data == PNG_1X1 and c[1].mime_type == "image/png"
            and isinstance(c[2], types.ResourceLink) and c[2].uri == "ref://docs/guide" and c[2].name == "guide",
            dump(r),
        )

        if mode == "legacy":
            # Stateless HTTP on the handshake era has no back channel for
            # server-to-client requests, so the SDK cannot elicit here.
            try:
                r = await client.call_tool("ask", {})
                check("ask (legacy stateless) fails: no back channel", False, dump(r))
            except MCPError as e:
                check("ask (legacy stateless) fails: no back channel", True)
                print(f"       (ask error: code={e.error.code} message={e.error.message!r})")
        else:
            for action, want in (("accept", "project=apollo"), ("decline", "declined"), ("cancel", "cancelled")):
                elicitor.next_action = action
                r = await client.call_tool("ask", {})
                check(f"ask {action}", texts(r) == [want], dump(r))
            p = elicitor.seen[0]
            check(
                "ask elicitation form",
                isinstance(p, types.ElicitRequestFormParams)
                and p.message == "Which project?"
                and p.requested_schema
                == {"type": "object", "properties": {"project": {"type": "string"}}, "required": ["project"]},
                p,
            )

        r = await client.call_tool("inspect", {}, meta={"progressToken": "tok-1", "x-test": "yes"})
        s = r.structured_content or {}
        print(f"       (inspect: {json.dumps(s, sort_keys=True)})")
        check("inspect text", texts(r) == ["inspected"])
        check("inspect protocolVersion", s.get("protocolVersion") == expect_version, s.get("protocolVersion"))
        hdrs = s.get("headers") or {}
        check(
            "inspect headers",
            "accept" in hdrs and "content-type" in hdrs and "mcp-protocol-version" in hdrs
            and "authorization" not in hdrs and "cookie" not in hdrs,
            hdrs,
        )
        meta = s.get("meta") or {}
        check("inspect meta", meta.get("x-test") == "yes" and meta.get("progressToken") == "tok-1", meta)
        if mode != "legacy":
            check("inspect clientInfo", isinstance(s.get("clientInfo"), dict), s.get("clientInfo"))
            check(
                "inspect capabilities declare elicitation",
                isinstance(s.get("capabilities"), dict) and "elicitation" in s["capabilities"],
                s.get("capabilities"),
            )

        try:
            await client.call_tool("nope", {})
            check("unknown tool -> JSON-RPC error", False)
        except MCPError:
            check("unknown tool -> JSON-RPC error", True)

        # ---- prompts
        prompts = (await client.list_prompts()).prompts
        check(
            "prompt list",
            [dump(p) for p in prompts]
            == [
                {"name": "greet", "arguments": [{"name": "who", "required": True}]},
                {"name": "summarize", "arguments": [{"name": "topic", "required": True}, {"name": "tone"}]},
            ],
            [dump(p) for p in prompts],
        )
        g = await client.get_prompt("greet", {"who": "Ana"})
        check(
            "greet",
            len(g.messages) == 1 and g.messages[0].role == "user"
            and isinstance(g.messages[0].content, types.TextContent)
            and g.messages[0].content.text == "Hello, Ana!",
            dump(g),
        )
        g = await client.get_prompt("summarize", {"topic": "Q3"})
        check("summarize default tone", g.messages[0].content.text == "Summarize Q3 in a neutral tone.", dump(g))  # type: ignore[union-attr]
        g = await client.get_prompt("summarize", {"topic": "Q3", "tone": "formal"})
        check("summarize tone", g.messages[0].content.text == "Summarize Q3 in a formal tone.", dump(g))  # type: ignore[union-attr]

        # ---- resources
        res = (await client.list_resources()).resources
        check(
            "resource list",
            [(x.uri, x.name, x.mime_type) for x in res]
            == [
                ("ref://docs/guide", "guide", "text/markdown"),
                ("ref://docs/faq", "faq", "text/plain"),
                ("ref://secrets/key", "key", "text/plain"),
            ],
            [dump(x) for x in res],
        )
        tmpl = (await client.list_resource_templates()).resource_templates
        check(
            "template list",
            [dump(t) for t in tmpl] == [{"uriTemplate": "ref://notes/{id}", "name": "note", "mimeType": "text/plain"}],
            [dump(t) for t in tmpl],
        )
        for uri, want, mime in (
            ("ref://docs/guide", "# Guide\nSee ref://docs/faq and https://example.com/x.", "text/markdown"),
            ("ref://docs/faq", "FAQ", "text/plain"),
            ("ref://secrets/key", "sk-test-123", "text/plain"),
            ("ref://notes/42", "note 42", "text/plain"),
            ("ref://hidden/thing", "hidden thing", "text/plain"),
        ):
            rr = await client.read_resource(uri)
            c0 = rr.contents[0]
            check(
                f"read {uri}",
                len(rr.contents) == 1 and isinstance(c0, types.TextResourceContents)
                and c0.text == want and c0.uri == uri and c0.mime_type == mime,
                dump(rr),
            )
        try:
            await client.read_resource("ref://docs/missing")
            check("read unknown -> not found error", False)
        except MCPError as e:
            check("read unknown -> not found error", e.error.code == types.INVALID_PARAMS, e.error)


def raw_post(url: str, body: dict[str, Any], headers: dict[str, str]) -> tuple[int, dict[str, str], str]:
    data = json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method="POST")
    for k, v in headers.items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, dict(resp.headers.items()), resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers.items()), e.read().decode()


def raw_wire(url: str) -> None:
    print("\n== raw HTTP")
    modern_meta = {
        "io.modelcontextprotocol/protocolVersion": "2026-07-28",
        "io.modelcontextprotocol/clientCapabilities": {},
        "io.modelcontextprotocol/clientInfo": {"name": "raw", "version": "0"},
    }
    base = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    modern = {**base, "MCP-Protocol-Version": "2026-07-28"}

    cases = [
        ("2026-07-28 server/discover", modern | {"Mcp-Method": "server/discover"},
         {"jsonrpc": "2.0", "id": 1, "method": "server/discover", "params": {"_meta": modern_meta}}),
        ("2026-07-28 tools/call echo", modern | {"Mcp-Method": "tools/call", "Mcp-Name": "echo"},
         {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
          "params": {"name": "echo", "arguments": {"text": "hi"}, "_meta": modern_meta}}),
        ("2026-07-28 tools/call slow_count with progress token", modern | {"Mcp-Method": "tools/call", "Mcp-Name": "slow_count"},
         {"jsonrpc": "2.0", "id": 3, "method": "tools/call",
          "params": {"name": "slow_count", "arguments": {"steps": 2, "delayMs": 5},
                     "_meta": {**modern_meta, "progressToken": "p1"}}}),
        ("2026-07-28 tools/call ask (first round)", modern | {"Mcp-Method": "tools/call", "Mcp-Name": "ask"},
         {"jsonrpc": "2.0", "id": 4, "method": "tools/call",
          "params": {"name": "ask", "arguments": {}, "_meta": modern_meta}}),
        ("2025-11-25 initialize (stateless)", base,
         {"jsonrpc": "2.0", "id": 5, "method": "initialize",
          "params": {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "raw", "version": "0"}}}),
    ]
    for label, headers, body in cases:
        status, resp_headers, text = raw_post(url, body, headers)
        keep = {k.lower(): v for k, v in resp_headers.items() if k.lower() in ("content-type", "mcp-session-id", "cache-control")}
        print(f"-- {label}\n   HTTP {status} {keep}\n   {text.strip()[:600]}")
        check(f"raw {label} -> 200", status == 200, status)
        if label.startswith("2025") or label.startswith("2026"):
            check(f"raw {label} has no Mcp-Session-Id", "mcp-session-id" not in keep, keep)


async def main() -> int:
    cmd = [
        "pkg/hooks/harness/servers/python/.venv/bin/python",
        "pkg/hooks/harness/servers/python/server.py",
        "--port",
        "0",
    ]
    proc = subprocess.Popen(
        cmd, cwd=REPO_ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL
    )
    assert proc.stdout is not None and proc.stdin is not None
    line = proc.stdout.readline().decode()
    print(f"startup: {line.strip()}")
    startup = json.loads(line)
    check("startup url on 127.0.0.1 /mcp", startup["url"].startswith("http://127.0.0.1:") and startup["url"].endswith("/mcp"))
    check("startup language/sdk", startup["language"] == "python" and startup["sdk"] == "mcp")
    check("startup protocolVersions newest first", startup["protocolVersions"][0] == "2026-07-28", startup)
    url = startup["url"]
    try:
        await exercise(url, "auto", "2026-07-28")
        await exercise(url, "legacy", "2025-11-25")
        raw_wire(url)
    finally:
        proc.stdin.close()
        try:
            rc = proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            rc = None
        rest = proc.stdout.read().decode()
    print("\n== shutdown")
    check("exits when stdin closes", rc == 0, rc)
    check("nothing else on stdout", rest == "", rest)

    print(f"\n{passes} passed, {len(failures)} failed")
    for f in failures:
        print(f"  failed: {f}")
    return 1 if failures else 0


if __name__ == "__main__":
    os.chdir(REPO_ROOT)
    sys.exit(asyncio.run(main()))
