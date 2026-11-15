"""Reference MCP server (Python) for the rules-engine end-to-end harness.

Built on the official MCP Python SDK's low-level `Server`, served over the
SDK's Streamable HTTP session manager in stateless mode. The contract is
pkg/hooks/harness/servers/SPEC.md.

The low-level server is used (rather than `MCPServer`) because the contract
fixes input schemas byte for byte, wants `search` to echo its arguments exactly
as received, and has a resource that is readable but not listed; `MCPServer`
derives schemas from Python signatures, coerces arguments through pydantic and
only serves listed resources or templates.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import signal
import socket
import sys
import threading
from importlib.metadata import version as dist_version
from typing import Any

import anyio
import mcp_types as types
import uvicorn
from jsonschema import Draft202012Validator
from mcp_types.version import HANDSHAKE_PROTOCOL_VERSIONS, KNOWN_PROTOCOL_VERSIONS, MODERN_PROTOCOL_VERSIONS

from mcp.server.context import ServerRequestContext
from mcp.server.lowlevel import Server
from mcp.shared.exceptions import MCPError
from mcp.shared.uri_template import UriTemplate

LANGUAGE = "python"
SDK_DIST = "mcp"
SERVER_NAME = f"refserver-{LANGUAGE}"
SERVER_VERSION = "1.0.0"
INSTRUCTIONS = "Reference server.\n\n## Notes\nUse search first."

PNG_1X1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

REPORT_TEXT = "Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7."
REPORT_STRUCTURED: dict[str, Any] = {
    "user": {"email": "ana@example.com", "name": "Ana"},
    "rows": [{"id": 1, "secret": "s1"}, {"id": 2, "secret": "s2"}],
    "note": None,
}

ASK_MESSAGE = "Which project?"
ASK_SCHEMA: dict[str, Any] = {
    "type": "object",
    "properties": {"project": {"type": "string"}},
    "required": ["project"],
}
ASK_INPUT_KEY = "project"

# Headers never echoed back by `inspect`.
REDACTED_HEADERS = frozenset({"authorization", "cookie"})

log = logging.getLogger(SERVER_NAME)

READ_ONLY = types.ToolAnnotations(read_only_hint=True)
DESTRUCTIVE = types.ToolAnnotations(destructive_hint=True)
EMPTY_OBJECT: dict[str, Any] = {"type": "object", "properties": {}}


def _obj(properties: dict[str, Any], required: list[str]) -> dict[str, Any]:
    return {"type": "object", "properties": properties, "required": required}


TOOLS: list[types.Tool] = [
    types.Tool(
        name="echo",
        description="Return the text.",
        input_schema=_obj({"text": {"type": "string"}}, ["text"]),
        annotations=READ_ONLY,
    ),
    types.Tool(
        name="add",
        description="Add two numbers.",
        input_schema=_obj({"a": {"type": "number"}, "b": {"type": "number"}}, ["a", "b"]),
        annotations=READ_ONLY,
    ),
    types.Tool(
        name="search",
        description="Search pages.",
        input_schema=_obj(
            {
                "query": {"type": "string"},
                "limit": {"type": "integer"},
                "space": {"type": "string"},
                "telemetry": _obj({"intent": {"type": "string"}}, ["intent"]),
            },
            ["query", "telemetry"],
        ),
        annotations=READ_ONLY,
    ),
    types.Tool(
        name="delete_page",
        description="Delete a page.",
        input_schema=_obj({"id": {"type": "string"}}, ["id"]),
        annotations=DESTRUCTIVE,
    ),
    types.Tool(
        name="get_report",
        description="Return the report.",
        input_schema=EMPTY_OBJECT,
        annotations=READ_ONLY,
    ),
    types.Tool(
        name="slow_count",
        description="Count slowly, reporting progress.",
        input_schema=_obj({"steps": {"type": "integer"}, "delayMs": {"type": "integer", "default": 20}}, ["steps"]),
        annotations=READ_ONLY,
    ),
    types.Tool(name="fail", description="Always fails.", input_schema=EMPTY_OBJECT),
    types.Tool(name="crash", description="Always crashes.", input_schema=EMPTY_OBJECT),
    types.Tool(
        name="picture",
        description="Return a picture.",
        input_schema=EMPTY_OBJECT,
        annotations=READ_ONLY,
    ),
    types.Tool(name="ask", description="Ask which project.", input_schema=EMPTY_OBJECT),
    types.Tool(
        name="inspect",
        description="Describe the request.",
        input_schema=EMPTY_OBJECT,
        annotations=READ_ONLY,
    ),
]
TOOLS_BY_NAME = {t.name: t for t in TOOLS}
VALIDATORS = {t.name: Draft202012Validator(t.input_schema) for t in TOOLS}

PROMPTS: list[types.Prompt] = [
    types.Prompt(name="greet", arguments=[types.PromptArgument(name="who", required=True)]),
    types.Prompt(
        name="summarize",
        arguments=[types.PromptArgument(name="topic", required=True), types.PromptArgument(name="tone")],
    ),
]
PROMPTS_BY_NAME = {p.name: p for p in PROMPTS}

RESOURCES: list[tuple[types.Resource, str]] = [
    (
        types.Resource(uri="ref://docs/guide", name="guide", mime_type="text/markdown"),
        "# Guide\nSee ref://docs/faq and https://example.com/x.",
    ),
    (types.Resource(uri="ref://docs/faq", name="faq", mime_type="text/plain"), "FAQ"),
    (types.Resource(uri="ref://secrets/key", name="key", mime_type="text/plain"), "sk-test-123"),
]
RESOURCES_BY_URI = {r.uri: (r, text) for r, text in RESOURCES}

NOTE_TEMPLATE = types.ResourceTemplate(uri_template="ref://notes/{id}", name="note", mime_type="text/plain")
NOTE_MATCHER = UriTemplate.parse(NOTE_TEMPLATE.uri_template)

HIDDEN_URI = "ref://hidden/thing"
HIDDEN_TEXT = "hidden thing"


# ---------------------------------------------------------------- helpers


def _text(text: str) -> types.TextContent:
    return types.TextContent(type="text", text=text)


def _format_number(value: int | float) -> tuple[int | float, str]:
    """A number as JSON value and decimal string: 5 not 5.0, 2.5 as 2.5."""
    if isinstance(value, float) and value.is_integer() and abs(value) < 2**53:
        value = int(value)
    return value, (str(value) if isinstance(value, int) else repr(value))


def _dump(model: Any) -> Any:
    if model is None:
        return None
    return model.model_dump(mode="json", by_alias=True, exclude_none=True)


def _raw_params(ctx: ServerRequestContext[Any]) -> dict[str, Any]:
    """The request params as they arrived on the wire (before model validation)."""
    return dict(ctx.params) if ctx.params is not None else {}


def _request_headers(ctx: ServerRequestContext[Any]) -> dict[str, str]:
    """HTTP headers of the request, lower-cased, minus credentials. Empty off HTTP."""
    request = ctx.request
    headers = getattr(request, "headers", None)
    if headers is None:
        return {}
    out: dict[str, str] = {}
    for name, value in headers.items():
        key = name.lower()
        if key in REDACTED_HEADERS:
            continue
        out[key] = value
    return out


# ---------------------------------------------------------------- tools


async def list_tools(ctx: ServerRequestContext[Any], params: types.PaginatedRequestParams | None) -> types.ListToolsResult:
    return types.ListToolsResult(tools=TOOLS)


async def call_tool(
    ctx: ServerRequestContext[Any], params: types.CallToolRequestParams
) -> types.CallToolResult | types.InputRequiredResult:
    name = params.name
    if name not in TOOLS_BY_NAME:
        raise MCPError(code=types.INVALID_PARAMS, message=f"Unknown tool: {name}")
    raw_args = _raw_params(ctx).get("arguments")
    args: dict[str, Any] = raw_args if isinstance(raw_args, dict) else {}
    error = next(iter(VALIDATORS[name].iter_errors(args)), None)
    if error is not None:
        return types.CallToolResult(content=[_text(f"Invalid arguments for tool {name}: {error.message}")], is_error=True)

    match name:
        case "echo":
            text = args["text"]
            return types.CallToolResult(content=[_text(text)], structured_content={"text": text})
        case "add":
            total, rendered = _format_number(args["a"] + args["b"])
            return types.CallToolResult(content=[_text(rendered)], structured_content={"sum": total})
        case "search":
            compact = json.dumps(args, separators=(",", ":"), ensure_ascii=False)
            return types.CallToolResult(content=[_text(compact)], structured_content={"args": args})
        case "delete_page":
            return types.CallToolResult(content=[_text(f"deleted {args['id']}")])
        case "get_report":
            return types.CallToolResult(content=[_text(REPORT_TEXT)], structured_content=REPORT_STRUCTURED)
        case "slow_count":
            return await _slow_count(ctx, args)
        case "fail":
            return types.CallToolResult(content=[_text("boom")], is_error=True)
        case "crash":
            # A plain exception: the SDK turns it into a JSON-RPC error response.
            raise RuntimeError("boom")
        case "picture":
            return types.CallToolResult(
                content=[
                    _text("a picture"),
                    types.ImageContent(type="image", data=PNG_1X1, mime_type="image/png"),
                    types.ResourceLink(type="resource_link", uri="ref://docs/guide", name="guide"),
                ]
            )
        case "ask":
            return await _ask(ctx, params)
        case "inspect":
            return _inspect(ctx)
    raise AssertionError(name)  # pragma: no cover


async def _slow_count(ctx: ServerRequestContext[Any], args: dict[str, Any]) -> types.CallToolResult:
    steps: int = args["steps"]
    delay_ms: int = args.get("delayMs", 20)
    # `report_progress` sends notifications/progress under the request's own
    # progress token, and is a no-op when the request carried none.
    for i in range(1, steps + 1):
        await anyio.sleep(max(delay_ms, 0) / 1000)
        await ctx.session.report_progress(i, steps, f"step {i} of {steps}: reading db-internal-7")
    return types.CallToolResult(content=[_text(f"counted {steps}")])


def _elicit_outcome(result: types.ElicitResult) -> types.CallToolResult:
    if result.action == "accept":
        value = (result.content or {}).get("project")
        return types.CallToolResult(content=[_text(f"project={value}")])
    if result.action == "decline":
        return types.CallToolResult(content=[_text("declined")])
    return types.CallToolResult(content=[_text("cancelled")])


async def _ask(
    ctx: ServerRequestContext[Any], params: types.CallToolRequestParams
) -> types.CallToolResult | types.InputRequiredResult:
    if ctx.protocol_version in MODERN_PROTOCOL_VERSIONS:
        # 2026-07-28: no server-to-client requests. The elicitation rides an
        # `input_required` result and the client retries with the answer.
        response = (params.input_responses or {}).get(ASK_INPUT_KEY)
        if isinstance(response, types.ElicitResult):
            return _elicit_outcome(response)
        return types.InputRequiredResult(
            input_requests={
                ASK_INPUT_KEY: types.ElicitRequest(
                    params=types.ElicitRequestFormParams(message=ASK_MESSAGE, requested_schema=ASK_SCHEMA)
                )
            }
        )
    # Handshake era: a server-to-client elicitation/create request on this
    # request's stream. In stateless HTTP mode there is no back channel and the
    # SDK raises NoBackChannelError, which reaches the client as a JSON-RPC error.
    result = await ctx.session.elicit_form(ASK_MESSAGE, ASK_SCHEMA, related_request_id=ctx.request_id)
    return _elicit_outcome(result)


def _inspect(ctx: ServerRequestContext[Any]) -> types.CallToolResult:
    client_params = ctx.session.client_params
    meta = _raw_params(ctx).get("_meta")
    described = {
        "protocolVersion": ctx.protocol_version,
        "clientInfo": _dump(client_params.client_info) if client_params is not None else None,
        "capabilities": _dump(ctx.session.client_capabilities),
        "meta": meta if isinstance(meta, dict) else None,
        "headers": _request_headers(ctx),
    }
    return types.CallToolResult(content=[_text("inspected")], structured_content=described)


# ---------------------------------------------------------------- prompts


async def list_prompts(
    ctx: ServerRequestContext[Any], params: types.PaginatedRequestParams | None
) -> types.ListPromptsResult:
    return types.ListPromptsResult(prompts=PROMPTS)


async def get_prompt(
    ctx: ServerRequestContext[Any], params: types.GetPromptRequestParams
) -> types.GetPromptResult:
    prompt = PROMPTS_BY_NAME.get(params.name)
    if prompt is None:
        raise MCPError(code=types.INVALID_PARAMS, message=f"Unknown prompt: {params.name}")
    arguments = params.arguments or {}
    missing = [a.name for a in prompt.arguments or [] if a.required and a.name not in arguments]
    if missing:
        raise MCPError(
            code=types.INVALID_PARAMS, message=f"Missing required arguments for prompt {params.name}: {missing}"
        )
    if prompt.name == "greet":
        text = f"Hello, {arguments['who']}!"
    else:
        text = f"Summarize {arguments['topic']} in a {arguments.get('tone') or 'neutral'} tone."
    return types.GetPromptResult(messages=[types.PromptMessage(role="user", content=_text(text))])


# ---------------------------------------------------------------- resources


async def list_resources(
    ctx: ServerRequestContext[Any], params: types.PaginatedRequestParams | None
) -> types.ListResourcesResult:
    return types.ListResourcesResult(resources=[r for r, _ in RESOURCES])


async def list_resource_templates(
    ctx: ServerRequestContext[Any], params: types.PaginatedRequestParams | None
) -> types.ListResourceTemplatesResult:
    return types.ListResourceTemplatesResult(resource_templates=[NOTE_TEMPLATE])


async def read_resource(
    ctx: ServerRequestContext[Any], params: types.ReadResourceRequestParams
) -> types.ReadResourceResult:
    uri = params.uri

    def contents(mime_type: str | None, text: str) -> types.ReadResourceResult:
        return types.ReadResourceResult(contents=[types.TextResourceContents(uri=uri, mime_type=mime_type, text=text)])

    if uri in RESOURCES_BY_URI:
        resource, text = RESOURCES_BY_URI[uri]
        return contents(resource.mime_type, text)
    if uri == HIDDEN_URI:
        return contents("text/plain", HIDDEN_TEXT)
    match = NOTE_MATCHER.match(uri)
    if match is not None and isinstance(match.get("id"), str):
        return contents(NOTE_TEMPLATE.mime_type, f"note {match['id']}")
    # Same code, message and data as the SDK's own MCPServer resource-not-found
    # path (ResourceNotFoundError -> INVALID_PARAMS, SEP-2164).
    raise MCPError(code=types.INVALID_PARAMS, message=f"Unknown resource: {uri}", data={"uri": uri})


# ---------------------------------------------------------------- process


def build_server() -> Server[Any]:
    return Server(
        SERVER_NAME,
        version=SERVER_VERSION,
        instructions=INSTRUCTIONS,
        on_list_tools=list_tools,
        on_call_tool=call_tool,
        on_list_prompts=list_prompts,
        on_get_prompt=get_prompt,
        on_list_resources=list_resources,
        on_list_resource_templates=list_resource_templates,
        on_read_resource=read_resource,
    )


def served_protocol_versions() -> list[str]:
    """Protocol versions the SDK's server side serves, newest first, read from the SDK."""
    served = set(MODERN_PROTOCOL_VERSIONS) | set(HANDSHAKE_PROTOCOL_VERSIONS)
    return [v for v in reversed(KNOWN_PROTOCOL_VERSIONS) if v in served]


