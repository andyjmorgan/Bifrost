# resources/read.pre

`resources/read.pre` decides whether a read goes ahead, with a reason the client can see, or asks the user to confirm first.

## When it runs

`resources/read.pre` runs once for each `resources/read` and each `resources/directory/read` request, after the URI passes every list hook and before the call to the server:

1. [server.settings](server-settings.md). If `settings.resources` is off, the URI is not found.
2. The gateway refuses a URI that is not in canonical form.
3. On the mux route, the gateway removes the slug prefix, or resolves an unprefixed URI through [fallback resolution](overview.md#fallback-resolution).
4. [skills/list.post](skills-list-post.md), [resources/list.post](resources-list-post.md) and [resources/templates/list.post](resources-templates-list-post.md) check the URI. Any deny makes it unknown.
5. `resources/read.pre` rules run.
6. The read from the server. An `input_required` reply goes to [resources/read.post.elicit](resources-read-post-elicit.md). The contents go to [resources/read.post.result](resources-read-post-result.md).

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `resource` | The listed resource for this URI, after `resources/list.post`: `resource.uri`, `resource.name`, `resource.title`, `resource.description`, `resource.mimeType`, `resource.size`, `resource.annotations`, `resource.icons`, `resource._meta` |
| `resource.upstream` | The listed resource as the server declares it |
| `request.params` | `request.params.uri`, the URI as the server knows it, without the mux prefix. `request.params._meta`, when the client sent one |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

`request.upstream` does not exist here, because nothing has been sent to the server yet.

For a URI that no listing contains, only `resource.uri` and `resource.upstream.uri` are present. A plain read of any other field fails the rule, which refuses the read. Read those fields with the [optional forms](overview.md#optional-values), such as `resource.?mimeType.orValue('')`.

`request.method` is `resources/read` for a read and `resources/directory/read` for a directory read.

## What a rule can set

A rule here sets no path. It only decides.

A rule here can deny, and can ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the read. The server is not called | Yes |
| `reason` | string | The error message the client receives with a deny | No |
| `confirm` | `elicit(...)` | Ask the user to confirm first | Yes |

A rule that sets any path is refused when saved. For example, `request.params.uri` gives `request.params.uri cannot be changed on resources/read.pre; a rule here can set nothing`.

## What the client sees

`resources/read` has no error result, so refusals are JSON-RPC errors.

| Outcome | Result |
| --- | --- |
| Allowed | The server's contents, after `resources/read.post.result`, with the gateway's `ttlMs` and `cacheScope: "private"`. On the mux route, `contents[].uri` carries the slug prefix |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `resources/read was refused by policy` |
| Confirmation | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Confirmation, client cannot elicit | `-32602`, `This request needs confirmation (<message>), which this client does not support.`, or the error argument of `elicit()` |
| A rule fails to evaluate | `-32602`, `resources/read was refused by policy` |

Confirmations work as in [tools/call.pre](tools-call-pre.md#confirmation). The challenge is bound to the URI as the server knows it and to the method. A directory read binds `resources/directory/read`. So a confirmation given for a read cannot be used for a directory read of the same URI, or the other way round. Refusals never name the rule that refused.

## Examples

### Deny with a reason

```json
{
  "name": "ENG via search",
  "hook": "resources/read.pre",
  "when": "resource.uri.startsWith('confluence://ENG/')",
  "then": { "decision": "'deny'", "reason": "'ENG pages are available only through the search tool.'" }
}
```

`confluence://ENG/runbook` stays in `resources/list`, but a read returns `{"code": -32602, "message": "ENG pages are available only through the search tool."}`.

### Confirm before reading

```json
{
  "name": "Confirm read",
  "hook": "resources/read.pre",
  "when": "resource.uri.startsWith('file:///secrets/')",
  "then": { "confirm": "elicit('ok', 'Share {uri} with the assistant?', {'uri': resource.uri})" }
}
```

A read of `file:///secrets/db.txt` returns `input_required` with the message `Share "file:///secrets/db.txt" with the assistant?`. A retry that declines returns `-32602`, `Declined.`, with no rule needed. A retry that accepts with `{"confirm": true}` reaches the server. Each challenge is answered once, so after a decline the client sends the read again to be asked afresh.

### Refuse skill reads for API keys

```json
{
  "name": "Skills for users",
  "hook": "resources/read.pre",
  "when": "request.caller.type == 'apikey' && resource.uri.startsWith('skill://')",
  "then": { "decision": "'deny'", "reason": "'Skills are available to users only.'" }
}
```

For an API key, a read of `skill://notebooks/SKILL.md` and a directory read of `skill://notebooks/` both return `-32602`, `Skills are available to users only.` Other callers can read both.

## Nuances and limits

- Use `resources/list.post` to make a URI look unknown. Use `resources/read.pre` when the client should learn why it cannot read the URI.
- The same rules run for directory reads. A rule can tell the two apart with `request.method`. Confirmations are not shared between them.
- The URI comes from the client. When a confirmation message includes it, use the template form of `elicit()`, so the URI is quoted and cannot pose as the gateway's wording.
- Read contents are never cached by the gateway. Every read reaches the server.
- A read that a confirmation let through tells clients not to cache it, with `ttlMs: 0`, so the next read asks again.

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/list.post for hiding URIs](resources-list-post.md)
- [resources/read.post.result for shaping read contents](resources-read-post-result.md)
- [Confirmation in tools/call.pre](tools-call-pre.md#confirmation)
