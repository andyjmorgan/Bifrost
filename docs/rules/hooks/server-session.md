# server.session

`server.session` decides what the gateway tells a server about the client when it opens a session. Nothing about the client is passed on unless a rule here says so.

## When it runs

`server.session` runs for every request the gateway sends to a server: each forwarded `tools/call`, `prompts/get`, `resources/read` and directory read, and the gateway's own requests, such as fetching instructions for discover. Every forward carries its result: the client information, the capabilities and whether `_meta` is forwarded.

By default, the gateway is anonymous to the server. It names itself `bifrost`, advertises no elicitation capability and strips `_meta` from outbound requests.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `request` | `request.client`, with `clientInfo` and `capabilities` as the client declared them. Also `request.caller`, `request.headers`, `request.route`, `request.flags`, `request.time`. `request.method` is `server/discover`, and `request.params` holds only `_meta` |
| `server` | `server.slug`, and `server.name`, `server.title`, `server.description`, `server.instructions`, `server.protocolVersion`, `server.url` where the gateway knows them |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

This hook has no item, so there is no `upstream`. A rule cannot read `session`: it is write-only.

`request.client.clientInfo` is absent when the client sent none, and so are its `name`, `version` and `title`. Read them with the [optional forms](overview.md#optional-values), or guard with `has(request.client.clientInfo)`.

## What a rule can set

| Path | Type | Default | Effect |
| --- | --- | --- | --- |
| `session.clientInfo` | map | `{name: 'bifrost', version: <gateway version>}` | The client information the server receives |
| `session.capabilities.elicitation` | bool | `false` | `true` advertises elicitation to the server. When it is off, a compliant server never asks the user a question |
| `session._meta` | bool | `false` | `true` forwards the client's `_meta` on forwarded requests, without keys that start with `io.modelcontextprotocol/`. `false` strips it |

A rule here cannot deny and cannot ask the user to confirm. Nothing stops evaluation. Later rules override earlier ones.

## What the server sees

These paths change the outbound request only. The client sees no difference, with one exception. A server that is not told about elicitation does not ask questions, so [tools/call.post.elicit](tools-call-post-elicit.md), [prompts/get.post.elicit](prompts-get-post-elicit.md) and [resources/read.post.elicit](resources-read-post-elicit.md) do not run.

If an enforce rule fails to evaluate, the gateway uses its anonymous defaults for that request.

## Examples

### Pass the client's identity through

```json
{
  "name": "Pass client identity",
  "hook": "server.session",
  "when": "has(request.client.clientInfo)",
  "then": { "session.clientInfo": "request.client.clientInfo" }
}
```

A client that reports `claude-code` version `2.1` reaches the server as `{"name": "claude-code", "version": "2.1"}`. For a client that sends no client information, the rule does not match, and the server sees the default `bifrost`.

### Advertise elicitation except for API keys

```json
{
  "name": "Elicitation for users",
  "hook": "server.session",
  "when": "request.caller.type != 'apikey'",
  "then": { "session.capabilities.elicitation": "true" }
}
```

Requests on behalf of users advertise `{"elicitation": {}}`. Requests on behalf of API keys advertise no capabilities, so the server does not ask them questions.

### Forward request metadata to one server

```json
{
  "name": "Forward meta",
  "hook": "server.session",
  "when": "server.slug == 'tracing'",
  "then": { "session._meta": "true" }
}
```

A call that the client sends with `_meta` `{"trace": "x", "io.modelcontextprotocol/clientInfo": {"name": "spoof"}}` reaches the `tracing` server with `_meta` `{"trace": "x"}`. The gateway still identifies itself as `bifrost`.

## Nuances and limits

- `session.clientInfo` replaces the whole map. `request.client.clientInfo` holds only the client's name, version and title, so it is safe to pass as it is.
- `request.client.clientInfo` is self-reported. The MCP specification says servers should not make security decisions on it.
- A plain read such as `request.client.clientInfo.name` fails when the client sent no client information. The rule fails, and the gateway falls back to its anonymous defaults.
- Rules can read `request.caller`, so the identity can differ for each caller. The server never learns more than the rules send.
- With `session._meta` on, the gateway still removes every `_meta` key that starts with `io.modelcontextprotocol/`, such as `io.modelcontextprotocol/clientInfo`. A client cannot use `_meta` to claim another identity with the server.
- On Bifrost, `session._meta` forwards only the keys on the gateway's input allow-list, `MCPGW_META_ALLOW_IN`. The list is empty unless set.
- The gateway offers elicitation to a server only when the client declared elicitation on this request, and only when the server speaks MCP 2026-07-28. Otherwise it leaves the capability out and records why in the hits.

## Related pages

- [Hooks and rules overview](overview.md)
- [server.settings switches](server-settings.md)
- [tools/call.post.elicit for questions from the server](tools-call-post-elicit.md)
