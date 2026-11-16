# prompts/get.post.elicit

`prompts/get.post.elicit` decides whether an upstream server may ask the user for input during `prompts/get`, and can reword what it asks.

## When it runs

`prompts/get.post.elicit` runs when the upstream answers a forwarded `prompts/get` with `resultType: "input_required"`. It runs once for each input request in the reply, in key order. A reply with only a `requestState` and no requests is wrapped and returned without running rules.

An upstream asks for input only if the gateway advertised elicitation to it, which [server.session](server-session.md) controls. Gateway confirmations from [prompts/get.pre](prompts-get-pre.md) always resolve before the upstream is called.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `request` | map | One input request: `key`, `method` (always `elicitation/create`), and the request's params, such as `mode`, `message` and `requestedSchema` |
| `args` | map | The arguments as sent upstream: upstream argument names, with `prompts/get.pre` patches applied |
| `name`, `title`, `description`, `arguments`, `meta`, `icons` | Various | The exposed prompt |
| `upstream` | map | The prompt as the upstream sent it |
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

The gateway wraps the upstream's `requestState`, if any, in its own signed state. On the retry, it forwards only the upstream's answers and, when the upstream sent one, its original state. The answers are forwarded even when the upstream sent no state.

## Nuances and limits

- A deny on any one request refuses the whole round.
- Only `elicitation/create` requests are supported, and upstream keys must not start with `bifrost.`.
- `args` uses upstream argument names, not the names the client sees, because these are the arguments the upstream received.

## Examples

### Label prompts from the upstream

```json
{
  "name": "Label wiki prompts",
  "hook": "prompts/get.post.elicit",
  "when": "true",
  "then": { "message": "'The wiki asks: ' + request.message" }
}
```

An upstream request with the message `How detailed?` reaches the client as `The wiki asks: How detailed?`. The retry forwards the user's answer and the upstream's state.

### Refuse upstream prompts for API keys

```json
{
  "name": "No prompts for keys",
  "hook": "prompts/get.post.elicit",
  "when": "caller.type == 'key'",
  "then": { "decision": "'deny'", "reason": "'This prompt needs user input, which API keys cannot give.'" }
}
```

For an API key, the client receives `{"code": -32602, "message": "This prompt needs user input, which API keys cannot give."}`.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/call.post.elicit, the matching hook for tool calls](tools-call-post-elicit.md)
- [server.session for advertising elicitation upstream](server-session.md)
