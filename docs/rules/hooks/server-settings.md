# server.settings

`server.settings` sets switches that decide what kinds of items a server exposes and which server details pass to the client.

## When it runs

`server.settings` runs first on every request that touches a server: discover, every listing, every call, read and `skills/get`, and the search tools. Each switch starts at its default, and matching rules change it.

A switch that is off removes the whole kind. Its list and call hooks do not run.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

This hook has no item, so there is no `upstream` variable.

## Fields

| Field | Type | Default | Effect |
| --- | --- | --- | --- |
| `tools` | bool | `true` | Expose tools |
| `prompts` | bool | `true` | Expose prompts |
| `resources` | bool | `true` | Expose resources, templates, skills and directory reads |
| `tasks` | bool | `false` | Reserved. The gateway does not support the `io.modelcontextprotocol/tasks` extension yet, so this switch stays off whatever a rule sets, and a task handle a server returns is refused, for a tool call, a prompt or a read |
| `instructions` | bool | `true` | Show the server's instructions in discover, mux excerpts and `get_server_instructions` |
| `serverInfo` | bool | `false` | On the direct route, show the upstream's name and version in discover instead of the gateway's |
| `searchRoster` | bool | `true` | Use the server's title, description and instructions, after [server/discover.post](server-discover-post.md), when search mode ranks tools |
| `progress` | bool | `true` | Relay upstream progress events on the request's own stream. Only servers that speak MCP 2026-07-28 can have it; for any other server it stays off whatever a rule sets |

No field stops evaluation. Later rules override earlier ones.

## What the client sees

| Switch off | Result |
| --- | --- |
| `tools` | `tools/list` omits the server's tools. `tools/call` returns `-32602`, `unknown tool <name>` |
| `prompts` | `prompts/list` omits them. `prompts/get` returns `-32602`, `unknown prompt <name>` |
| `resources` | Resource, template and skill listings omit them. Reads and `skills/get` return `-32602`, `not found: <uri>` |
| `instructions` | Discover has no instructions. `get_server_instructions` returns an error result |

Discover advertises a capability only when at least one server on the route has that kind switched on. On the mux route, discover always advertises tools, because the gateway has its own tool. Discover declares the skills extension, `io.modelcontextprotocol/skills`, only when a server with skills has `resources` on.

## Nuances and limits

- If an enforce rule fails to evaluate, all eight switches are off for that request.
- If the entitlement's enabled rules do not compile, every switch is off.
- Switches are per server. On the mux route, each server's switches apply only to that server's items.
- When a rule asks for `tasks`, or for `progress` on a server that cannot have it, the gateway turns the switch off and records why in the hits, under the rule label `(gateway)`.
- Progress events pass through the progress hooks: [tools/call.post.progress](tools-call-post-progress.md), [prompts/get.post.progress](prompts-get-post-progress.md) and [resources/read.post.progress](resources-read-post-progress.md). No rule runs on any other notification.

## Examples

### Expose tools only

```json
{
  "name": "Tools only",
  "hook": "server.settings",
  "when": "true",
  "then": { "prompts": "false", "resources": "false" }
}
```

Discover advertises only `{"tools": {"listChanged": false}}`. `prompts/get` for any prompt returns `-32602`, `unknown prompt summary`, and reads return `not found`.

### Hide instructions from API keys

```json
{
  "name": "No instructions for keys",
  "hook": "server.settings",
  "when": "caller.type == 'key'",
  "then": { "instructions": "false" }
}
```

Users see the server's instructions in discover. API keys see none.

### Switch tools off during a change freeze

```json
{
  "name": "Holiday freeze",
  "hook": "server.settings",
  "when": "now >= timestamp('2026-12-20T00:00:00Z') && now < timestamp('2027-01-04T00:00:00Z')",
  "then": { "tools": "false" }
}
```

On December 10, 2026, `tools/list` shows the server's tools. On December 24, 2026, the list is empty and `tools/call` returns `-32602`, `unknown tool rollout`.

### Show the upstream server name

```json
{
  "name": "Show upstream",
  "hook": "server.settings",
  "when": "true",
  "then": { "serverInfo": "true" }
}
```

On the direct route, discover returns the upstream's name and version in `_meta["io.modelcontextprotocol/serverInfo"]`, such as `{"name": "atlassian", "version": "9"}`, instead of `bifrost`. On the mux route, the gateway always identifies itself as `bifrost`.

## Related pages

- [Hooks and rules overview](overview.md)
- [server/discover.post for title, description and instructions](server-discover-post.md)
- [server.session for the gateway's outbound identity](server-session.md)
