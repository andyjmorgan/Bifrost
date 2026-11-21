# server.settings

`server.settings` switches whole parts of a server on or off: its tools, prompts and resources, and which server details pass to the client.

## When it runs

`server.settings` runs first on every request that touches a server: discover, every listing, every call, read and `skills/get`, and the search tools. Each switch starts at its default, and matching rules change it.

A switch that is off removes the whole kind. Its list and call hooks do not run.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `request` | `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time`. `request.method` is `server/discover`, and `request.params` holds only `_meta` |
| `server` | `server.id`, `server.slug`, and `server.name`, `server.title`, `server.description`, `server.instructions`, `server.protocolVersion`, `server.url` where the gateway knows them |

This hook has no item, so there is no `upstream`. A rule cannot read `settings`: the switches are write-only.

## What a rule can set

| Path | Type | Default | Effect |
| --- | --- | --- | --- |
| `settings.tools` | bool | `true` | Expose tools |
| `settings.prompts` | bool | `true` | Expose prompts |
| `settings.resources` | bool | `true` | Expose resources, templates, skills and directory reads |
| `settings.tasks` | bool | `false` | Reserved. The gateway does not support the `io.modelcontextprotocol/tasks` extension yet, so this switch stays off whatever a rule sets. A task handle a server returns is refused, for a tool call, a prompt or a read |
| `settings.instructions` | bool | `true` | Show the server's instructions in discover, mux excerpts and `get_server_instructions` |
| `settings.serverInfo` | bool | `false` | On the direct route, show the server's name and version in discover instead of the gateway's |
| `settings.searchRoster` | bool | `true` | Use the server's title, description and instructions, after [server/discover.post](server-discover-post.md), when search mode ranks tools |
| `settings.progress` | bool | `true` | Relay the server's progress notifications on the request's own stream. Only servers that speak MCP 2026-07-28 can have it. For any other server it stays off whatever a rule sets |

A rule here cannot deny and cannot ask the user to confirm. Nothing stops evaluation. Later rules override earlier ones.

## What the client sees

| Switch off | Result |
| --- | --- |
| `settings.tools` | `tools/list` omits the server's tools. `tools/call` returns `-32602`, `unknown tool <name>` |
| `settings.prompts` | `prompts/list` omits them. `prompts/get` returns `-32602`, `unknown prompt <name>` |
| `settings.resources` | Resource, template and skill listings omit them. Reads and `skills/get` return `-32602`, `not found: <uri>` |
| `settings.instructions` | Discover has no instructions. `get_server_instructions` returns an error result |

Discover advertises a capability only when at least one server on the route has that kind switched on. On the mux route, discover always advertises tools, because the gateway has its own tool. Discover declares the skills extension, `io.modelcontextprotocol/skills`, only when a server with skills has `settings.resources` on.

## Examples

### Expose tools only

```json
{
  "name": "Tools only",
  "hook": "server.settings",
  "when": "true",
  "then": { "settings.prompts": "false", "settings.resources": "false" }
}
```

Discover advertises only `{"tools": {"listChanged": false}}`. `prompts/get` for any prompt returns `-32602`, `unknown prompt summary`, and reads return `not found`.

### Hide instructions from API keys

```json
{
  "name": "No instructions for keys",
  "hook": "server.settings",
  "when": "request.caller.type == 'apikey'",
  "then": { "settings.instructions": "false" }
}
```

Other callers see the server's instructions in discover. API keys see none.

### Switch tools off during a change freeze

```json
{
  "name": "Holiday freeze",
  "hook": "server.settings",
  "when": "request.time >= timestamp('2026-12-20T00:00:00Z') && request.time < timestamp('2027-01-04T00:00:00Z')",
  "then": { "settings.tools": "false" }
}
```

On December 10, 2026, `tools/list` shows the server's tools. On December 24, 2026, the list is empty and `tools/call` returns `-32602`, `unknown tool rollout`.

### Show the server's own name

```json
{
  "name": "Show upstream",
  "hook": "server.settings",
  "when": "true",
  "then": { "settings.serverInfo": "true" }
}
```

On the direct route, discover returns the server's name and version in `_meta["io.modelcontextprotocol/serverInfo"]`, such as `{"name": "atlassian", "version": "9"}`, instead of `bifrost`. On the mux route, the gateway always identifies itself as `bifrost`.

## Nuances and limits

- If an enforce rule fails to evaluate, all eight switches are off for that request.
- If the server's enabled rules do not compile, every switch is off.
- Switches are per server. On the mux route, each server's switches apply only to that server's items.
- When a rule asks for `settings.tasks`, or for `settings.progress` on a server that cannot have it, the gateway turns the switch off. It records why in the hits, under the rule label `(gateway)`.
- Progress notifications pass through the progress hooks: [tools/call.post.progress](tools-call-post-progress.md), [prompts/get.post.progress](prompts-get-post-progress.md) and [resources/read.post.progress](resources-read-post-progress.md). No rule runs on any other notification.
- `request.caller` fields come from the gateway. A plain read of a field the gateway does not supply fails the rule, which switches everything off.

## Related pages

- [Hooks and rules overview](overview.md)
- [server/discover.post for title, description and instructions](server-discover-post.md)
- [server.session for what the server is told about the client](server-session.md)
