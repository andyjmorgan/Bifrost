# resources/read.pre

`resources/read.pre` decides whether a read goes ahead, with a reason the client can see, or asks the user to confirm first.

## When it runs

`resources/read.pre` runs once for each `resources/read` and each `resources/directory/read` request, after the URI passes every list hook and before the upstream call:

1. [server.settings](server-settings.md). If `resources` is off, the URI is not found.
2. The gateway refuses a URI that is not in canonical form.
3. On the mux route, the gateway removes the slug prefix, or resolves an unprefixed URI through [fallback resolution](overview.md#fallback-resolution).
4. [skills/list.post](skills-list-post.md), [resources/list.post](resources-list-post.md) and [resources/templates/list.post](resources-templates-list-post.md) check the URI. Any deny makes it unknown.
5. `resources/read.pre` rules run.
6. The upstream read. An `input_required` reply goes to [resources/read.post.elicit](resources-read-post-elicit.md). Text content goes to [resources/read.post.result](resources-read-post-result.md).

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `uri` | string | The URI as the upstream knows it, without the mux prefix |
| `name`, `title`, `description`, `mimeType`, `annotations`, `meta`, `size`, `icons` | Various | The listed resource for this URI, after `resources/list.post`. For a URI no listing contains, these are empty |
| `upstream` | map | The listed resource as the upstream sent it |
| `args` | map | `{uri: <uri>}` |
| `input` | map | The user's answers to this gateway's confirmations, by key. Empty on the first request |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the read | Yes |
| `reason` | string | The error message the client receives with a deny | No |
| `result` | `elicit(...)` | Ask the user to confirm first | Yes |

## What the client sees

`resources/read` has no error result, so refusals are JSON-RPC errors.

| Outcome | Result |
| --- | --- |
| Allowed | The upstream's contents, after `resources/read.post.result`, with the gateway's `ttlMs` and `cacheScope: "private"`. On the mux route, `contents[].uri` carries the slug prefix |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `resources/read was refused by policy` |
| Confirmation | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Confirmation, client cannot elicit | `-32602`, `This request needs confirmation (<message>), which this client does not support.`, or the error argument of `elicit()` |
| A rule fails to evaluate | `-32602`, `resources/read was refused by policy` |

Confirmations work as in [tools/call.pre](tools-call-pre.md#elicitation). The challenge is bound to the upstream URI and the method. A directory read binds `resources/directory/read`, so a confirmation given for a read cannot be used for a directory read of the same URI, or the other way round. Refusals never name the rule that refused.

## Nuances and limits

- Use `resources/list.post` to make a URI look unknown. Use `resources/read.pre` when the client should learn why it cannot read the URI.
- The same rules run for directory reads. Rules cannot tell a read from a directory read, but confirmations are not shared between them.
- The URI comes from the client. When a confirmation message includes it, use the template form of `elicit()`, so the URI is quoted and cannot pose as the gateway's wording.
- Read content is never cached. Every read reaches the upstream.

## Examples

### Deny with a reason

```json
{
  "name": "ENG via search",
  "hook": "resources/read.pre",
  "when": "uri.startsWith('confluence://ENG/')",
  "then": { "decision": "'deny'", "reason": "'ENG pages are available only through the search tool.'" }
}
```

`confluence://ENG/runbook` stays in `resources/list`, but a read returns `{"code": -32602, "message": "ENG pages are available only through the search tool."}`.

### Confirm before reading

```json
[
  {
    "name": "Confirm read",
    "hook": "resources/read.pre",
    "when": "uri.startsWith('file:///secrets/') && !has(input.ok)",
    "then": { "result": "elicit('ok', 'Share {uri} with the assistant?', {'uri': uri})" }
  }
]
```

A read of `file:///secrets/db.txt` returns `input_required` with the message `Share "file:///secrets/db.txt" with the assistant?`. A retry that declines returns `-32602`, `Declined.`, with no rule needed. A retry that accepts with `{"confirm": true}` reaches the upstream. Each challenge is answered once, so after a decline the client sends the read again to be asked afresh.

### Skills for users only

```json
{
  "name": "Skills for users",
  "hook": "resources/read.pre",
  "when": "caller.type == 'key' && uri.startsWith('skill://')",
  "then": { "decision": "'deny'", "reason": "'Skills are available to users only.'" }
}
```

For an API key, a read of `skill://notebooks/SKILL.md` and a directory read of `skill://notebooks/` both return `-32602`, `Skills are available to users only.` Users can read both.

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/list.post for hiding URIs](resources-list-post.md)
- [resources/read.post.result for shaping read text](resources-read-post-result.md)
- [Elicitation in tools/call.pre](tools-call-pre.md#elicitation)
