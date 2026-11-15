# tools/input

`tools/input` decides whether an upstream server may ask the user for input during a tool call, and can reword what it asks.

## When it runs

`tools/input` runs when the upstream answers a forwarded `tools/call` with `resultType: "input_required"`. It runs once for each input request in the reply, in key order. [tools/result](tools-result.md) does not run on that reply.

The gateway recognizes an `input_required` reply that carries input requests, a `requestState`, or both. A reply with only a `requestState` has no requests for rules to check, so the gateway wraps the state and returns it to the client.

An upstream asks for input only if the gateway advertised elicitation to it. That is off by default and controlled by [server/connect](server-connect.md).

Gateway confirmations from [tools/call](tools-call.md) always resolve before the upstream is called, so the gateway and the upstream never ask in the same round.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `request` | map | One input request: `key`, `method` (always `elicitation/create`), and the request's params, such as `mode`, `message`, `requestedSchema`, `url` and `elicitationId` |
| `args` | map | The exact arguments forwarded upstream, under upstream names, after call patches, pins and removal of added parameters |
| `name`, `title`, `description`, `annotations`, `annotationsRaw`, `meta`, `inputSchema`, `outputSchema`, `icons` | Various | The exposed tool |
| `upstream` | map | The tool as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the whole round of input requests | Yes |
| `reason` | string | The text the client receives with a deny | No |
| `message` | string | Replace the message the user sees. Later rules read the new value through `request.message` | No |

## What the client sees

| Outcome | Result |
| --- | --- |
| Allowed | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}`, with each request's message as the rules left it |
| Denied | `{"isError": true, "content": [{"type": "text", "text": "<reason>"}]}`. Without a reason, the text is `The upstream server asked for input, which policy does not allow.` |
| A rule fails to evaluate | An error result: `The upstream server asked for input, which policy does not allow.` |
| Shaped reply over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

The gateway wraps the upstream's `requestState`, if any, inside its own signed state. The client never sees the upstream's state directly. On the retry, the gateway checks its state, then forwards only the upstream's answers and, when the upstream sent one, its original `requestState`. The answers are forwarded even when the upstream sent no `requestState`.

## Nuances and limits

- A deny on any one request refuses the whole round.
- The gateway supports only `elicitation/create` requests. Any other method returns an error result.
- Upstream request keys must not start with `bifrost.`, which the gateway reserves for its own confirmations. Such a reply returns an error result.
- `args` holds upstream parameter names, not the names the client sees, because these are the arguments the upstream received. A value that a `tools/call` rule patched or a pin forced appears as forwarded.
- The size limit applies to the reply after rules reword messages, so a long message cannot bypass it.
- On the retry, an answer to a key that the upstream did not ask returns `-32602`, `no pending input request "<key>"`.

## Examples

### Label prompts from the upstream

```json
{
  "name": "Label Jira prompts",
  "hook": "tools/input",
  "when": "true",
  "then": { "message": "'Jira asks: ' + request.message" }
}
```

An upstream request with the message `Which project?` reaches the client as `Jira asks: Which project?`. When the client retries with `{"project": {"action": "accept", "content": {"p": "OPS"}}}`, the upstream receives that answer and its own state.

### Refuse upstream prompts for API keys

```json
{
  "name": "No prompts for keys",
  "hook": "tools/input",
  "when": "caller.type == 'key'",
  "then": { "decision": "'deny'", "reason": "'This tool needs user input, which API keys cannot give.'" }
}
```

For an API key, the client receives `{"isError": true, "content": [{"type": "text", "text": "This tool needs user input, which API keys cannot give."}]}`.

### Refuse URL-mode requests

```json
{
  "name": "No URL mode",
  "hook": "tools/input",
  "when": "request.mode == 'url'",
  "then": { "decision": "'deny'" }
}
```

An upstream request that sends the user to a URL returns the error result `The upstream server asked for input, which policy does not allow.` Form requests pass through.

## Related pages

- [Hooks and rules overview](overview.md)
- [server/connect for advertising elicitation upstream](server-connect.md)
- [tools/call for gateway confirmations](tools-call.md)
- [prompts/input](prompts-input.md) and [resources/input](resources-input.md), the matching hooks for prompts and reads
