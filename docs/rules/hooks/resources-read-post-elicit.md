# resources/read.post.elicit

`resources/read.post.elicit` decides whether an upstream server may ask the user for input during a read, and can reword what it asks.

## When it runs

`resources/read.post.elicit` runs when the upstream answers a forwarded `resources/read` or directory read with `resultType: "input_required"`. It runs once for each input request in the reply, in key order. [resources/read.post.result](resources-read-post-result.md) does not run on that reply. A reply with only a `requestState` and no requests is wrapped and returned without running rules.

An upstream asks for input only if the gateway advertised elicitation to it, which [server.session](server-session.md) controls. Gateway confirmations from [resources/read.pre](resources-read-pre.md) always resolve before the upstream is called.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `request` | map | One input request: `key`, `method` (always `elicitation/create`), and the request's params, such as `mode`, `message` and `requestedSchema` |
| `args` | map | `{uri: <uri>}`, with the URI as the upstream knows it |
| `uri`, `name`, `title`, `description`, `mimeType`, `annotations`, `meta`, `size`, `icons` | Various | The listed resource for this URI, or only `uri` when no listing contains it |
| `upstream` | map | The resource as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the whole round of input requests | Yes |
| `reason` | string | The error message the client receives with a deny | No |
| `message` | string | Replace the message the user sees | No |

## What the client sees

| Outcome | Result |
| --- | --- |
| Allowed | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `The upstream server asked for input, which policy does not allow.` |
| A rule fails to evaluate | `-32602`, `The upstream server asked for input, which policy does not allow.` |
| Shaped reply over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

The gateway wraps the upstream's `requestState`, if any, in its own signed state. On the retry, it forwards only the upstream's answers and, when the upstream sent one, its original state. The answers are forwarded even when the upstream sent no state.

## Nuances and limits

- A deny on any one request refuses the whole round.
- Only `elicitation/create` requests are supported, and upstream keys must not start with `bifrost.`.

## Examples

### Label prompts from the upstream

```json
{
  "name": "Label drive prompts",
  "hook": "resources/read.post.elicit",
  "when": "true",
  "then": { "message": "'Drive asks: ' + request.message" }
}
```

An upstream request with the message `Why do you need this file?` reaches the client as `Drive asks: Why do you need this file?`. The retry forwards the user's answer and the upstream's state.

### Refuse upstream prompts on reads

```json
{
  "name": "No upstream prompts on reads",
  "hook": "resources/read.post.elicit",
  "when": "true",
  "then": { "decision": "'deny'", "reason": "'This server may not ask for input during reads.'" }
}
```

The client receives `{"code": -32602, "message": "This server may not ask for input during reads."}`.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/call.post.elicit, the matching hook for tool calls](tools-call-post-elicit.md)
- [server.session for advertising elicitation upstream](server-session.md)
