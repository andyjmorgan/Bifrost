# server.session

`server.session` decides what the gateway tells an upstream server about itself when the gateway sends it a request.

## When it runs

`server.session` runs for every request the gateway sends to an upstream server: each forwarded `tools/call`, `prompts/get`, `resources/read` and directory read, and the gateway's own requests, such as fetching instructions for discover. Every forward carries its result: `clientInfo`, `capabilities` and `forwardMeta`.

By default, the gateway is anonymous to the upstream: it names itself `bifrost`, advertises no elicitation capability and strips `_meta` from outbound requests.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

This hook has no item, so there is no `upstream` variable.

## Fields

| Field | Type | Default | Effect |
| --- | --- | --- | --- |
| `clientInfo` | map | `{name: 'bifrost', version: <gateway version>}` | The client information the upstream receives |
| `capabilities.elicitation` | bool | `false` | `true` advertises elicitation to the upstream. When it is off, a compliant upstream never asks for user input |
| `meta` | bool | `false` | `true` forwards the client's `_meta` on forwarded requests, without keys that start with `io.modelcontextprotocol/`. `false` strips it |

No field stops evaluation. Later rules override earlier ones.

## What the upstream sees

The fields change the outbound request only. The client sees no difference, except that an upstream without the elicitation capability does not ask for input, so [tools/call.post.elicit](tools-call-post-elicit.md), [prompts/get.post.elicit](prompts-get-post-elicit.md) and [resources/read.post.elicit](resources-read-post-elicit.md) do not run.

If an enforce rule fails to evaluate, the gateway uses its anonymous defaults for that request.

## Nuances and limits

- `clientInfo` replaces the whole map. To pass the client through, build the map from `client.name` and `client.version`. Do not pass `client` itself, because it also carries the client's capabilities.
- `client.name` and `client.version` are self-reported. The MCP specification says servers should not make security decisions on them.
- Rules can read `caller`, so the identity can differ per caller. Once shaped, the upstream never learns more than the rules send.
- With `meta` on, the gateway still removes every `_meta` key that starts with `io.modelcontextprotocol/`, such as `io.modelcontextprotocol/clientInfo`. A client cannot use `_meta` to claim another identity upstream.

## Examples

### Pass the client name and version through

```json
{
  "name": "Pass client identity",
  "hook": "server.session",
  "when": "true",
  "then": { "clientInfo": "{'name': client.name, 'version': client.version}" }
}
```

A client that reports `claude-code` version `2.1` reaches the upstream as `{"name": "claude-code", "version": "2.1"}`. A client that sends no name makes the rule fail, and the upstream sees the default `bifrost`.

### Advertise elicitation for users only

```json
{
  "name": "Elicitation for users",
  "hook": "server.session",
  "when": "caller.type == 'user'",
  "then": { "capabilities.elicitation": "true" }
}
```

Requests on behalf of users advertise `{"elicitation": {}}`. Requests on behalf of API keys advertise no capabilities, so the upstream does not ask them for input.

### Forward request metadata to one server

```json
{
  "name": "Forward meta",
  "hook": "server.session",
  "when": "server.slug == 'tracing'",
  "then": { "meta": "true" }
}
```

A call that the client sends with `_meta` `{"trace": "x", "io.modelcontextprotocol/clientInfo": {"name": "spoof"}}` reaches the `tracing` server with `_meta` `{"trace": "x"}`. The gateway still identifies itself as `bifrost`.

## Related pages

- [Hooks and rules overview](overview.md)
- [server.settings switches](server-settings.md)
- [tools/call.post.elicit for upstream input requests](tools-call-post-elicit.md)
