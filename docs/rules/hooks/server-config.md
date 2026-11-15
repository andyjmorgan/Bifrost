# server/config

`server/config` sets switches that decide what kinds of items a server exposes and which server details pass to the client.

## When it runs

`server/config` runs first on every request that touches a server: discover, every listing, every call, read and `skills/get`, and the search tools. Each switch starts at its default, and matching rules change it.

A switch that is off removes the whole kind. Its filter, modify and call hooks do not run.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

This hook has no item, so there is no `upstream` variable.

## Fields

| Field | Type | Default | Effect |
| --- | --- | --- | --- |
| `tools` | bool | `true` | Expose tools |
| `prompts` | bool | `true` | Expose prompts |
| `resources` | bool | `true` | Expose resources, templates, skills and directory reads |
| `tasks` | bool | `false` | Expose the `io.modelcontextprotocol/tasks` extension |
| `instructions` | bool | `true` | Show the server's instructions in discover, mux excerpts and `get_server_instructions` |
| `serverInfo` | bool | `false` | On the direct route, show the upstream's name and version in discover instead of the gateway's |
| `searchRoster` | bool | `true` | Use the server's title, description and instructions, after [server/modify](server-modify.md), when search mode ranks tools |
| `progress` | bool | `true` | Forward upstream progress notifications on the request's own stream |

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
- The engine evaluates `tasks` and `progress` and passes them to the gateway. No rules run on notifications.

## Examples

### Expose tools only

```json
{
  "name": "Tools only",
  "hook": "server/config",
  "when": "true",
  "then": { "prompts": "false", "resources": "false" }
}
```

Discover advertises only `{"tools": {"listChanged": false}}`. `prompts/get` for any prompt returns `-32602`, `unknown prompt summary`, and reads return `not found`.

### Hide instructions from API keys

```json
{
  "name": "No instructions for keys",
  "hook": "server/config",
  "when": "caller.type == 'key'",
  "then": { "instructions": "false" }
}
```

Users see the server's instructions in discover. API keys see none.

### Switch tools off during a change freeze

```json
{
  "name": "Holiday freeze",
  "hook": "server/config",
  "when": "now >= timestamp('2026-12-20T00:00:00Z') && now < timestamp('2027-01-04T00:00:00Z')",
  "then": { "tools": "false" }
}
```

On December 10, 2026, `tools/list` shows the server's tools. On December 24, 2026, the list is empty and `tools/call` returns `-32602`, `unknown tool rollout`.

### Show the upstream server name

```json
{
  "name": "Show upstream",
  "hook": "server/config",
  "when": "true",
  "then": { "serverInfo": "true" }
}
```

On the direct route, discover returns the upstream's name and version in `_meta["io.modelcontextprotocol/serverInfo"]`, such as `{"name": "atlassian", "version": "9"}`, instead of `bifrost`. On the mux route, the gateway always identifies itself as `bifrost`.

## Related pages

- [Hooks and rules overview](overview.md)
- [server/modify for title, description and instructions](server-modify.md)
- [server/connect for the gateway's outbound identity](server-connect.md)
