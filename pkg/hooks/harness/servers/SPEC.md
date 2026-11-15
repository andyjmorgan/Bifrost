# Reference server contract

Three real MCP servers, one per official SDK (Go, Python, TypeScript), each built on that SDK's server side. They are the real downstream servers for end-to-end tests of the rules engine. All three must behave the same, so a test can run against any of them and expect the same result.

This file is the contract. If an SDK cannot do something here, do not fake it with hand-written protocol code: leave it out, and say so in the startup line (see "Startup") and in your report.

## Process contract

- Each server is a program started as a subprocess. It serves MCP over Streamable HTTP on `127.0.0.1`, at path `/mcp`.
- Flag `--port N` sets the port. `--port 0` (the default) picks a free port.
- Startup: the first line on stdout is one JSON object, then nothing else on stdout. Logs go to stderr.

  ```json
  {"url":"http://127.0.0.1:PORT/mcp","language":"python","sdk":"mcp","sdkVersion":"2.3.0","protocolVersions":["2026-07-28","2025-11-25"],"unsupported":["ask"]}
  ```

  `protocolVersions` is what this SDK version really speaks, newest first, read from the SDK, not assumed. `unsupported` lists the names of tools, prompts or resources from this contract that the SDK could not provide.
- The server exits when it receives SIGTERM or when its stdin is closed.
- Use the SDK's stateless mode if it has one. Prefer protocol version 2026-07-28 when the SDK supports it.
- Launch commands, run from the repository root:
  - Go: `go run ./pkg/hooks/harness/servers/go --port 0`
  - Python: `pkg/hooks/harness/servers/python/.venv/bin/python pkg/hooks/harness/servers/python/server.py --port 0`
  - TypeScript: `node pkg/hooks/harness/servers/typescript/server.mjs --port 0`

## Server identity

- name `refserver-<language>` (`refserver-go`, `refserver-python`, `refserver-typescript`), version `1.0.0`
- instructions, exactly: `Reference server.\n\n## Notes\nUse search first.`

## Tools

Every tool has an input schema of type `object`. "Required" means listed in `required`. Text results are one `text` content block unless stated.

Schemas are compared across the three servers, so write them the same way:

- a tool with no arguments has the schema `{"type":"object","properties":{}}`
- a tool with arguments has `type`, `properties` and `required`, and nothing else at the top level
- a documented default goes in the schema: `slow_count.delayMs` is `{"type":"integer","default":20}`
- no `title`, `description` or `additionalProperties` keys inside input schemas
- an optional prompt argument leaves out `required`

| Name | Arguments | Annotations | Result |
| --- | --- | --- | --- |
| `echo` | `text` string, required | read-only | text: the `text` value. `structuredContent`: `{"text": <text>}` |
| `add` | `a` number, required; `b` number, required | read-only | text: the sum as a decimal string, such as `5` or `2.5`. `structuredContent`: `{"sum": <sum>}` |
| `search` | `query` string, required; `limit` integer, optional; `space` string, optional; `telemetry` object with required string `intent`, required | read-only | `structuredContent`: `{"args": <the arguments object exactly as received>}`. text: that same object as compact JSON |
| `delete_page` | `id` string, required | destructive | text: `deleted <id>` |
| `get_report` | none | read-only | text: `Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7.` `structuredContent`: `{"user":{"email":"ana@example.com","name":"Ana"},"rows":[{"id":1,"secret":"s1"},{"id":2,"secret":"s2"}],"note":null}` |
| `slow_count` | `steps` integer, required; `delayMs` integer, optional, default 20 | read-only | If the request carried a progress token: `steps` progress notifications, one every `delayMs`, with `progress` = i, `total` = steps, `message` = `step <i> of <steps>: reading db-internal-7`. Then text: `counted <steps>` |
| `fail` | none | none | a tool error: `isError: true`, text `boom` |
| `crash` | none | none | a protocol error: the handler raises, so the client gets a JSON-RPC error, not a result. The code is `-32603`. The message is whatever the SDK produces and is not compared |
| `picture` | none | read-only | three content blocks in this order: text `a picture`; an image, PNG, the 1x1 transparent pixel below; a resource link to `ref://docs/guide` with name `guide` |
| `ask` | none | none | The server asks the user for input through the SDK's elicitation API: message `Which project?`, a form with one required string field `project`. On accept, text `project=<value>`. On decline, text `declined`. On cancel, text `cancelled` |
| `inspect` | none | read-only | `structuredContent` describing the request as the server saw it: `{"protocolVersion": <negotiated version or null>, "clientInfo": <object or null>, "capabilities": <the client's declared capabilities or null>, "meta": <the request's _meta object or null>, "headers": {<lower-cased header name>: <value>, ...}}`. Include at least the `mcp-*`, `accept` and `content-type` headers, and never `authorization` or `cookie`. text: `inspected` |

The PNG, base64: `iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==`

Tool descriptions: `echo`: `Return the text.` `add`: `Add two numbers.` `search`: `Search pages.` `delete_page`: `Delete a page.` `get_report`: `Return the report.` `slow_count`: `Count slowly, reporting progress.` `fail`: `Always fails.` `crash`: `Always crashes.` `picture`: `Return a picture.` `ask`: `Ask which project.` `inspect`: `Describe the request.`

## Prompts

| Name | Arguments | Result |
| --- | --- | --- |
| `greet` | `who`, required | one user message, text `Hello, <who>!` |
| `summarize` | `topic`, required; `tone`, optional | one user message, text `Summarize <topic> in a <tone> tone.` with `neutral` when `tone` is absent |

## Resources

Listed resources:

| URI | Name | MIME type | Text |
| --- | --- | --- | --- |
| `ref://docs/guide` | `guide` | `text/markdown` | `# Guide\nSee ref://docs/faq and https://example.com/x.` |
| `ref://docs/faq` | `faq` | `text/plain` | `FAQ` |
| `ref://secrets/key` | `key` | `text/plain` | `sk-test-123` |

One template: `ref://notes/{id}`, name `note`, MIME type `text/plain`. Reading `ref://notes/<id>` returns text `note <id>`.

One unlisted resource: `ref://hidden/thing` is not in the listing and matches no template, but reading it returns text `hidden thing`. If the SDK cannot serve a URI it does not list, add it to `unsupported` as `ref://hidden/thing`.

Reading any other URI is the SDK's normal "not found" error.

## Not in scope

Tasks, skills, MCP Apps, sampling, roots, logging notifications, list-changed notifications, subscriptions and authentication.
