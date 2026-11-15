"""Line-protocol driver for the official Python MCP client SDK (the `mcp` package).

The Go harness starts this as a subprocess. It reads one JSON object per line
on stdin and writes one JSON object per line on stdout. Everything else,
including the SDK's own logging, goes to stderr.

Requests:
    {"id": 1, "op": "connect", "url": "...", "token": "...", "capabilities": {"elicitation": {}}}
    {"id": 2, "op": "call", "method": "tools/call", "params": {...}, "progress": true,
     "elicit": [{"action": "accept", "content": {"confirm": true}}], "cancelAfterMs": 200}
    {"id": 3, "op": "close"}

Every reply carries the request's id and an "sdk" object. A call's reply has
"result" or "error", plus the "progress" events and "elicitations" the SDK
delivered while the call ran.
"""

from __future__ import annotations

import asyncio
import importlib.metadata
import json
import sys
from typing import Any

import anyio
import httpx2
from mcp import Client, types
from mcp.client.streamable_http import streamable_http_client
from mcp.shared.exceptions import MCPError
from pydantic import ValidationError

SDK_VERSION = importlib.metadata.version("mcp")


def describe(e: BaseException) -> dict[str, Any]:
    """An exception as an error object. anyio wraps failures in exception
    groups; report the exceptions inside, which say what went wrong."""
    leaves: list[BaseException] = []

    def walk(x: BaseException) -> None:
        if isinstance(x, BaseExceptionGroup):
            for sub in x.exceptions:
                walk(sub)
        else:
            leaves.append(x)

    walk(e)
    first = leaves[0] if leaves else e
    if isinstance(first, MCPError):
        return {"kind": "rpc", "code": first.code, "message": first.message, "data": first.data}
    if isinstance(first, ValidationError):
        return {"kind": "validation", "type": type(first).__name__, "message": str(first)[:2000]}
    message = "; ".join(f"{type(x).__name__}: {x}" for x in leaves) or str(e)
    return {"kind": "other", "type": type(first).__name__, "message": message[:2000]}


def dump(model: Any) -> Any:
    """A pydantic model as plain JSON, in wire spelling."""
    if hasattr(model, "model_dump"):
        return model.model_dump(mode="json", by_alias=True, exclude_none=True)
    return model