def _watch_stdin(on_eof: Any) -> None:
    try:
        # Raw fd reads: a daemon thread blocked in sys.stdin's buffered reader
        # aborts the interpreter at shutdown.
        while os.read(0, 65536):
            pass
    except (OSError, ValueError):
        pass
    on_eof()


async def main_async(port: int) -> None:
    server = build_server()
    app = server.streamable_http_app(streamable_http_path="/mcp", stateless_http=True, host="127.0.0.1")

    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind(("127.0.0.1", port))
    sock.listen(128)
    real_port = sock.getsockname()[1]

    config = uvicorn.Config(
        app,
        log_config=None,
        access_log=False,
        lifespan="on",
        timeout_graceful_shutdown=2,
    )
    http = uvicorn.Server(config)

    loop = asyncio.get_running_loop()

    def request_exit() -> None:
        loop.call_soon_threadsafe(setattr, http, "should_exit", True)

    threading.Thread(target=_watch_stdin, args=(request_exit,), name="stdin-watch", daemon=True).start()

    async def announce() -> None:
        while not http.started:
            if http.should_exit:
                return
            await asyncio.sleep(0.01)
        startup = {
            "url": f"http://127.0.0.1:{real_port}/mcp",
            "language": LANGUAGE,
            "sdk": SDK_DIST,
            "sdkVersion": dist_version(SDK_DIST),
            "protocolVersions": served_protocol_versions(),
            "unsupported": [],
        }
        sys.stdout.write(json.dumps(startup, separators=(",", ":")) + "\n")
        sys.stdout.flush()

    announcer = asyncio.create_task(announce())
    try:
        await http.serve(sockets=[sock])
    finally:
        announcer.cancel()


def main() -> None:
    parser = argparse.ArgumentParser(description="Reference MCP server (Python SDK).")
    parser.add_argument("--port", type=int, default=0)
    args = parser.parse_args()

    logging.basicConfig(stream=sys.stderr, level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    # uvicorn installs its own SIGTERM/SIGINT handlers while serving; this
    # covers a SIGTERM that lands before it does.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    asyncio.run(main_async(args.port))


if __name__ == "__main__":
    main()