class Driver:
    def __init__(self) -> None:
        self.client: Client | None = None
        self.http: httpx2.AsyncClient | None = None
        # The call in flight. One at a time: the harness sends the next
        # request only after the previous reply.
        self.answers: list[dict[str, Any]] = []
        self.progress: list[dict[str, Any]] = []
        self.elicitations: list[dict[str, Any]] = []

    def sdk(self) -> dict[str, Any]:
        out: dict[str, Any] = {"name": "mcp", "version": SDK_VERSION, "language": "python"}
        if self.client is not None:
            try:
                out["protocolVersion"] = self.client.protocol_version
            except Exception:  # not connected
                pass
        return out

    async def on_elicit(self, context: Any, params: Any) -> Any:
        self.elicitations.append(dump(params))
        if not self.answers:
            # Nothing scripted: say so through the protocol, never by guessing.
            return types.ErrorData(code=-32603, message="driver: no scripted elicitation answer left")
        answer = self.answers.pop(0)
        return types.ElicitResult(action=answer.get("action", "decline"), content=answer.get("content"))

    async def on_progress(self, progress: float, total: float | None, message: str | None) -> None:
        event: dict[str, Any] = {"progress": progress}
        if total is not None:
            event["total"] = total
        if message is not None:
            event["message"] = message
        self.progress.append(event)

    async def connect(self, req: dict[str, Any]) -> dict[str, Any]:
        await self.close()  # a second connect replaces the first
        headers = {}
        if req.get("token"):
            headers["Authorization"] = "Bearer " + req["token"]
        self.http = httpx2.AsyncClient(
            headers=headers,
            timeout=httpx2.Timeout(30.0, read=float(req.get("readTimeoutSeconds", 20))),
        )
        caps = req.get("capabilities") or {}
        kwargs: dict[str, Any] = {
            "mode": req.get("mode", "auto"),
            "read_timeout_seconds": float(req.get("readTimeoutSeconds", 20)),
            "client_info": types.Implementation(name="harness-python-client", version="1.0.0"),
            "elicitation_callback": self.on_elicit if "elicitation" in caps else None,
        }
        if req.get("cache") is False:
            kwargs["cache"] = None
        self.client = Client(streamable_http_client(req["url"], http_client=self.http), **kwargs)
        await self.client.__aenter__()
        return {}

    async def call(self, req: dict[str, Any]) -> dict[str, Any]:
        if self.client is None:
            return {"error": {"kind": "driver", "message": "not connected"}}
        self.answers = list(req.get("elicit") or [])
        self.progress, self.elicitations = [], []
        out: dict[str, Any] = {}
        cancel_ms = req.get("cancelAfterMs")
        scope = anyio.move_on_after(cancel_ms / 1000) if cancel_ms else anyio.CancelScope()
        try:
            with scope:
                out["result"] = dump(await self.dispatch(req["method"], req.get("params") or {}, req))
            if scope.cancelled_caught:
                out["error"] = {"kind": "cancelled", "message": "the driver cancelled the call"}
        except Exception as e:  # JSON-RPC errors, rejected replies, transport failures
            out["error"] = describe(e)
        out["progress"], out["elicitations"] = self.progress, self.elicitations
        return out

    async def dispatch(self, method: str, p: dict[str, Any], req: dict[str, Any]) -> Any:
        c = self.client
        assert c is not None
        cache: dict[str, Any] = {"cache_mode": req["cacheMode"]} if req.get("cacheMode") else {}
        if method == "tools/list":
            return await c.list_tools(cursor=p.get("cursor"), **cache)
        if method == "prompts/list":
            return await c.list_prompts(cursor=p.get("cursor"), **cache)
        if method == "resources/list":
            return await c.list_resources(cursor=p.get("cursor"), **cache)
        if method == "resources/templates/list":
            return await c.list_resource_templates(cursor=p.get("cursor"), **cache)
        if method == "tools/call":
            return await c.call_tool(
                p["name"], p.get("arguments"), progress_callback=self.on_progress if req.get("progress") else None
            )
        if method == "prompts/get":
            return await c.get_prompt(p["name"], p.get("arguments"))
        if method == "resources/read":
            return await c.read_resource(p["uri"], **cache)
        raise ValueError(f"driver: unsupported method {method}")

    async def close(self) -> None:
        if self.client is not None:
            client, self.client = self.client, None
            try:
                await client.__aexit__(None, None, None)
            except Exception as e:
                print(f"driver: close: {type(e).__name__}: {e}", file=sys.stderr)
        if self.http is not None:
            http, self.http = self.http, None
            await http.aclose()


async def main() -> None:
    d = Driver()
    loop = asyncio.get_running_loop()
    while True:
        line = await loop.run_in_executor(None, sys.stdin.readline)
        if not line:
            break
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError as e:
            print(f"driver: bad request line: {e}", file=sys.stderr)
            continue
        reply: dict[str, Any] = {"id": req.get("id")}
        op = req.get("op")
        try:
            if op == "connect":
                reply.update(await d.connect(req))
            elif op == "call":
                reply.update(await d.call(req))
            elif op == "close":
                await d.close()
            else:
                reply["error"] = {"kind": "driver", "message": f"unknown op {op}"}
        except BaseException as e:  # a failed connect must still answer
            if isinstance(e, (KeyboardInterrupt, SystemExit)):
                raise
            reply["error"] = describe(e)
        reply["sdk"] = d.sdk()
        sys.stdout.write(json.dumps(reply, separators=(",", ":")) + "\n")
        sys.stdout.flush()
        if op == "close":
            break
    await d.close()


if __name__ == "__main__":
    anyio.run(main)
